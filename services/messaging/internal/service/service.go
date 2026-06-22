// Package service implements messaging business logic: conversation creation, message
// posting (with participant authorization), read receipts, and attachments.
//
// NOTE: real-time delivery (WebSocket push) is handled by a separate gateway service that
// consumes the message.sent events emitted here. This service is REST-only.
package service

import (
	"context"
	"time"

	"github.com/aizorix/platform/messaging/internal/store"
	"github.com/aizorix/platform/pkg/outbox"
)

var (
	ErrNotFound       = store.ErrNotFound
	ErrNotParticipant = store.ErrNotParticipant
)

type Service struct{ store *store.Store }

func New(st *store.Store) *Service { return &Service{store: st} }

// CreateConversation creates a conversation and adds the creator plus any other
// participants. The creator gets the 'owner' role; the rest are 'member'.
func (s *Service) CreateConversation(ctx context.Context, creatorUserID string, contractID, projectID, subject *string, participantUserIDs []string) (*store.Conversation, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	conv, err := s.store.InsertConversation(ctx, tx, contractID, projectID, subject)
	if err != nil {
		return nil, err
	}
	if err := s.store.AddParticipant(ctx, tx, conv.ID, creatorUserID, "owner"); err != nil {
		return nil, err
	}
	for _, uid := range participantUserIDs {
		if uid == "" || uid == creatorUserID {
			continue
		}
		if err := s.store.AddParticipant(ctx, tx, conv.ID, uid, "member"); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &conv, nil
}

// PostMessage verifies the sender is a participant, inserts the message, bumps the
// conversation, and emits message.sent so the notification service can fan out.
func (s *Service) PostMessage(ctx context.Context, conversationID, senderID string, body *string, kind string) (*store.Message, error) {
	if kind == "" {
		kind = "text"
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	ok, err := s.store.IsParticipant(ctx, tx, conversationID, senderID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotParticipant
	}
	msg, err := s.store.InsertMessage(ctx, tx, conversationID, senderID, body, kind)
	if err != nil {
		return nil, err
	}
	if err := s.store.TouchConversation(ctx, tx, conversationID); err != nil {
		return nil, err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "conversation", AggregateID: conversationID, EventType: "message.sent",
		Topic: "messaging.events", PartitionKey: conversationID,
		Payload: map[string]any{
			"message_id":      msg.ID,
			"conversation_id": conversationID,
			"sender_id":       senderID,
			"kind":            kind,
		},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &msg, nil
}

// ListMessages returns the conversation history after verifying the requester is a member.
func (s *Service) ListMessages(ctx context.Context, conversationID, requesterUserID string, limit int, before *time.Time) ([]store.Message, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	ok, err := s.store.IsParticipant(ctx, s.store.PoolQuerier(), conversationID, requesterUserID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotParticipant
	}
	return s.store.ListMessages(ctx, conversationID, limit, before)
}

// ListConversations returns the conversations the user participates in.
func (s *Service) ListConversations(ctx context.Context, userID string) ([]store.Conversation, error) {
	return s.store.ListConversations(ctx, userID)
}

// IsParticipant reports whether the user is a member of the conversation. Used by the
// wsgateway to authorize a WebSocket "join" before subscribing the connection to live
// conversation events.
func (s *Service) IsParticipant(ctx context.Context, conversationID, userID string) (bool, error) {
	return s.store.IsParticipant(ctx, s.store.PoolQuerier(), conversationID, userID)
}

// MarkRead records a read receipt for the user on the conversation.
func (s *Service) MarkRead(ctx context.Context, conversationID, userID string) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.store.MarkRead(ctx, tx, conversationID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AddAttachment links an uploaded file to a message.
func (s *Service) AddAttachment(ctx context.Context, messageID, s3Key, filename string, sizeBytes int64, contentType string) (string, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	id, err := s.store.AddAttachment(ctx, tx, messageID, s3Key, filename, sizeBytes, contentType)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}
