package main

import (
	"encoding/json"
	"testing"

	gotiktoklive "github.com/steampoweredtaco/gotiktoklive"
)

// TestUserMapCarriesRoles guards the "User Permissions" filter on the bridge
// path. The widget reads isFollower / isSubscriber / isModerator /
// followRole / fanClubBadge; before this patch userMap() sent none of them, so
// selecting a permission silently blocked every first chatter.
func TestUserMapCarriesRoles(t *testing.T) {
	u := &gotiktoklive.User{
		ID:            42,
		Username:      "viewer",
		Nickname:      "Viewer",
		FansClubName:  "Sekisungkarak",
		FansClubLevel: 12,
		FanClubBadge:  "https://p16-webcast.tiktokcdn.com/webcast-va/webcast-va-fans_badge_icon_lv10_v2.png~tplv-obj.image",
		ExtraAttributes: &gotiktoklive.ExtraAttributes{
			FollowRole: 2,
		},
	}

	data := withIdentity(userMap(u), &gotiktoklive.UserIdentity{
		IsFollower:   true,
		IsSubscriber: true,
		IsModerator:  false,
	})

	b, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got["isFollower"] != true {
		t.Errorf("isFollower = %v, mau true", got["isFollower"])
	}
	if got["isSubscriber"] != true {
		t.Errorf("isSubscriber = %v, mau true", got["isSubscriber"])
	}
	if got["isModerator"] != false {
		t.Errorf("isModerator = %v, mau false", got["isModerator"])
	}
	if got["followRole"] != float64(2) {
		t.Errorf("followRole = %v, mau 2", got["followRole"])
	}
	// fanClubBadge adalah sinyal badge fan club yang dibaca filter widget.
	if got["fanClubBadge"] != u.FanClubBadge {
		t.Errorf("fanClubBadge = %v, mau %v", got["fanClubBadge"], u.FanClubBadge)
	}

	fci, ok := got["fansClubInfo"].(map[string]interface{})
	if !ok {
		t.Fatalf("fansClubInfo hilang; kunci: %v", got)
	}
	if fci["clubName"] != "Sekisungkarak" {
		t.Errorf("fansClubInfo.clubName = %v", fci["clubName"])
	}
	if fci["fansLevel"] != float64(12) {
		t.Errorf("fansClubInfo.fansLevel = %v", fci["fansLevel"])
	}

	// Bentuk badge harus tetap apa adanya (jangan sampai regresi).
	if _, ok := got["userBadges"]; !ok {
		t.Errorf("userBadges hilang")
	}
}

func TestUserMapPlainViewerHasNoFanClub(t *testing.T) {
	u := &gotiktoklive.User{ID: 7, Username: "plain", Nickname: "Plain"}
	got := userMap(u)
	if _, ok := got["fansClubInfo"]; ok {
		t.Errorf("viewer biasa tidak boleh punya fansClubInfo")
	}
	if _, ok := got["fanClubBadge"]; ok {
		t.Errorf("viewer biasa tidak boleh punya fanClubBadge")
	}
}
