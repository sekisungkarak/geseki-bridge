package gotiktoklive

import (
	"testing"

	pb "github.com/steampoweredtaco/gotiktoklive/proto"
)

// TestToUserFillsFanClubBadgeAndScene menutup dua celah yang bikin filter
// "Fan Club" gagal di jalur bridge: toUser() dulu tidak pernah mengisi medal
// fan club, dan UserBadge.SceneType tidak pernah diisi (selalu 0).
func TestToUserFillsFanClubBadgeAndScene(t *testing.T) {
	fansURL := "https://p16-webcast.tiktokcdn.com/webcast-va/webcast-va-fans_badge_icon_lv10_v2.png~tplv-obj.image"
	u := &pb.User{
		Id:       1,
		Nickname: "member",
		Medal:    &pb.Image{UrlList: []string{fansURL}},
		BadgeList: []*pb.BadgeStruct{{
			DisplayType: pb.BadgeStruct_BADGEDISPLAYTYPE_IMAGE,
			BadgeType: &pb.BadgeStruct_Image{Image: &pb.BadgeStruct_ImageBadge{
				Image: &pb.Image{UrlList: []string{fansURL}},
			}},
		}},
	}

	got := toUser(u)

	if got.FanClubBadge != fansURL {
		t.Errorf("FanClubBadge = %q, mau %q", got.FanClubBadge, fansURL)
	}
	if got.Badge == nil || len(got.Badge.Badges) != 1 {
		t.Fatalf("badge hilang: %+v", got.Badge)
	}
	if got.Badge.Badges[0].SceneType != 10 {
		t.Errorf("SceneType = %d, mau 10 (fan club)", got.Badge.Badges[0].SceneType)
	}
}

// TestToUserPlainViewerHasNoFanClubBadge memastikan viewer biasa tidak dapat
// badge fan club palsu.
func TestToUserPlainViewerHasNoFanClubBadge(t *testing.T) {
	got := toUser(&pb.User{Id: 2, Nickname: "plain"})
	if got.FanClubBadge != "" {
		t.Errorf("viewer biasa tidak boleh punya FanClubBadge: %q", got.FanClubBadge)
	}
}

// TestBadgeSceneFromURL menutup celah yang bikin fan club gagal di bridge:
// proto TikTok tidak punya field scene untuk badge, jadi badgeSceneType harus
// diturunkan dari artwork. Salah turunkan -> filter fan club salah orang.
func TestBadgeSceneFromURL(t *testing.T) {
	cases := []struct {
		url  string
		want int
	}{
		{"https://p16-webcast.tiktokcdn.com/webcast-va/webcast-va-fans_badge_icon_lv10_v2.png~tplv-obj.image", 10},
		{"https://p16-webcast.tiktokcdn.com/webcast-va/fans_badge_icon_lv50_v4.png~tplv-obj.image", 10},
		{"https://p16-webcast.tiktokcdn.com/webcast-va/grade_badge_icon_lite_lv18_v1.png~tplv-obj.image", 8},
		{"https://p16-webcast.tiktokcdn.com/webcast-va/moderater_badge_icon.png~tplv-obj.image", 1},
		{"https://p16-webcast.tiktokcdn.com/webcast-sg/new_top_gifter_version_2.png~tplv-obj.image", 0},
		{"", 0},
	}
	for _, c := range cases {
		if got := badgeSceneFromURL(c.url); got != c.want {
			t.Errorf("badgeSceneFromURL(%q) = %d, mau %d", c.url, got, c.want)
		}
	}
}

// TestToUserFanClubDormancy guards the "grey badge = not a fan club" rule on
// the bridge path. TikTok greys the badge when a member stops earning points
// for 7 consecutive days; the proto signals it two ways and either one must
// clear the flag. A payload that omits the status must stay ACTIVE, so a live
// member is never mistaken for a former one.
func TestToUserFanClubDormancy(t *testing.T) {
	inactive := &pb.User{FansClub: &pb.User_FansClub{Data: &pb.User_FansClub_FansClubData{
		ClubName: "Sekisungkarak", Level: 12,
		UserFansClubStatus: pb.User_FansClub_FansClubData_INACTIVE,
	}}}
	if toUser(inactive).FanClubActive {
		t.Errorf("userFansClubStatus=INACTIVE harus FanClubActive=false")
	}

	sleeping := &pb.User{FansClubInfo: &pb.User_FansClubInfo{IsSleeping: true, FansLevel: 5}}
	if toUser(sleeping).FanClubActive {
		t.Errorf("isSleeping=true harus FanClubActive=false")
	}

	active := &pb.User{FansClub: &pb.User_FansClub{Data: &pb.User_FansClub_FansClubData{
		ClubName: "Sekisungkarak", Level: 12,
		UserFansClubStatus: pb.User_FansClub_FansClubData_ACTIVE,
	}}}
	if !toUser(active).FanClubActive {
		t.Errorf("status ACTIVE harus FanClubActive=true")
	}

	// Status tidak dikirim sama sekali -> tetap dianggap aktif.
	unknown := &pb.User{FansClub: &pb.User_FansClub{Data: &pb.User_FansClub_FansClubData{
		ClubName: "Sekisungkarak", Level: 3,
	}}}
	if !toUser(unknown).FanClubActive {
		t.Errorf("status kosong harus FanClubActive=true (fail-open)")
	}

	// Bukan member sama sekali -> tidak ada sinyal klub.
	plain := &pb.User{}
	if toUser(plain).FanClubActive {
		t.Errorf("bukan member harus FanClubActive=false")
	}
}
