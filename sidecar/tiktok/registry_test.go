package main

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// parseMsg looks a wire message up by name in the global proto registry. If the
// subscriber-emote type is not registered under the exact name TikTok sends on
// the wire, the message is dropped before any of our code sees it. This test
// pins the name so a proto regeneration cannot silently break emote delivery.
func TestSubscriberEmoteTypeIsRegistered(t *testing.T) {
	names := []string{
		"WebcastEmoteChatMessage",
		"WebcastChatMessage",
	}
	for _, n := range names {
		mt, err := protoregistry.GlobalTypes.FindMessageByName(protoreflect.FullName(n))
		if err != nil {
			t.Errorf("FindMessageByName(%q) failed: %v", n, err)
			continue
		}
		t.Logf("registered: %s -> %T", n, mt.New().Interface())
	}
}
