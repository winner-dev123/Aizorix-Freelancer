package hub

import (
	"encoding/json"
	"testing"

	"github.com/aizorix/platform/wsgateway/internal/config"
)

// TestPresenceRefCounting pins the presence ref-counting invariant: a user stays "online" while
// any of their connections on this replica remains, and unregister reports "last" only when the
// final one closes. A regression would either flicker a user offline while a tab is still open or
// never clear presence. register/unregister touch only the in-memory maps (no Redis), so a hub
// built with nil bus/persister/logger exercises the real logic.
func TestPresenceRefCounting(t *testing.T) {
	h := New(config.Config{}, nil, nil, nil)
	newConnFor := func(uid string) *Conn { return &Conn{userID: uid, joined: map[string]struct{}{}} }

	c1, c2 := newConnFor("u1"), newConnFor("u1") // same user, two connections
	c3 := newConnFor("u2")
	h.register(c1)
	h.register(c2)
	h.register(c3)

	if last := h.unregister(c1); last {
		t.Fatal("u1 still has c2 open — unregister(c1) must NOT report last-for-user")
	}
	if last := h.unregister(c2); !last {
		t.Fatal("u1's final connection closed — unregister(c2) must report last-for-user")
	}
	if last := h.unregister(c3); !last {
		t.Fatal("u2's only connection closed — must report last-for-user")
	}
}

// TestErrorFrame pins that error frames serialize to the documented wire shape.
func TestErrorFrame(t *testing.T) {
	var o Outbound
	if err := json.Unmarshal(errorFrame("FORBIDDEN", "not a participant"), &o); err != nil {
		t.Fatalf("error frame is not valid JSON: %v", err)
	}
	if o.Type != TypeError || o.Code != "FORBIDDEN" || o.Message != "not a participant" {
		t.Fatalf("error frame = %+v, want type=error code=FORBIDDEN", o)
	}
}

// TestInboundDecode covers decoding of a client "send" frame.
func TestInboundDecode(t *testing.T) {
	var in Inbound
	if err := json.Unmarshal([]byte(`{"type":"send","conversation_id":"c1","body":"hi","ref":"r1"}`), &in); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if in.Type != TypeSend || in.ConversationID != "c1" || in.Body != "hi" || in.Ref != "r1" {
		t.Fatalf("decoded inbound = %+v", in)
	}
}
