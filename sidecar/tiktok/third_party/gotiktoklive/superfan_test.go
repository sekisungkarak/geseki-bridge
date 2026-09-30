package gotiktoklive

import (
	"testing"

	pb "github.com/steampoweredtaco/gotiktoklive/proto"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// Super Fan notices reach the wire as WebcastBarrageMessage (became a Super Fan
// / a Super Fan joined) or WebcastEnvelopeMessage (sent a Super Fan Box).
// Upstream's parseMsg had no case for either, so every one was dropped. These
// tests pin the three kinds and the "not a Super Fan" fall-through.

// withUnknownField appends a length-delimited field the vendored descriptor does
// not declare, so parseMsg has to recover it from the raw bytes.
func withUnknownField(t *testing.T, msg proto.Message, num protowire.Number, payload []byte) []byte {
	t.Helper()
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw = protowire.AppendTag(raw, num, protowire.BytesType)
	return protowire.AppendBytes(raw, payload)
}

func parseBarrage(t *testing.T, payload []byte) Event {
	t.Helper()
	ev, err := parseMsg(&pb.WebcastResponse_Message{
		Method:  "WebcastBarrageMessage",
		Payload: payload,
	}, func(...interface{}) {}, func(...interface{}) {}, false)
	if err != nil {
		t.Fatalf("parseMsg: %v", err)
	}
	return ev
}

// TestParseMsgBarrageSuperFanNew: content.key == ttlive_superfan means the
// viewer just became a Super Fan. The sender is carried in field 50 (user),
// which the vendored descriptor does not declare.
func TestParseMsgBarrageSuperFanNew(t *testing.T) {
	userRaw, err := proto.Marshal(&pb.User{Id: 42, Nickname: "Ana", IdStr: "ana"})
	if err != nil {
		t.Fatal(err)
	}
	payload := withUnknownField(t, &pb.WebcastBarrageMessage{
		Common:  &pb.Common{MsgId: 99, CreateTime: 100},
		Content: &pb.Text{Key: "ttlive_superfan"},
	}, 50, userRaw)

	ev := parseBarrage(t, payload)
	sf, ok := ev.(SuperFanEvent)
	if !ok {
		t.Fatalf("expected SuperFanEvent, got %T", ev)
	}
	if sf.Event != SUPER_FAN_NEW {
		t.Errorf("Event = %q, want %q", sf.Event, SUPER_FAN_NEW)
	}
	if sf.User == nil || sf.User.Nickname != "Ana" {
		t.Fatalf("user from unknown field 50 not recovered: %+v", sf.User)
	}
	if sf.User.ID != 42 {
		t.Errorf("user id = %d, want 42", sf.User.ID)
	}
}

// TestParseMsgBarrageSuperFanJoin: the "superfanjoined" marker must win over the
// generic one, matching TikTok Live Connector's ordering.
func TestParseMsgBarrageSuperFanJoin(t *testing.T) {
	payload := withUnknownField(t, &pb.WebcastBarrageMessage{
		Common:  &pb.Common{MsgId: 1, CreateTime: 2},
		Content: &pb.Text{Key: "ttlive_superfan_commentnotif_superfanjoined"},
	}, 50, mustMarshal(t, &pb.User{Id: 1, Nickname: "old fan"}))

	ev := parseBarrage(t, payload)
	sf, ok := ev.(SuperFanEvent)
	if !ok {
		t.Fatalf("expected SuperFanEvent, got %T", ev)
	}
	if sf.Event != SUPER_FAN_JOIN {
		t.Errorf("Event = %q, want %q", sf.Event, SUPER_FAN_JOIN)
	}
}

// TestParseMsgBarrageCommonBarrageContent: the marker can also arrive in field
// 24 (commonBarrageContent.key), which is undeclared here and must be read off
// the wire.
func TestParseMsgBarrageCommonBarrageContent(t *testing.T) {
	inner := mustMarshal(t, &pb.Text{Key: "ttlive_superfan_commentnotif_superfanjoined"})
	payload := withUnknownField(t, &pb.WebcastBarrageMessage{
		Common: &pb.Common{MsgId: 5, CreateTime: 6},
	}, 24, inner)

	ev := parseBarrage(t, payload)
	sf, ok := ev.(SuperFanEvent)
	if !ok {
		t.Fatalf("expected SuperFanEvent, got %T", ev)
	}
	if sf.Event != SUPER_FAN_JOIN {
		t.Errorf("Event = %q, want %q", sf.Event, SUPER_FAN_JOIN)
	}
}

// TestParseMsgBarrageNonSuperFanDropped: an ordinary system banner carries no
// Super Fan marker and must not surface as an event.
func TestParseMsgBarrageNonSuperFanDropped(t *testing.T) {
	payload := mustMarshal(t, &pb.WebcastBarrageMessage{
		Common:  &pb.Common{MsgId: 1},
		Content: &pb.Text{Key: "ttlive_something_else"},
	})
	if ev := parseBarrage(t, payload); ev != nil {
		t.Fatalf("expected nil (dropped), got %T", ev)
	}
}

// TestParseMsgEnvelopeSuperFanBox: a Super Fan Box is an envelope whose
// businessType is SUPER_FAN_BOX (19). The vendored enum stops at 7, so this
// guards the numeric compare. Sender details come from the flat sendUser* fields.
func TestParseMsgEnvelopeSuperFanBox(t *testing.T) {
	msg := &pb.WebcastEnvelopeMessage{
		Common: &pb.Common{MsgId: 9, CreateTime: 10},
		EnvelopeInfo: &pb.WebcastEnvelopeMessage_EnvelopeInfo{
			BusinessType:   pb.EnvelopeBusinessType(19),
			SendUserId:     "12345",
			SendUserName:   "Boxer",
			SendUserAvatar: &pb.Image{UrlList: []string{"http://a/x.jpg"}},
			DiamondCount:   500,
		},
	}
	raw, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}

	ev, err := parseMsg(&pb.WebcastResponse_Message{
		Method:  "WebcastEnvelopeMessage",
		Payload: raw,
	}, func(...interface{}) {}, func(...interface{}) {}, false)
	if err != nil {
		t.Fatalf("parseMsg: %v", err)
	}
	sf, ok := ev.(SuperFanEvent)
	if !ok {
		t.Fatalf("expected SuperFanEvent, got %T", ev)
	}
	if sf.Event != SUPER_FAN_BOX {
		t.Errorf("Event = %q, want %q", sf.Event, SUPER_FAN_BOX)
	}
	if sf.DiamondCount != 500 {
		t.Errorf("DiamondCount = %d, want 500", sf.DiamondCount)
	}
	if sf.User == nil || sf.User.Nickname != "Boxer" {
		t.Fatalf("user not built from envelope: %+v", sf.User)
	}
	if sf.User.ID != 12345 {
		t.Errorf("user id = %d, want 12345", sf.User.ID)
	}
}

// TestParseMsgEnvelopeOtherBusinessTypeDropped: any other envelope (diamonds,
// portal, …) is not a Super Fan Box.
func TestParseMsgEnvelopeOtherBusinessTypeDropped(t *testing.T) {
	raw, err := proto.Marshal(&pb.WebcastEnvelopeMessage{
		Common: &pb.Common{MsgId: 1},
		EnvelopeInfo: &pb.WebcastEnvelopeMessage_EnvelopeInfo{
			BusinessType: pb.EnvelopeBusinessType(2), // platform diamond
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := parseMsg(&pb.WebcastResponse_Message{
		Method:  "WebcastEnvelopeMessage",
		Payload: raw,
	}, func(...interface{}) {}, func(...interface{}) {}, false)
	if err != nil {
		t.Fatalf("parseMsg: %v", err)
	}
	if ev != nil {
		t.Fatalf("expected nil (dropped), got %T", ev)
	}
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
