package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	gotiktoklive "github.com/steampoweredtaco/gotiktoklive"
)

// captureFrames runs fn with os.Stdout redirected and returns the parsed JSON
// frames it emitted, one per line.
func captureFrames(t *testing.T, fn func()) []map[string]interface{} {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = old
	raw := <-done

	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("frame bukan JSON valid: %v (%s)", err, line)
		}
		out = append(out, m)
	}
	return out
}

func testUser() *gotiktoklive.User {
	return &gotiktoklive.User{
		ID:         42,
		Username:   "viewer",
		Nickname:   "Viewer",
		SecUid:     "sec-42",
		CreateTime: 1600000000,
		ProfilePicture: &gotiktoklive.ProfilePicture{
			Urls: []string{"https://cdn.example/avatar.webp"},
		},
		ExtraAttributes: &gotiktoklive.ExtraAttributes{FollowRole: 1},
	}
}

// TestChatFrameHasTLCMeta guards a chat frame carries msgId/createTime plus the
// user identity fields, all in the shapes TLC uses.
func TestChatFrameHasTLCMeta(t *testing.T) {
	frames := captureFrames(t, func() {
		handleEvent(gotiktoklive.ChatEvent{
			MessageID:    7137750790064065286,
			Timestamp:    1661887134718,
			Comment:      "hello",
			User:         testUser(),
			UserIdentity: &gotiktoklive.UserIdentity{IsFollower: true},
		})
	})
	if len(frames) != 1 {
		t.Fatalf("mau 1 frame, dapat %d", len(frames))
	}
	f := frames[0]
	// The sidecar emits `ev`; the plugin re-wraps it as `type` on the wire.
	if f["ev"] != "tiktok" || f["event"] != "chat" {
		t.Fatalf("frame salah: %v", f)
	}
	data := f["data"].(map[string]interface{})
	if data["msgId"] != "7137750790064065286" {
		t.Errorf("msgId = %v (%T), mau string", data["msgId"], data["msgId"])
	}
	if data["createTime"] != "1661887134718" {
		t.Errorf("createTime = %v, mau string", data["createTime"])
	}
	if data["secUid"] != "sec-42" {
		t.Errorf("secUid = %v", data["secUid"])
	}
	if data["isFollower"] != true {
		t.Errorf("isFollower = %v, mau true", data["isFollower"])
	}
	if _, ok := data["userDetails"]; !ok {
		t.Errorf("userDetails hilang: %v", data)
	}
}

// TestMemberEventName guards a room entry is emitted as `member` (TLC's name).
func TestMemberEventName(t *testing.T) {
	frames := captureFrames(t, func() {
		handleEvent(gotiktoklive.UserEvent{
			MessageID: 1,
			Timestamp: 2,
			Event:     gotiktoklive.USER_JOIN,
			User:      testUser(),
			ActionID:  1,
		})
	})
	if len(frames) != 1 || frames[0]["event"] != "member" {
		t.Fatalf("mau satu event member, dapat %v", frames)
	}
	data := frames[0]["data"].(map[string]interface{})
	if data["actionId"] != float64(1) {
		t.Errorf("actionId = %v, mau 1", data["actionId"])
	}
}

// TestSocialEmitsFollowAndShare guards a follow/share is emitted both as
// `social` (TLC) and under its own name (the widgets).
func TestSocialEmitsFollowAndShare(t *testing.T) {
	for _, tc := range []struct {
		kind gotiktoklive.Event
		name string
	}{
		{gotiktoklive.UserEvent{Event: gotiktoklive.USER_FOLLOW}, "follow"},
		{gotiktoklive.UserEvent{Event: gotiktoklive.USER_SHARE}, "share"},
	} {
		ue := tc.kind.(gotiktoklive.UserEvent)
		ue.User = testUser()
		ue.MessageID = 5
		ue.Timestamp = 6
		ue.DisplayType = "pm_main_follow_message_viewer_2"
		frames := captureFrames(t, func() { handleEvent(ue) })
		var names []string
		for _, f := range frames {
			names = append(names, f["event"].(string))
		}
		if len(frames) != 2 {
			t.Fatalf("%s: mau 2 frame, dapat %v", tc.name, names)
		}
		hasSocial, hasNamed := false, false
		for _, n := range names {
			if n == "social" {
				hasSocial = true
			}
			if n == tc.name {
				hasNamed = true
			}
		}
		if !hasSocial || !hasNamed {
			t.Errorf("%s: mau social + %s, dapat %v", tc.name, tc.name, names)
		}
	}
}

