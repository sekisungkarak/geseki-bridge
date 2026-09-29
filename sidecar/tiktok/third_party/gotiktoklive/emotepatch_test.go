package gotiktoklive

import (
	"testing"

	pb "github.com/steampoweredtaco/gotiktoklive/proto"
	"google.golang.org/protobuf/proto"
)

// TestToEmotesReadsPlaceholderIndex guards the subscriber-emote fix: TikTok
// stores a placeholder character in the comment text and the artwork in
// emotesList, where `index` is the 0-based position of that placeholder
// (matching TikTok Live Connector's `placeInComment`). Before this the emotes
// were dropped entirely, so a subscriber emote rendered as a raw placeholder.
func TestToEmotesReadsPlaceholderIndex(t *testing.T) {
	const art = "https://p19-webcast.tiktokcdn.com/emote/thumb.png~tplv-obj.image"
	list := []*pb.WebcastChatMessage_EmoteWithIndex{
		{Index: 4, Emote: &pb.Emote{EmoteId: "7089", Image: &pb.Image{UrlList: []string{art}}}},
	}

	got := toEmotes(list)
	if len(got) != 1 {
		t.Fatalf("expected 1 emote, got %d", len(got))
	}
	if got[0].EmoteID != "7089" {
		t.Fatalf("emoteId = %q, want %q", got[0].EmoteID, "7089")
	}
	if got[0].PlaceInComment != 4 {
		t.Fatalf("placeInComment = %d, want 4", got[0].PlaceInComment)
	}
	if got[0].ImageURL != art {
		t.Fatalf("imageUrl = %q, want %q", got[0].ImageURL, art)
	}
}

// TestToEmoteListMarksSubscriberWave: a standalone subscriber emote is flagged
// with EMOTE_PRIVATE_TYPE_SUB_WAVE; the flag must survive so consumers can tell
// a subscriber emote from a normal one.
func TestToEmoteListMarksSubscriberWave(t *testing.T) {
	got := toEmoteList([]*pb.Emote{{
		EmoteId:          "9001",
		Image:            &pb.Image{UrlList: []string{"https://cdn/sub_wave.png"}},
		EmotePrivateType: pb.EmotePrivateType_EMOTE_PRIVATE_TYPE_SUB_WAVE,
	}})
	if len(got) != 1 {
		t.Fatalf("expected 1 emote, got %d", len(got))
	}
	if got[0].PrivateType != int(pb.EmotePrivateType_EMOTE_PRIVATE_TYPE_SUB_WAVE) {
		t.Fatalf("privateType = %d, want SUB_WAVE(%d)", got[0].PrivateType, pb.EmotePrivateType_EMOTE_PRIVATE_TYPE_SUB_WAVE)
	}
	if got[0].ImageURL != "https://cdn/sub_wave.png" {
		t.Fatalf("imageUrl = %q", got[0].ImageURL)
	}
}

// TestParseMsgChatCarriesEmotes walks the real parse path so a refactor that
// drops the field again fails here rather than in production.
func TestParseMsgChatCarriesEmotes(t *testing.T) {
	const art = "https://cdn/emote.png"
	msg := &pb.WebcastChatMessage{
		Common:  &pb.Common{MsgId: 1, CreateTime: 2},
		User:    &pb.User{Id: 7, Nickname: "viewer"},
		Content: "hi \ufffcthere",
		EmotesList: []*pb.WebcastChatMessage_EmoteWithIndex{
			{Index: 3, Emote: &pb.Emote{EmoteId: "e1", Image: &pb.Image{UrlList: []string{art}}}},
		},
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

	ev, err := parseMsg(&pb.WebcastResponse_Message{
		Method:  "WebcastChatMessage",
		Payload: raw,
	}, func(...interface{}) {}, func(...interface{}) {}, false)
	if err != nil {
		t.Fatal(err)
	}
	chat, ok := ev.(ChatEvent)
	if !ok {
		t.Fatalf("expected ChatEvent, got %T", ev)
	}
	if len(chat.Emotes) != 1 || chat.Emotes[0].ImageURL != art {
		t.Fatalf("chat emotes not carried through parse: %+v", chat.Emotes)
	}
}
