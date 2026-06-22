// Package service implements notification business logic: consuming domain events and
// fanning them out to channels per user preferences, plus read-state management.
//
// A Kafka consumer (a separate runner, not part of this REST process) calls HandleEvent for
// each domain event (e.g. proposal.submitted, milestone.approved, message.sent). The same
// logic is exposed over HTTP via POST /v1/notifications/internal/dispatch for testing and
// for other services that prefer a synchronous call.
package service

import (
	"context"

	"github.com/aizorix/platform/notification/internal/store"
)

// defaultChannel is used when a user has configured no preferences for an event type.
const defaultChannel = "in_app"

// Notification is the minimal view passed to a Sender for delivery.
type Notification struct {
	ID     string
	UserID string
	Type   string
	Title  string
	Body   *string
	Data   map[string]any
}

// Sender delivers a notification on a single channel (email/push/sms/in_app). The real
// implementations live behind provider clients; the default stub just marks delivery sent.
type Sender interface {
	Send(ctx context.Context, channel string, n Notification) error
}

// stubSender is the default no-op sender: it reports success so the delivery attempt is
// marked 'sent' without contacting any external provider.
type stubSender struct{}

func (stubSender) Send(context.Context, string, Notification) error { return nil }

type Service struct {
	store  *store.Store
	sender Sender
}

func New(st *store.Store) *Service { return &Service{store: st, sender: stubSender{}} }

// WithSender overrides the default stub sender (used in production wiring/tests).
func (s *Service) WithSender(sender Sender) *Service { s.sender = sender; return s }

// HandleEvent creates a notification for the user, resolves the enabled channels for the
// event type (defaulting to in_app), and queues a delivery attempt per channel. The stub
// sender then marks each attempt 'sent'. All writes happen in one transaction.
func (s *Service) HandleEvent(ctx context.Context, userID, eventType, title string, body *string, data map[string]any) (string, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	notifID, err := s.store.InsertNotification(ctx, tx, userID, eventType, title, body, data)
	if err != nil {
		return "", err
	}
	channels, err := s.store.EnabledChannels(ctx, tx, userID, eventType)
	if err != nil {
		return "", err
	}
	if len(channels) == 0 {
		channels = []string{defaultChannel}
	}
	n := Notification{ID: notifID, UserID: userID, Type: eventType, Title: title, Body: body, Data: data}
	for _, ch := range channels {
		attemptID, err := s.store.InsertDeliveryAttempt(ctx, tx, notifID, ch, "queued")
		if err != nil {
			return "", err
		}
		// Delegate actual sending to the channel sender. The stub marks delivery 'sent';
		// a failure leaves the attempt 'queued' for a retry runner to pick up.
		if err := s.sender.Send(ctx, ch, n); err != nil {
			continue
		}
		if err := s.store.MarkDeliveryStatus(ctx, tx, attemptID, "sent", nil, nil); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return notifID, nil
}

// ListNotifications returns the user's notifications, optionally unread only.
func (s *Service) ListNotifications(ctx context.Context, userID string, unreadOnly bool, limit int) ([]store.Notification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.store.ListNotifications(ctx, userID, unreadOnly, limit)
}

// MarkRead marks a single notification read.
func (s *Service) MarkRead(ctx context.Context, notificationID, userID string) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.store.MarkRead(ctx, tx, notificationID, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkAllRead marks all of a user's notifications read and returns the count updated.
func (s *Service) MarkAllRead(ctx context.Context, userID string) (int64, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	n, err := s.store.MarkAllRead(ctx, tx, userID)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return n, nil
}

// GetPreferences returns the user's channel preferences.
func (s *Service) GetPreferences(ctx context.Context, userID string) ([]store.Preference, error) {
	return s.store.GetPreferences(ctx, userID)
}

// SetPreference upserts a single (event_type, channel) preference for the user.
func (s *Service) SetPreference(ctx context.Context, userID, eventType, channel string, enabled bool) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := s.store.UpsertPreference(ctx, tx, userID, eventType, channel, enabled); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
