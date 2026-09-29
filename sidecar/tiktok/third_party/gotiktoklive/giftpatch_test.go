package gotiktoklive

import (
	"testing"

	pb "github.com/steampoweredtaco/gotiktoklive/proto"
	"google.golang.org/protobuf/proto"
)

// TestGiftPictureURL verifies our vendored patch: the gift image parsed from
// the protobuf must reach GiftEvent.PictureURL, matching TikFinity's
// giftPictureUrl. Regression guard for the upstream gap where GiftEvent had no
// image field at all.
func TestGiftPictureURL(t *testing.T) {
	gift := &pb.WebcastGiftMessage{
		Common: &pb.Common{MsgId: 42, CreateTime: 1700000000},
		GiftId: 5655,
		Gift: &pb.GiftStruct{
			Name:         "Rose",
			Describe:     "Rose",
			DiamondCount: 1,
			// Only the icon is populated, exactly like real TikTok traffic:
			// the large "image" field is usually absent.
			Icon: &pb.Image{UrlList: []string{
				"https://p16-webcast.tiktokcdn.com/rose_small.webp",
				"https://p16-webcast.tiktokcdn.com/rose_large.webp",
			}},
		},
		RepeatCount: 3,
	}
	payload, err := proto.Marshal(gift)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	ev, err := parseMsg(&pb.WebcastResponse_Message{
		Method:  "WebcastGiftMessage",
		Payload: payload,
	}, func(...interface{}) {}, func(...interface{}) {}, false)
	if err != nil {
		t.Fatalf("parseMsg: %v", err)
	}

	ge, ok := ev.(GiftEvent)
	if !ok {
		t.Fatalf("expected GiftEvent, got %T", ev)
	}
	const want = "https://p16-webcast.tiktokcdn.com/rose_large.webp"
	if ge.PictureURL != want {
		t.Fatalf("PictureURL = %q, want %q", ge.PictureURL, want)
	}
	if ge.Name != "Rose" || ge.RepeatCount != 3 {
		t.Fatalf("other fields wrong: name=%q repeat=%d", ge.Name, ge.RepeatCount)
	}
}

// TestGiftPictureURLPrefersImage verifies field priority: the large gift
// artwork ("image") wins over the smaller icon when both are present.
func TestGiftPictureURLPrefersImage(t *testing.T) {
	gift := &pb.WebcastGiftMessage{
		Common: &pb.Common{MsgId: 43, CreateTime: 1700000001},
		GiftId: 5655,
		Gift: &pb.GiftStruct{
			Name:  "Galaxy",
			Image: &pb.Image{UrlList: []string{"https://cdn/galaxy.png"}},
			Icon:  &pb.Image{UrlList: []string{"https://cdn/galaxy_icon.png"}},
		},
	}
	payload, _ := proto.Marshal(gift)
	ev, err := parseMsg(&pb.WebcastResponse_Message{
		Method:  "WebcastGiftMessage",
		Payload: payload,
	}, func(...interface{}) {}, func(...interface{}) {}, false)
	if err != nil {
		t.Fatalf("parseMsg: %v", err)
	}
	if got := ev.(GiftEvent).PictureURL; got != "https://cdn/galaxy.png" {
		t.Fatalf("PictureURL = %q, want the large image", got)
	}
}
