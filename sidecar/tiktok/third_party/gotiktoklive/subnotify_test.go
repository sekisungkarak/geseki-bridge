package gotiktoklive

import (
	"testing"

	pb "github.com/steampoweredtaco/gotiktoklive/proto"
	"google.golang.org/protobuf/proto"
)

// TestParseMsgSubNotifyProducesUserEvent guards the subscribe fix: TikTok sends
// a subscription as its own WebcastSubNotifyMessage, but upstream's parseMsg
// had no case for it, so every subscribe hit the default branch and was
// dropped. protocol.md documented a `subscribe` event that therefore never
// occurred.
func TestParseMsgSubNotifyProducesUserEvent(t *testing.T) {
	msg := &pb.WebcastSubNotifyMessage{
		Common: &pb.Common{MsgId: 11, CreateTime: 22},
		User:   &pb.User{Id: 7, Nickname: "subber"},
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

	ev, err := parseMsg(&pb.WebcastResponse_Message{
		Method:  "WebcastSubNotifyMessage",
		Payload: raw,
	}, func(...interface{}) {}, func(...interface{}) {}, false)
	if err != nil {
		t.Fatal(err)
	}
	ue, ok := ev.(UserEvent)
	if !ok {
		t.Fatalf("expected UserEvent, got %T", ev)
	}
	if ue.Event != USER_SUBSCRIBE {
		t.Fatalf("Event = %q, want %q", ue.Event, USER_SUBSCRIBE)
	}
	if ue.User == nil || ue.User.Nickname != "subber" {
		t.Fatalf("user not carried through: %+v", ue.User)
	}
}

// TestToUserTypeSubscribed: a subscribe can also arrive as a
// WebcastMemberMessage with action SUBSCRIBED. That string used to fall through
// to the "not implemented" fallback, which the sidecar's type switch ignored.
func TestToUserTypeSubscribed(t *testing.T) {
	if got := toUserType("SUBSCRIBED"); got != USER_SUBSCRIBE {
		t.Fatalf("toUserType(SUBSCRIBED) = %q, want %q", got, USER_SUBSCRIBE)
	}
}
