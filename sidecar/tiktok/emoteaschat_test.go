package main

import (
	"testing"

	gotiktoklive "github.com/steampoweredtaco/gotiktoklive"
)

// A standalone subscriber emote must reach the widget as a synthetic `chat`
// frame: one placeholder character per emote and a 0-based placeInComment for
// each, so the widget's existing chat renderer draws the artwork with no
// widget-side change.
func TestStandaloneEmoteAsChatSingle(t *testing.T) {
	comment, emotes := standaloneEmoteAsChat([]gotiktoklive.Emote{
		{EmoteID: "7089", ImageURL: "https://cdn/x.png", PrivateType: 1},
	})

	if r := []rune(comment); len(r) != 1 || r[0] != '\u200b' {
		t.Fatalf("comment = %q, want a single zero-width space", comment)
	}
	if len(emotes) != 1 {
		t.Fatalf("emotes len = %d, want 1", len(emotes))
	}
	m := emotes[0].(map[string]interface{})
	if m["placeInComment"] != 0 {
		t.Errorf("placeInComment = %v, want 0", m["placeInComment"])
	}
	if m["emoteId"] != "7089" {
		t.Errorf("emoteId = %v, want 7089", m["emoteId"])
	}
	if m["emoteImageUrl"] != "https://cdn/x.png" {
		t.Errorf("emoteImageUrl = %v", m["emoteImageUrl"])
	}
}

// Several emotes in one message get consecutive 0-based indexes and one
// placeholder each, matching the comment-index contract.
func TestStandaloneEmoteAsChatMultiple(t *testing.T) {
	comment, emotes := standaloneEmoteAsChat([]gotiktoklive.Emote{
		{EmoteID: "a"},
		{EmoteID: "b"},
		{EmoteID: "c"},
	})

	if len([]rune(comment)) != 3 {
		t.Fatalf("comment = %q, want 3 placeholders", comment)
	}
	for i, e := range emotes {
		if got := e.(map[string]interface{})["placeInComment"]; got != i {
			t.Errorf("emote %d placeInComment = %v, want %d", i, got, i)
		}
	}
}

// The helper must not mutate the caller's slice (the event may be reused).
func TestStandaloneEmoteAsChatDoesNotMutateInput(t *testing.T) {
	in := []gotiktoklive.Emote{{EmoteID: "a"}, {EmoteID: "b"}}
	_, _ = standaloneEmoteAsChat(in)
	if in[0].PlaceInComment != 0 || in[1].PlaceInComment != 0 {
		t.Errorf("input mutated: %+v", in)
	}
}

// The real-world bug: TikTok sends a subscriber emote through the chat path
// with an EMPTY comment while its emotes still carry indexes 0,1,2,… The widget
// drops any emote whose index is outside the comment text, so the comment must
// be padded with placeholders up to the last index.
func TestNormalizeCommentPadsEmptyComment(t *testing.T) {
	emotes := []gotiktoklive.Emote{
		{EmoteID: "a", PlaceInComment: 0},
		{EmoteID: "b", PlaceInComment: 1},
		{EmoteID: "c", PlaceInComment: 2},
	}
	got := normalizeComment("", emotes)
	if r := []rune(got); len(r) != 3 {
		t.Fatalf("comment runes = %d, want 3", len(r))
	}
	for _, ch := range got {
		if ch != '\u200b' {
			t.Fatalf("comment has non-placeholder rune %q", ch)
		}
	}
}

// A comment that already carries its own placeholder characters must be left
// exactly as-is.
func TestNormalizeCommentLeavesSufficientComment(t *testing.T) {
	comment := "hi \ufffd"
	got := normalizeComment(comment, []gotiktoklive.Emote{{PlaceInComment: 3}})
	if got != comment {
		t.Fatalf("comment = %q, want unchanged %q", got, comment)
	}
}

// An emote at index N needs the comment to be at least N+1 characters long.
func TestNormalizeCommentPadsToLastIndex(t *testing.T) {
	got := normalizeComment("", []gotiktoklive.Emote{{PlaceInComment: 4}})
	if r := []rune(got); len(r) != 5 {
		t.Fatalf("comment runes = %d, want 5", len(r))
	}
}

// No emotes: the comment is untouched.
func TestNormalizeCommentNoEmotes(t *testing.T) {
	if got := normalizeComment("hello", nil); got != "hello" {
		t.Fatalf("comment = %q, want hello", got)
	}
}
