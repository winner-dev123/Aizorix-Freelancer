// Package store is the notification data layer: notifications, per-user channel
// preferences, and delivery attempts. The notification_channel enum is
// ('in_app','email','push','sms').
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Notification is the read/write model for notifications.
type Notification struct {
	ID        string
	UserID    string
	Type      string
	Title     string
	Body      *string
	Data      map[string]any
	ReadAt    *time.Time
	CreatedAt time.Time
}

// InsertNotification writes a notification row and returns its generated id.
func (s *Store) InsertNotification(ctx context.Context, tx pgx.Tx, userID, typ, title string, body *string, data map[string]any) (string, error) {
	payload, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO notifications (user_id, type, title, body, data)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id`, userID, typ, title, body, payload).Scan(&id)
	return id, err
}

// EnabledChannels returns the channels enabled for (user_id, event_type). When the user has
// configured no preferences for the event, the caller defaults to in_app.
func (s *Store) EnabledChannels(ctx context.Context, tx pgx.Tx, userID, eventType string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT channel FROM notification_preferences
		WHERE user_id=$1 AND event_type=$2 AND enabled=true`, userID, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

// InsertDeliveryAttempt queues a delivery attempt for a notification on one channel.
func (s *Store) InsertDeliveryAttempt(ctx context.Context, tx pgx.Tx, notificationID, channel, status string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO delivery_attempts (notification_id, channel, status)
		VALUES ($1,$2,$3)
		RETURNING id`, notificationID, channel, status).Scan(&id)
	return id, err
}

// MarkDeliveryStatus updates a delivery attempt's status (and optional provider ref/error).
func (s *Store) MarkDeliveryStatus(ctx context.Context, tx pgx.Tx, attemptID, status string, providerRef, errMsg *string) error {
	_, err := tx.Exec(ctx, `
		UPDATE delivery_attempts SET status=$2, provider_ref=$3, error=$4 WHERE id=$1`,
		attemptID, status, providerRef, errMsg)
	return err
}

// ListNotifications returns a user's notifications newest-first, optionally unread only.
func (s *Store) ListNotifications(ctx context.Context, userID string, unreadOnly bool, limit int) ([]Notification, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, type, title, body, data, read_at, created_at
		FROM notifications
		WHERE user_id=$1 AND ($2::bool = false OR read_at IS NULL)
		ORDER BY created_at DESC
		LIMIT $3`, userID, unreadOnly, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		var data []byte
		if err := rows.Scan(&n.ID, &n.UserID, &n.Type, &n.Title, &n.Body, &data, &n.ReadAt, &n.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(data, &n.Data)
		out = append(out, n)
	}
	return out, rows.Err()
}

// MarkRead stamps read_at for a single notification owned by the user. Returns ErrNotFound
// when no matching row exists.
func (s *Store) MarkRead(ctx context.Context, tx pgx.Tx, notificationID, userID string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE notifications SET read_at=now() WHERE id=$1 AND user_id=$2 AND read_at IS NULL`,
		notificationID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkAllRead stamps read_at for all of a user's unread notifications.
func (s *Store) MarkAllRead(ctx context.Context, tx pgx.Tx, userID string) (int64, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE notifications SET read_at=now() WHERE user_id=$1 AND read_at IS NULL`, userID)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Preference is the read/write model for notification_preferences.
type Preference struct {
	EventType string
	Channel   string
	Enabled   bool
}

// GetPreferences returns all configured preferences for a user.
func (s *Store) GetPreferences(ctx context.Context, userID string) ([]Preference, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT event_type, channel, enabled FROM notification_preferences
		WHERE user_id=$1 ORDER BY event_type, channel`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Preference
	for rows.Next() {
		var p Preference
		if err := rows.Scan(&p.EventType, &p.Channel, &p.Enabled); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertPreference sets the enabled flag for (user_id, event_type, channel).
func (s *Store) UpsertPreference(ctx context.Context, tx pgx.Tx, userID, eventType, channel string, enabled bool) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO notification_preferences (user_id, event_type, channel, enabled)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (user_id, event_type, channel) DO UPDATE SET enabled = EXCLUDED.enabled`,
		userID, eventType, channel, enabled)
	return err
}
