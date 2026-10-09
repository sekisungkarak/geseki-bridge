package main

import (
	"encoding/json"
	"testing"

	gotiktoklive "github.com/steampoweredtaco/gotiktoklive"
)

// TestUserMapCarriesTLCFields guards the "payload too small" fix: the data map
// must carry the identity/profile fields TikTok Live Connector (v2.5.0)
// exposes, not just name and avatar.
func TestUserMapCarriesTLCFields(t *testing.T) {
	u := &gotiktoklive.User{
		ID:             6813181309701719620,
		Username:       "zerodytester",
		Nickname:       "Zerody Tester",
		SecUid:         "MS4wLjABAAAA-sec",
		CreateTime:     1588502711,
		BioDescription: "hello",
		ProfilePictureUrls: []string{
			"https://cdn.example/a.webp",
			"https://cdn.example/b.jpeg",
		},
		ProfilePicture: &gotiktoklive.ProfilePicture{Urls: []string{"https://cdn.example/best.webp"}},
		FollowInfo: &gotiktoklive.FollowInfo{
			FollowingCount: 277,
			FollowerCount:  96,
			FollowStatus:   1,
			PushStatus:     0,
		},
		GifterLevel: 12,
		ExtraAttributes: &gotiktoklive.ExtraAttributes{
			FollowRole: 1,
		},
		Badge: &gotiktoklive.BadgeAttributes{
			Badges: []*gotiktoklive.UserBadge{
				{Type: "image", SceneType: 1, Name: "Moderator"},
			},
		},
	}

	got := jsonMap(t, userMap(u))

	for _, k := range []string{"secUid", "userDetails", "followInfo", "userSceneTypes",
		"isModerator", "isSubscriber", "isNewGifter", "topGifterRank", "gifterLevel", "teamMemberLevel"} {
		if _, ok := got[k]; !ok {
			t.Errorf("kunci %q hilang dari payload: %v", k, got)
		}
	}
	if got["secUid"] != "MS4wLjABAAAA-sec" {
		t.Errorf("secUid = %v", got["secUid"])
	}
	if got["gifterLevel"] != float64(12) {
		t.Errorf("gifterLevel = %v, mau 12", got["gifterLevel"])
	}
	// Flags diturunkan dari badge, sama seperti TLC.
	if got["isModerator"] != true {
		t.Errorf("isModerator = %v, mau true (badge scene 1)", got["isModerator"])
	}
	// userDetails membawa createTime sebagai string, sama seperti TLC.
	details, ok := got["userDetails"].(map[string]interface{})
	if !ok {
		t.Fatalf("userDetails bukan objek: %v", got["userDetails"])
	}
	if details["createTime"] != "1588502711" {
		t.Errorf("userDetails.createTime = %v, mau \"1588502711\"", details["createTime"])
	}
	if details["bioDescription"] != "hello" {
		t.Errorf("userDetails.bioDescription = %v", details["bioDescription"])
	}
	fi, ok := got["followInfo"].(map[string]interface{})
	if !ok {
		t.Fatalf("followInfo bukan objek: %v", got["followInfo"])
	}
	if fi["followerCount"] != float64(96) {
		t.Errorf("followInfo.followerCount = %v, mau 96", fi["followerCount"])
	}
}

// TestWithIdentityNeverDowngrades guards that a role derived from a badge (set
// true in userMap) is not overwritten with false by the per-event identity
// block, which omits flags it does not know.
func TestWithIdentityNeverDowngrades(t *testing.T) {
	base := map[string]interface{}{"isModerator": true}
	got := withIdentity(base, &gotiktoklive.UserIdentity{IsModerator: false})
	if got["isModerator"] != true {
		t.Errorf("isModerator diturunkan jadi %v, mau tetap true", got["isModerator"])
	}
	// Flag yang belum ada tetap diisi, termasuk false.
	if got["isFollower"] != false {
		t.Errorf("isFollower = %v, mau false", got["isFollower"])
	}
	// Identity nil tidak boleh mengubah apa pun.
	got2 := withIdentity(map[string]interface{}{"isModerator": true}, nil)
	if got2["isModerator"] != true {
		t.Errorf("identity nil mengubah map: %v", got2)
	}
}

// TestWithEventMetaStringifies guards msgId/createTime are strings, the way
// TikTok Live Connector serialises protobuf Long values.
func TestWithEventMetaStringifies(t *testing.T) {
	got := withEventMeta(map[string]interface{}{}, 7137750790064065286, 1661887134718)
	if got["msgId"] != "7137750790064065286" {
		t.Errorf("msgId = %v (%T), mau string", got["msgId"], got["msgId"])
	}
	if got["createTime"] != "1661887134718" {
		t.Errorf("createTime = %v (%T), mau string", got["createTime"], got["createTime"])
	}
}

func jsonMap(t *testing.T, v interface{}) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return got
}
