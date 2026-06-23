//go:build integration

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aizorix/platform/messaging/internal/itest"
	"github.com/aizorix/platform/messaging/internal/service"
	"github.com/aizorix/platform/messaging/internal/store"
)

// TestConversationParticipantAuthz pins the participant-authorization guard — the messaging
// service's security-critical logic: only members of a conversation can read or post to it.
// An outsider is rejected with ErrNotParticipant at every guarded operation, and
// IsParticipant (the probe the wsgateway uses to gate a WebSocket join) reflects membership.
func TestConversationParticipantAuthz(t *testing.T) {
	ctx := context.Background()
	pool := itest.NewPostgres(t)
	u := itest.SeedUsers(ctx, t, pool)
	svc := service.New(store.New(pool))

	subject := "Kickoff"
	conv, err := svc.CreateConversation(ctx, u.Alice, nil, nil, &subject, []string{u.Bob})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	// Membership reflects the creator + the added participant, but not the outsider.
	for _, tc := range []struct {
		name string
		uid  string
		want bool
	}{
		{"creator", u.Alice, true},
		{"participant", u.Bob, true},
		{"outsider", u.Mallory, false},
	} {
		got, err := svc.IsParticipant(ctx, conv.ID, tc.uid)
		if err != nil {
			t.Fatalf("IsParticipant(%s): %v", tc.name, err)
		}
		if got != tc.want {
			t.Fatalf("IsParticipant(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}

	body := "hello there"

	// ── Forbidden: an outsider cannot post into the conversation. ──
	if _, err := svc.PostMessage(ctx, conv.ID, u.Mallory, &body, "text"); !errors.Is(err, service.ErrNotParticipant) {
		t.Fatalf("outsider PostMessage = %v, want ErrNotParticipant", err)
	}

	// Happy path: a participant posts.
	msg, err := svc.PostMessage(ctx, conv.ID, u.Bob, &body, "text")
	if err != nil {
		t.Fatalf("participant PostMessage: %v", err)
	}
	if msg.ConversationID != conv.ID {
		t.Fatalf("posted message conversation = %q, want %q", msg.ConversationID, conv.ID)
	}

	// ── Forbidden: an outsider cannot read the message history. ──
	if _, err := svc.ListMessages(ctx, conv.ID, u.Mallory, 50, nil); !errors.Is(err, service.ErrNotParticipant) {
		t.Fatalf("outsider ListMessages = %v, want ErrNotParticipant", err)
	}

	// Happy path: a participant reads and sees the posted message.
	msgs, err := svc.ListMessages(ctx, conv.ID, u.Alice, 50, nil)
	if err != nil {
		t.Fatalf("participant ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	// ── Forbidden: an outsider cannot stamp a read receipt. ──
	if err := svc.MarkRead(ctx, conv.ID, u.Mallory); !errors.Is(err, service.ErrNotParticipant) {
		t.Fatalf("outsider MarkRead = %v, want ErrNotParticipant", err)
	}

	// Happy path: a participant marks read.
	if err := svc.MarkRead(ctx, conv.ID, u.Bob); err != nil {
		t.Fatalf("participant MarkRead: %v", err)
	}
}
