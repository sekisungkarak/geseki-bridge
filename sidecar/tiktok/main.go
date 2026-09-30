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
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	gotiktoklive "github.com/steampoweredtaco/gotiktoklive"
)

const version = "0.1.0"

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
	APIKey   string `json:"apiKey"`
}

func handleConnect(c connectCmd) {
	stopSession()

	username := c.Username
	if username == "" {
		emitState("error", "missing username")
		return
	}

	opts := []gotiktoklive.TikTokLiveOption{}
	if c.APIKey != "" {
		opts = append(opts, gotiktoklive.SigningApiKey(c.APIKey))
	}

	tt, err := gotiktoklive.NewTikTok(opts...)
	if err != nil {
		emitState("error", "init: "+err.Error())
		return
	}
	tt.SetInfoHandler(func(a ...interface{}) { logf("info: %v", a) })
	tt.SetWarnHandler(func(a ...interface{}) { logf("warn: %v", a) })
	tt.SetErrorHandler(func(a ...interface{}) { logf("error: %v", a) })

	ctx, cancel := context.WithCancel(context.Background())
	setSession(nil, cancel)

	go runSession(ctx, cancel, tt, username)
}

// Reconnect policy. A dropped WebSocket (network blip, TikTok closing the
// socket, an expired cursor) used to end the session for good: the sidecar
// process stays alive, so the plugin's supervisor never restarted it and the
// widget sat silent until the user reconnected by hand. We now retry with
// exponential backoff and give up only when TikTok says the room is gone.
const (
	reconnectBaseDelay = 2 * time.Second
	reconnectMaxDelay  = 30 * time.Second
	// A session that stayed up at least this long is considered healthy, so the
	// next drop restarts the backoff from the base instead of climbing further.
	reconnectHealthyAfter = 30 * time.Second
)

// runSession tracks the room and reconnects when the socket drops. It returns
// when the context is cancelled (a new connect, a disconnect, or quit) or when
// the stream has genuinely ended.
func runSession(ctx context.Context, cancel context.CancelFunc, tt *gotiktoklive.TikTok, username string) {
	delay := reconnectBaseDelay
	first := true

	for {
		if first {
			emitState("connecting", "")
			first = false
		}

		started := time.Now()
		l, err := tt.TrackUser(username)
		if err != nil {
			if terminalTrackError(err) {
				// The room is gone (stream ended) or the handle is wrong:
				// there is nothing useful to retry.
				emitState("off", err.Error())
				return
			}
			logf("track failed: %v (retry in %s)", err, delay)
			if !sleepBackoff(ctx, &delay) {
				return
			}
			emitState("connecting", "reconnecting")
			continue
		}

		setSession(l, cancel)
		emitState("connected", "")

		// RoomInfo carries the starting viewer count; ViewersEvent only fires
		// when the number changes, so a widget opened mid-stream would
		// otherwise show 0 until the next join/leave.
		if l.Info != nil && l.Info.UserCount > 0 {
			emit(outMsg{Ev: "tiktok", Event: "roomUser", Data: map[string]interface{}{
				"viewerCount": l.Info.UserCount,
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

// terminalTrackError reports whether a TrackUser failure means "do not retry":
// the stream ended, or the username does not resolve. Every other error
// (network, signer, rate limit) is treated as transient.
func terminalTrackError(err error) bool {
	return errors.Is(err, gotiktoklive.ErrUserOffline) ||
		errors.Is(err, gotiktoklive.ErrUserNotFound) ||
		errors.Is(err, gotiktoklive.ErrUserInfoNotFound)
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

func handleEvent(ev gotiktoklive.Event) {
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
