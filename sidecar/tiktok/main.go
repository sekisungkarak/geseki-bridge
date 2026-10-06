// Geseki Bridge — TikTok sidecar.
//
// Wraps github.com/steampoweredtaco/gotiktoklive (MIT) and speaks
// newline-delimited JSON over stdin/stdout so the OBS plugin can supervise it
// without opening a second port.
//
// Wire contract: ../docs/protocol.md §4.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	gotiktoklive "github.com/steampoweredtaco/gotiktoklive"
)

const version = "0.5.2"

// ---------------------------------------------------------------- stdio I/O

// stdout is shared by the state machine goroutine and the event loop, so every
// write goes through outMu: interleaved halves of two JSON lines would corrupt
// the stream the plugin parses.
var outMu sync.Mutex

type outMsg struct {
	Ev      string      `json:"ev"`
	State   string      `json:"state,omitempty"`
	Message string      `json:"message,omitempty"`
	Event   string      `json:"event,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

func emit(m outMsg) {
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	outMu.Lock()
	defer outMu.Unlock()
	_, _ = os.Stdout.Write(append(b, '\n'))
}

func emitState(state, message string) {
	emit(outMsg{Ev: "state", State: state, Message: message})
}

// logf writes diagnostics to stderr. The plugin captures it; it never touches
// stdout, which carries protocol frames only.
func logf(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "[tiktok] "+format+"\n", a...)
}

// ------------------------------------------------------------ session state

var (
	sessMu     sync.Mutex
	live       *gotiktoklive.Live
	cancelFunc context.CancelFunc
)

func setSession(l *gotiktoklive.Live, cancel context.CancelFunc) {
	sessMu.Lock()
	defer sessMu.Unlock()
	live = l
	cancelFunc = cancel
}

// stopSession cancels the running tracker and waits for the event loop to
// drain. Without the cancel the old socket keeps emitting into a dead widget
// after a username change.
func stopSession() {
	sessMu.Lock()
	cancel := cancelFunc
	cancelFunc = nil
	live = nil
	sessMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ------------------------------------------------------------------ connect

type connectCmd struct {
	Cmd      string `json:"cmd"`
	Username string `json:"username"`
	// SignerUrl is the local sign server. Set unless the user turned on
	// the alternative connection mode.
	SignerUrl string `json:"signerUrl"`
	// APIKey is optional and belongs to the alternative connection mode,
	// where it raises the signing rate limit. It is unused locally.
	APIKey string `json:"apiKey"`
}

func handleConnect(c connectCmd) {
	stopSession()

	username := c.Username
	if username == "" {
		emitState("error", "missing username")
		return
	}
	opts := []gotiktoklive.TikTokLiveOption{}
	if c.SignerUrl != "" {
		// Default: sign locally, no third-party service involved.
		opts = append(opts, gotiktoklive.SigningUrl(c.SignerUrl))
		logf("using local sign server %s", c.SignerUrl)
	} else {
		// Alternative connection mode: sign through the library's own
		// remote service. An API key is optional and only raises the
		// limit it is subject to.
		if c.APIKey != "" {
			opts = append(opts, gotiktoklive.SigningApiKey(c.APIKey))
		}
		logf("using alternative connection mode")
	}

	ctx, cancel := context.WithCancel(context.Background())
	setSession(nil, cancel)

	go runWithSigner(ctx, cancel, opts, username)
}

// runWithSigner builds the TikTok client, retrying while the signer is not
// ready yet.
//
// NewTikTok queries the signer for its rate limits before returning, so a
// signer that is still starting makes it fail with a connection error. The
// local sign server starts at the same moment as this sidecar but needs
// ~30-90s to bring up its browser, so that window is the normal case: giving
// up here would leave the widget silent until the user reconnected by hand.
// Retry with the usual backoff, then hand over to runSession.
func runWithSigner(ctx context.Context, cancel context.CancelFunc, opts []gotiktoklive.TikTokLiveOption, username string) {
	delay := reconnectBaseDelay
	for {
		tt, err := gotiktoklive.NewTikTok(opts...)
		if err == nil {
			tt.SetInfoHandler(func(a ...interface{}) { logf("info: %v", a) })
			tt.SetWarnHandler(func(a ...interface{}) { logf("warn: %v", a) })
			tt.SetErrorHandler(func(a ...interface{}) { logf("error: %v", a) })
			runSession(ctx, cancel, tt, username)
			return
		}
		logf("init failed: %v (retry in %s)", err, delay)
		emitState("connecting", "waiting for sign server")
		if !sleepBackoff(ctx, &delay) {
			return
		}
	}
}

// Reconnect policy. A dropped WebSocket (network blip, TikTok closing the
// socket, an expired cursor) used to end the session for good: the sidecar
// process stays alive, so the plugin's supervisor never restarted it and the
// widget sat silent until the user reconnected by hand. We now retry with
// exponential backoff.
//
// "Not live" is NOT a dead end. Opening OBS before going live, or a stream
// that simply ended, used to stop the session for good — and because the
// process stayed alive the plugin never restarted it, so only an OBS restart
// reconnected. The sidecar now waits and polls at a steady interval, so a
// stream that starts later in the SAME OBS session connects by itself.
const (
	reconnectBaseDelay = 2 * time.Second
	reconnectMaxDelay  = 30 * time.Second
	// A session that stayed up at least this long is considered healthy, so the
	// next drop restarts the backoff from the base instead of climbing further.
	reconnectHealthyAfter = 30 * time.Second
	// Steady poll while the streamer is not live yet (or a stream just ended),
	// so the next stream is picked up without restarting OBS.
	waitForLiveDelay = 30 * time.Second
)

// blockedBackoffSteps is the wait ladder for a REFUSED connection (TikTok
// answers 403 on the IM transport). It differs from an ordinary drop on
// purpose: most refusals are a blip — a dropped socket, one unlucky request —
// and recover within seconds, so the first steps stay SHORT. If they keep
// coming, TikTok is rate-limiting the address and only clears after a few
// QUIET minutes, which the ordinary 30s cap never allowed, so the ladder then
// escalates into minutes. Escalating only on repetition keeps a blip fast
// without letting a real limit persist. It is a var, not a const, because Go
// constants cannot hold a slice.
var blockedBackoffSteps = []time.Duration{
	2 * time.Second,  // blip: same speed as a normal reconnect
	10 * time.Second, // still refusing: ease off
	1 * time.Minute,  // looks like a real limit: needs quiet
	5 * time.Minute,
	10 * time.Minute,
}

// blockedGiveUpAfter is the number of attempts before giving up entirely.
var blockedGiveUpAfter = len(blockedBackoffSteps)

// runSession tracks the room and reconnects when the socket drops. It returns
// when the context is cancelled (a new connect, a disconnect, or quit) or when
// the stream has genuinely ended.
func runSession(ctx context.Context, cancel context.CancelFunc, tt *gotiktoklive.TikTok, username string) {
	delay := reconnectBaseDelay
	first := true
	// Refusals get their own ladder: see blockedBackoffSteps.
	blockedCount := 0

	for {
		if first {
			emitState("connecting", "")
			first = false
		}

		started := time.Now()
		l, err := tt.TrackUser(username)
		if err != nil {
			if terminalTrackError(err) {
				// The handle does not resolve: retrying would only repeat the
				// same failure, so stop and let the user fix the settings.
				emitState("off", err.Error())
				return
			}
			if notLiveError(err) {
				// Not live right now (no room yet, or the room just ended).
				// Poll steadily so a stream that starts later in the same OBS
				// session connects without restarting OBS.
				logf("not live: %v (waiting %s)", err, waitForLiveDelay)
				emitState("connecting", "waiting for stream")
				if !waitForLivePoll(ctx) {
					return
				}
				continue
			}
			if blockedError(err) {
				blockedCount++
				logf("blocked by TikTok (attempt %d/%d): %v", blockedCount, blockedGiveUpAfter, err)

				// Give up once the ladder is exhausted: a loop that cannot
				// succeed only hides the problem and re-arms the limit.
				if blockedCount > blockedGiveUpAfter {
					logf("blocked %d times in a row; giving up until the user reconnects", blockedCount-1)
					emitState("error", "TikTok rate limit. Reconnect in a few minutes.")
					return
				}

				// Short first steps so a blip recovers fast; minutes only once
				// the refusal proves to be a real limit. A little jitter, as
				// TikFinity does, keeps retries from landing on a fixed beat.
				wait := jittered(blockedBackoffSteps[blockedCount-1])
				emitState("error", "Refused by TikTok. Retrying in "+shortDuration(wait)+".")

				if !sleepFor(ctx, wait) {
					return
				}
				continue
			}

			// Anything else that is not "not live" is an ordinary drop.
			logf("track failed: %v (retry in %s)", err, delay)
			if !sleepBackoff(ctx, &delay) {
				return
			}
			emitState("connecting", "reconnecting")
			continue
		}

		setSession(l, cancel)
		emitState("connected", "")
		// A successful connection proves the limit lapsed: start the refusal
		// ladder over so a later block is treated as fresh.
		blockedCount = 0

		// RoomInfo carries the starting viewer count; ViewersEvent only fires
		// when the number changes, so a widget opened mid-stream would
		// otherwise show 0 until the next join/leave.
		if l.Info != nil && l.Info.UserCount > 0 {
			emit(outMsg{Ev: "tiktok", Event: "roomUser", Data: map[string]interface{}{
				"viewerCount": l.Info.UserCount,
			}})
		}
		// The room owner carries the streamer's real avatar. The plugin shows
		// it in the dashboard status pill, so report it once per session.
		if url := ownerAvatarURL(l); url != "" {
			emit(outMsg{Ev: "tiktok", Event: "roomOwner", Data: map[string]interface{}{
				"avatar": url,
			}})
		}

		// pumpEvents returns true when the context ended (stop for good), false
		// when the socket closed (reconnect).
		if pumpEvents(ctx, l) {
			return
		}

		// The socket dropped. Reset the backoff if the session ran healthily,
		// so a long-lived session that blips once reconnects quickly.
		if time.Since(started) >= reconnectHealthyAfter {
			delay = reconnectBaseDelay
		}
		logf("connection lost after %s, reconnecting in %s", time.Since(started).Round(time.Second), delay)
		if !sleepBackoff(ctx, &delay) {
			return
		}
		emitState("connecting", "reconnecting")
	}
}

// ownerAvatarURL returns the streamer's avatar image URL, or "" when the
// room info does not carry one.
//
// avatar_medium is preferred over avatar_thumb: both are small, but the
// medium one survives being shown at 28px without looking soft.
func ownerAvatarURL(l *gotiktoklive.Live) string {
	if l == nil || l.Info == nil || l.Info.Owner == nil {
		return ""
	}
	for _, list := range [][]string{
		l.Info.Owner.AvatarMedium.URLList,
		l.Info.Owner.AvatarLarge.URLList,
		l.Info.Owner.AvatarThumb.URLList,
	} {
		for _, u := range list {
			if u != "" {
				return u
			}
		}
	}
	return ""
}

// pumpEvents forwards events until the socket closes or the context is
// cancelled. It reports whether the caller should stop entirely.
func pumpEvents(ctx context.Context, l *gotiktoklive.Live) (ctxDone bool) {
	for {
		select {
		case <-ctx.Done():
			return true
		case ev, ok := <-l.Events:
			if !ok {
				return false
			}
			handleEvent(ev)
		}
	}
}

// sleepBackoff waits for the current delay and then advances it via
// nextBackoff. Returns false when the context was cancelled during the wait.
func sleepBackoff(ctx context.Context, delay *time.Duration) bool {
	t := time.NewTimer(*delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
	}
	*delay = nextBackoff(*delay)
	return true
}

// nextBackoff doubles the current delay and clamps it to the cap. Split out as a
// pure function so the growth policy is testable without real sleeping.
func nextBackoff(cur time.Duration) time.Duration {
	next := cur * 2
	if next > reconnectMaxDelay {
		next = reconnectMaxDelay
	}
	return next
}

// sleepFor waits for exactly `d`. Unlike sleepBackoff it does not advance a
// counter: the refusal ladder is driven by the caller, which also needs to stop
// after a fixed number of attempts. Returns false when the context ended.
func sleepFor(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// shortDuration renders a wait as "5s" / "2m" for the status line. Duration's
// own String() gives "2m0s", which reads badly in a one-line status pill.
func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + "s"
	}
	return strconv.Itoa(int(d.Round(time.Minute)/time.Minute)) + "m"
}

// jittered spreads a wait across [d/2, d]. TikFinity adds jitter to its
// reconnect delay for the same reason: without it every retry lands on an
// exact beat, and a limit enforced on a sliding window can be hit again by a
// request that arrives precisely when the window resets. Half the wait is kept
// fixed so a short step stays short (jitter must not stretch a 2s blip retry
// into something the user notices).
func jittered(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

// terminalTrackError reports whether a TrackUser failure means "stop for good":
// the handle does not resolve, so retrying would only repeat the same failure.
// "Not live yet" and "the stream ended" are NOT terminal (see notLiveError):
// the streamer may go live later in the same OBS session, and giving up there
// is what forced an OBS restart to reconnect.
func terminalTrackError(err error) bool {
	return errors.Is(err, gotiktoklive.ErrUserNotFound) ||
		errors.Is(err, gotiktoklive.ErrUserInfoNotFound)
}

// notLiveError reports whether the failure only means the streamer is not live
// right now: no room yet, or the room has ended. The sidecar keeps polling for
// the next stream instead of giving up, so opening OBS before going live (or
// after a stream ends) still connects by itself.
func notLiveError(err error) bool {
	return errors.Is(err, gotiktoklive.ErrUserOffline) ||
		errors.Is(err, gotiktoklive.ErrLiveHasEnded)
}

// blockedError reports whether TikTok (or the signer) refused the request
// instead of merely failing. Retrying forever is useless here: the answer
// will not change until the signing stack is updated, so the widget must
// say so instead of sitting on "reconnecting".
//
// gotiktoklive returns ErrIPBlockedOrBanned for an HTTP 403, which is what
// TikTok answers for /webcast/im/fetch when the X-Gnarly signature is stale
// (the other webcast endpoints keep working, so this is not an IP ban).
func blockedError(err error) bool {
	var blocked *gotiktoklive.ErrIPBlockedOrBanned
	if errors.As(err, &blocked) {
		return true
	}
	// Jaring pengaman: adapter lama memetakan 403 menjadi 502, dan sebagian
	// jalur membungkusnya tanpa tipe. Cocokkan pada teksnya juga.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status code 403") ||
		strings.Contains(msg, "status code 502") ||
		strings.Contains(msg, "ip or country might be blocked")
}

// waitForLivePoll sleeps one steady interval, returning false when the context
// was cancelled (a new connect, a disconnect, or quit).
func waitForLivePoll(ctx context.Context) bool {
	t := time.NewTimer(waitForLiveDelay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ------------------------------------------------------------------- events

// userMap flattens gotiktoklive.User into the shape the widgets already read
// from TikFinity/IndoFinity, so no widget-side mapping is needed.
//
// userBadges mirrors TikFinity's entry: {badgeSceneType, image, name, color}.
// The vendored gotiktoklive is patched to expose the image URL and background
// colour (upstream stored only a protobuf debug dump and no image at all).
func userMap(u *gotiktoklive.User) map[string]interface{} {
	if u == nil {
		return map[string]interface{}{}
	}
	avatar := ""
	if u.ProfilePicture != nil && len(u.ProfilePicture.Urls) > 0 {
		avatar = u.ProfilePicture.Urls[len(u.ProfilePicture.Urls)-1]
	}
	badges := []interface{}{}
	if u.Badge != nil {
		for _, b := range u.Badge.Badges {
			badges = append(badges, map[string]interface{}{
				"badgeSceneType": b.SceneType,
				"image":          b.Image,
				"name":           b.Name,
				"color":          b.Color,
			})
		}
	}
	out := map[string]interface{}{
		"userId":            strconv.FormatInt(u.ID, 10),
		"uniqueId":          u.Username,
		"nickname":          u.Nickname,
		"profilePictureUrl": avatar,
		"userBadges":        badges,
	}
	// Follow role (0 none, 1 follower, 2 friend). The widget's filter reads
	// followRole >= 1 for the "follower" permission, so forward it too.
	if u.ExtraAttributes != nil {
		out["followRole"] = u.ExtraAttributes.FollowRole
	}
	// Fan-club membership, shaped like TikFinity's fansClubInfo so the widget's
	// "User Permissions" filter reads it with no widget-side change.
	if u.FansClubLevel > 0 || u.FansClubName != "" || u.FanClubBadge != "" {
		out["fansClubInfo"] = map[string]interface{}{
			"clubName":  u.FansClubName,
			"fansLevel": u.FansClubLevel,
			"isActive":  u.FanClubActive,
		}
		// Dormant ("grey badge") members keep their tier but are no longer
		// members, so the widget's fan-club filter has to see the state.
		out["fanClubActive"] = u.FanClubActive
	}
	// The fan-club badge artwork. TikTok's proto has no badgeSceneType, so this
	// URL is what tells the widget's fan-club filter apart from a grade badge.
	if u.FanClubBadge != "" {
		out["fanClubBadge"] = u.FanClubBadge
	}
	return out
}

// withUser merges the user fields into a per-event map without letting a nil
// user overwrite the base keys with empty strings.
func withUser(u *gotiktoklive.User, extra map[string]interface{}) map[string]interface{} {
	out := userMap(u)
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// withIdentity merges the event's UserIdentity flags into a data map. TikTok
// carries follower/subscriber/moderator per-EVENT (not on User), and upstream
// dropped them entirely, so a widget could not filter by role. The widget's
// "User Permissions" filter reads isFollower / isSubscriber / isModerator.
func withIdentity(m map[string]interface{}, id *gotiktoklive.UserIdentity) map[string]interface{} {
	if id == nil {
		return m
	}
	m["isFollower"] = id.IsFollower
	m["isSubscriber"] = id.IsSubscriber
	m["isModerator"] = id.IsModerator
	return m
}

// emoteList flattens gotiktoklive emotes into the shape the widget renders:
// {emoteId, emoteImageUrl, placeInComment}. placeInComment is the 0-based index
// of the placeholder character the emote replaces in the comment text (the same
// contract TikTok Live Connector exposes). Always returns a (possibly empty)
// slice so the JSON has "emotes": [] rather than null.
func emoteList(emotes []gotiktoklive.Emote) []interface{} {
	out := []interface{}{}
	for _, e := range emotes {
		out = append(out, map[string]interface{}{
			"emoteId":          e.EmoteID,
			"emoteImageUrl":    e.ImageURL,
			"placeInComment":   e.PlaceInComment,
			"emoteType":        e.EmoteType,
			"emotePrivateType": e.PrivateType,
		})
	}
	return out
}

// standaloneEmoteAsChat re-shapes a standalone subscriber emote into a synthetic
// `chat` frame so the widget's existing chat renderer draws it with no
// widget-side change. TikTok sends a subscriber emote as its own message with no
// comment text and no per-emote index, so we synthesise a comment of one
// placeholder character per emote and assign each emote the 0-based index of its
// placeholder — the same contract a real comment's emotes follow. The
// placeholder is a zero-width space: invisible should a consumer ever show the
// raw comment, and swapped for the artwork by the chat renderer.
func standaloneEmoteAsChat(emotes []gotiktoklive.Emote) (string, []interface{}) {
	fixed := make([]gotiktoklive.Emote, len(emotes))
	copy(fixed, emotes)
	var b strings.Builder
	for i := range fixed {
		fixed[i].PlaceInComment = i
		b.WriteRune('\u200b')
	}
	return b.String(), emoteList(fixed)
}

// normalizeComment makes sure every emote's 0-based placeInComment points at a
// real character in the comment. TikTok usually ships a placeholder char in the
// comment text for each inline emote, but a subscriber emote sent through the
// chat path arrives with an EMPTY comment while its emotes still carry indexes
// 0,1,2,… The widget drops any emote whose index is outside the text
// (`at >= text.length`), so without this every such emote vanished. We pad the
// comment with zero-width spaces (invisible if ever shown raw) up to the last
// index, leaving comments that already carry their own placeholders untouched.
func normalizeComment(comment string, emotes []gotiktoklive.Emote) string {
	need := 0
	for _, e := range emotes {
		if e.PlaceInComment+1 > need {
			need = e.PlaceInComment + 1
		}
	}
	runes := []rune(comment)
	if len(runes) >= need {
		return comment
	}
	var b strings.Builder
	b.WriteString(comment)
	for i := len(runes); i < need; i++ {
		b.WriteRune('\u200b')
	}
	return b.String()
}

// forwardable reports whether an event from the library should reach a widget.
// TikTok replays recent chat when a socket (re)connects, and the vendored
// library tags those frames IsHistory within a 15-minute window. The widgets
// keep no dedupe, so forwarding a replay re-adds the same questions to the
// queue on every reconnect — the "queue grows on its own" bug. Drop them here.
func forwardable(ev gotiktoklive.Event) bool {
	return !ev.IsHistory()
}

func handleEvent(ev gotiktoklive.Event) {
	if !forwardable(ev) {
		return
	}
	switch e := ev.(type) {

	case gotiktoklive.ChatEvent:
		emit(outMsg{Ev: "tiktok", Event: "chat", Data: withIdentity(withUser(e.User, map[string]interface{}{
			"comment": normalizeComment(e.Comment, e.Emotes),
			"emotes":  emoteList(e.Emotes),
		}), e.UserIdentity)})

	case gotiktoklive.EmoteEvent:
		// A subscriber emote (sticker). TikTok sends it as its own message with
		// no comment text, but the widget only renders emotes that sit inside a
		// comment (its `chat` handler). So re-shape it as a synthetic `chat`
		// frame: one invisible placeholder character per emote at its 0-based
		// placeInComment, exactly like a real comment that carries emotes. That
		// way the emote renders with no widget-side change.
		comment, emotes := standaloneEmoteAsChat(e.Emotes)
		emit(outMsg{Ev: "tiktok", Event: "chat", Data: withIdentity(withUser(e.User, map[string]interface{}{
			"comment": comment,
			"emotes":  emotes,
		}), e.UserIdentity)})

	case gotiktoklive.GiftEvent:
		// Streakable gifts (Type == 1) arrive many times; the widget already
		// filters on repeatEnd, and it needs repeatCount to show the total, so
		// every frame is forwarded rather than collapsing them here.
		emit(outMsg{Ev: "tiktok", Event: "gift", Data: withIdentity(withUser(e.User, map[string]interface{}{
			"giftId":         e.ID,
			"giftName":       e.Name,
			"giftPictureUrl": e.PictureURL,
			"repeatCount":    e.RepeatCount,
			"repeatEnd":      e.RepeatEnd,
			"giftType":       e.Type,
			"giftCost":       e.Diamonds,
		}), e.UserIdentity)})

	case gotiktoklive.UserEvent:
		// The event kind is an unexported type, so compare against the exported
		// constants rather than a string.
		switch e.Event {
		case gotiktoklive.USER_FOLLOW:
			emit(outMsg{Ev: "tiktok", Event: "follow", Data: userMap(e.User)})
		case gotiktoklive.USER_SHARE:
			emit(outMsg{Ev: "tiktok", Event: "share", Data: userMap(e.User)})
		case gotiktoklive.USER_JOIN:
			emit(outMsg{Ev: "tiktok", Event: "join", Data: userMap(e.User)})
		case gotiktoklive.USER_SUBSCRIBE:
			// Subscriptions were documented in protocol.md §2.3 but never
			// emitted: the vendored parser had no case for
			// WebcastSubNotifyMessage, so the event was dropped.
			emit(outMsg{Ev: "tiktok", Event: "subscribe", Data: userMap(e.User)})
		}

	case gotiktoklive.SuperFanEvent:
		// Three Super Fan kinds share one event type; the widget switches on
		// the wire name. Data carries the common user fields, plus the box
		// value when the source was a Super Fan Box envelope.
		extra := map[string]interface{}{}
		if e.Event == gotiktoklive.SUPER_FAN_BOX {
			extra["diamondCount"] = e.DiamondCount
		}
		emit(outMsg{Ev: "tiktok", Event: string(e.Event), Data: withUser(e.User, extra)})

	case gotiktoklive.ViewersEvent:
		emit(outMsg{Ev: "tiktok", Event: "roomUser", Data: map[string]interface{}{
			"viewerCount": e.Viewers,
		}})

	case gotiktoklive.LikeEvent:
		emit(outMsg{Ev: "tiktok", Event: "like", Data: withUser(e.User, map[string]interface{}{
			"likeCount":  e.Likes,
			"totalLikes": e.TotalLikes,
		})})

	case gotiktoklive.RoomEvent:
		// Room events are TikTok system notices (moderation, room state). They
		// are forwarded as a distinct event so widgets can opt in later.
		emit(outMsg{Ev: "tiktok", Event: "roomEvent", Data: map[string]interface{}{
			"roomEventType": e.Type,
			"message":       e.Message,
		}})
	}
}

// --------------------------------------------------------------------- main

func main() {
	logf("sidecar v%s starting", version)
	emit(outMsg{Ev: "ready"})

	sc := bufio.NewScanner(os.Stdin)
	// Event payloads are small, but a generous cap avoids a silent truncation
	// if a future field grows.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		var head struct {
			Cmd string `json:"cmd"`
		}
		if err := json.Unmarshal(line, &head); err != nil {
			logf("bad command: %v", err)
			continue
		}

		switch head.Cmd {
		case "connect":
			var c connectCmd
			if err := json.Unmarshal(line, &c); err != nil {
				emitState("error", "bad connect payload")
				continue
			}
			go handleConnect(c)

		case "disconnect":
			stopSession()
			emitState("off", "disconnected")

		case "quit":
			stopSession()
			return

		default:
			logf("unknown command: %s", head.Cmd)
		}
	}

	// stdin closed => the plugin is gone; do not linger as an orphan process.
	stopSession()
}
