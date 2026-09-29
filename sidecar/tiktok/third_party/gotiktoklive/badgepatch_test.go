package gotiktoklive

import (
	"testing"

	pb "github.com/steampoweredtaco/gotiktoklive/proto"
	"google.golang.org/protobuf/encoding/protowire"
)

// unknownTextBadge builds a TextBadge whose label sits in field 2, exactly as
// TikTok sends it. The vendored descriptor only declares field 3
// ("defaultPattern"), so field 2 lands in the unknown bytes — reproduced here
// byte-for-byte from a live Top Gifter badge:
//
//	\n\x1apm_mt_badeg_notes_profile3\x12\x05No. 3
func unknownTextBadge(label string) *pb.BadgeStruct_TextBadge {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, "pm_mt_badeg_notes_profile3")
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, label)
	tb := &pb.BadgeStruct_TextBadge{}
	tb.ProtoReflect().SetUnknown(b)
	return tb
}

// TestToUserDedupesDuplicateBadgeArtwork guards the "doubled badge" bug: TikTok
// sends the same Top Gifter artwork twice in one BadgeList — an IMAGE entry with
// no label and a COMBINE entry carrying "No. 3". Only one badge must survive,
// and it must keep the label.
func TestToUserDedupesDuplicateBadgeArtwork(t *testing.T) {
	const art = "https://p19-webcast.tiktokcdn.com/webcast-sg/new_top_gifter_version_2.png~tplv-obj.image"
	u := &pb.User{
		Id:       1,
		Nickname: "gifter",
		BadgeList: []*pb.BadgeStruct{
			{
				DisplayType: pb.BadgeStruct_BADGEDISPLAYTYPE_IMAGE,
				BadgeType: &pb.BadgeStruct_Image{Image: &pb.BadgeStruct_ImageBadge{
					Image: &pb.Image{UrlList: []string{art}},
				}},
			},
			{
				DisplayType: pb.BadgeStruct_BADGEDISPLAYTYPE_COMBINE,
				BadgeType: &pb.BadgeStruct_Combine{Combine: &pb.BadgeStruct_CombineBadge{
					Icon: &pb.Image{UrlList: []string{art}},
					Text: unknownTextBadge("No. 3"),
					Background: &pb.BadgeStruct_CombineBadgeBackground{
						BackgroundColorCode: "#66FE2C55",
					},
				}},
			},
		},
	}

	got := toUser(u)
	if got.Badge == nil {
		t.Fatal("expected badges")
	}
	if len(got.Badge.Badges) != 1 {
		t.Fatalf("expected 1 deduped badge, got %d: %+v", len(got.Badge.Badges), got.Badge.Badges)
	}
	b := got.Badge.Badges[0]
	if b.Name != "No. 3" {
		t.Fatalf("deduped badge lost its label: name=%q", b.Name)
	}
	if b.Image != art {
		t.Fatalf("deduped badge image = %q, want %q", b.Image, art)
	}
	if b.Color != "#66FE2C55" {
		t.Fatalf("deduped badge color = %q, want %q", b.Color, "#66FE2C55")
	}
}

// TestToUserKeepsDistinctBadges ensures dedupe only merges identical artwork:
// a grade badge and a fans-club badge must both survive.
func TestToUserKeepsDistinctBadges(t *testing.T) {
	u := &pb.User{
		Id: 1,
		BadgeList: []*pb.BadgeStruct{
			{DisplayType: pb.BadgeStruct_BADGEDISPLAYTYPE_COMBINE, BadgeType: &pb.BadgeStruct_Combine{Combine: &pb.BadgeStruct_CombineBadge{
				Icon: &pb.Image{UrlList: []string{"https://cdn/grade_lv18.png"}},
				Str:  "18",
			}}},
			{DisplayType: pb.BadgeStruct_BADGEDISPLAYTYPE_COMBINE, BadgeType: &pb.BadgeStruct_Combine{Combine: &pb.BadgeStruct_CombineBadge{
				Icon: &pb.Image{UrlList: []string{"https://cdn/fans_KCUN.png"}},
				Str:  "KCUN",
			}}},
		},
	}
	got := toUser(u)
	if len(got.Badge.Badges) != 2 {
		t.Fatalf("expected 2 badges, got %d", len(got.Badge.Badges))
	}
	if got.Badge.Badges[0].Name != "18" || got.Badge.Badges[1].Name != "KCUN" {
		t.Fatalf("badge order/labels wrong: %+v", got.Badge.Badges)
	}
}

func TestBadgeTextLabelFromUnknownField(t *testing.T) {
	if got := badgeTextLabel(unknownTextBadge("No. 3")); got != "No. 3" {
		t.Fatalf("badgeTextLabel = %q, want %q", got, "No. 3")
	}
}

// TestBadgeTextLabelFallsBackToDeclaredField guards the case where TikTok uses
// the declared field 3 instead of field 2.
func TestBadgeTextLabelFallsBackToDeclaredField(t *testing.T) {
	tb := &pb.BadgeStruct_TextBadge{DefaultPattern: "New gifter"}
	if got := badgeTextLabel(tb); got != "New gifter" {
		t.Fatalf("badgeTextLabel = %q, want %q", got, "New gifter")
	}
}

// TestToUserBadgeLabelFromUnknown walks the real path: a User whose badge is a
// Combine badge with a field-2 label must surface that label as the badge name,
// so the widget can render "3" next to the Top Gifter icon instead of nothing.
func TestToUserBadgeLabelFromUnknown(t *testing.T) {
	u := &pb.User{
		Id:       1,
		Nickname: "gifter",
		BadgeList: []*pb.BadgeStruct{{
			DisplayType: pb.BadgeStruct_BADGEDISPLAYTYPE_COMBINE,
			BadgeType: &pb.BadgeStruct_Combine{Combine: &pb.BadgeStruct_CombineBadge{
				Text: unknownTextBadge("No. 3"),
				Background: &pb.BadgeStruct_CombineBadgeBackground{
					BackgroundColorCode: "#66FE2C55",
				},
			}},
		}},
	}

	got := toUser(u)
	if got.Badge == nil || len(got.Badge.Badges) != 1 {
		t.Fatalf("expected one badge, got %+v", got.Badge)
	}
	b := got.Badge.Badges[0]
	if b.Name != "No. 3" {
		t.Fatalf("badge name = %q, want %q", b.Name, "No. 3")
	}
	if b.Color != "#66FE2C55" {
		t.Fatalf("badge color = %q, want %q", b.Color, "#66FE2C55")
	}
}