// TestGiftFrameHasGiftSubObject guards the gift frame carries TLC's `gift`
// compatibility object and groupId, alongside the flat fields.
func TestGiftFrameHasGiftSubObject(t *testing.T) {
	frames := captureFrames(t, func() {
		handleEvent(gotiktoklive.GiftEvent{
			MessageID:   9,
			Timestamp:   10,
			ID:          5953,
			Name:        "Rose",
			RepeatCount: 3,
			RepeatEnd:   true,
			Type:        1,
			GroupID:     1661887131074,
			User:        testUser(),
		})
	})
	if len(frames) != 1 {
		t.Fatalf("mau 1 frame, dapat %d", len(frames))
	}
	data := frames[0]["data"].(map[string]interface{})
	gift, ok := data["gift"].(map[string]interface{})
	if !ok {
		t.Fatalf("gift sub-objek hilang: %v", data)
	}
	if gift["gift_id"] != float64(5953) {
		t.Errorf("gift.gift_id = %v", gift["gift_id"])
	}
	if gift["repeat_end"] != float64(1) {
		t.Errorf("gift.repeat_end = %v, mau 1", gift["repeat_end"])
	}
	if data["groupId"] != "1661887131074" {
		t.Errorf("groupId = %v (%T), mau string", data["groupId"], data["groupId"])
	}
	// Field lama harus tetap ada.
	if data["giftName"] != "Rose" || data["repeatCount"] != float64(3) {
		t.Errorf("field gift lama hilang: %v", data)
	}
}

// TestLikeFrameHasTotalLikeCount guards both like counters are present.
func TestLikeFrameHasTotalLikeCount(t *testing.T) {
	frames := captureFrames(t, func() {
		handleEvent(gotiktoklive.LikeEvent{
			MessageID: 1, Timestamp: 2, Likes: 6, TotalLikes: 21349, User: testUser(),
		})
	})
	data := frames[0]["data"].(map[string]interface{})
	if data["totalLikeCount"] != float64(21349) || data["totalLikes"] != float64(21349) {
		t.Errorf("like counters salah: %v", data)
	}
}

// TestRoomUserHasTopViewers guards the roomUser frame carries the topViewers
// list in TLC's shape.
func TestRoomUserHasTopViewers(t *testing.T) {
	frames := captureFrames(t, func() {
		handleEvent(gotiktoklive.ViewersEvent{
			MessageID: 1, Timestamp: 2, Viewers: 630,
			TopViewers: []gotiktoklive.TopViewer{
				{User: testUser(), CoinCount: 1234},
			},
		})
	})
	data := frames[0]["data"].(map[string]interface{})
	if data["viewerCount"] != float64(630) {
		t.Errorf("viewerCount = %v", data["viewerCount"])
	}
	tv, ok := data["topViewers"].([]interface{})
	if !ok || len(tv) != 1 {
		t.Fatalf("topViewers salah: %v", data["topViewers"])
	}
	entry := tv[0].(map[string]interface{})
	if entry["coinCount"] != float64(1234) {
		t.Errorf("topViewers[0].coinCount = %v", entry["coinCount"])
	}
	if _, ok := entry["user"].(map[string]interface{}); !ok {
		t.Errorf("topViewers[0].user bukan objek: %v", entry["user"])
	}
}
