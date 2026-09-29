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
	"fmt"
	"os"
	"strconv"
	"sync"

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

	emitState("connecting", "")

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

	go func() {
		l, err := tt.TrackUser(username)
		if err != nil {
			emitState("error", "track: "+err.Error())
			cancel()
			return
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

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-l.Events:
				if !ok {
					emitState("off", "stream ended")
					return
				}
				handleEvent(ev)
			}
		}
	}()
}

// ------------------------------------------------------------------- events

// userMap flattens gotiktoklive.User into the shape the widgets already read
// from TikFinity/IndoFinity, so no widget-side mapping is needed.
//
// userBadges is intentionally empty: gotiktoklive exposes badge *names* but not
// their image URLs, and the widget only renders a badge it can load. Sending a
// nameless entry would render an empty slot. Tracked as a known gap in README.
func userMap(u *gotiktoklive.User) map[string]interface{} {
	if u == nil {
		return map[string]interface{}{}
	}
	avatar := ""
	if u.ProfilePicture != nil && len(u.ProfilePicture.Urls) > 0 {
		avatar = u.ProfilePicture.Urls[len(u.ProfilePicture.Urls)-1]
	}
	return map[string]interface{}{
		"userId":            strconv.FormatInt(u.ID, 10),
		"uniqueId":          u.Username,
		"nickname":          u.Nickname,
		"profilePictureUrl": avatar,
		"userBadges":        []interface{}{},
	}
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

func handleEvent(ev gotiktoklive.Event) {
	switch e := ev.(type) {

	case gotiktoklive.ChatEvent:
		emit(outMsg{Ev: "tiktok", Event: "chat", Data: withUser(e.User, map[string]interface{}{
			"comment": e.Comment,
		})})

	case gotiktoklive.GiftEvent:
		// Streakable gifts (Type == 1) arrive many times; the widget already
		// filters on repeatEnd, and it needs repeatCount to show the total, so
		// every frame is forwarded rather than collapsing them here.
		emit(outMsg{Ev: "tiktok", Event: "gift", Data: withUser(e.User, map[string]interface{}{
			"giftName":       e.Name,
			"giftPictureUrl": "", // not exposed by gotiktoklive; widget falls back to its own icon
			"repeatCount":    e.RepeatCount,
			"repeatEnd":      e.RepeatEnd,
			"giftType":       e.Type,
			"giftCost":       e.Diamonds,
		})})

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
