// Package store is the messaging data layer: conversations, participants, messages, and
// attachments. The messages table is range-partitioned by created_at, so inserts omit the
// id/created_at columns and let the database fill defaults, returning them via RETURNING.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound       = errors.New("store: not found")
	ErrNotParticipant = errors.New("store: user is not a conversation participant")
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store  { return &Store{pool: pool} }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Conversation is the read/write model for conversations.
type Conversation struct {
	ID            string
	ContractID    *string
	ProjectID     *string
	Subject       *string
	LastMessageAt *time.Time
	CreatedAt     time.Time
}

// InsertConversation creates a conversation and returns the generated id + created_at.
func (s *Store) InsertConversation(ctx context.Context, tx pgx.Tx, contractID, projectID, subject *string) (Conversation, error) {
	var c Conversation
	err := tx.QueryRow(ctx, `
		INSERT INTO conversations (contract_id, project_id, subject)
		VALUES ($1,$2,$3)
		RETURNING id, contract_id, project_id, subject, last_message_at, created_at`,
		contractID, projectID, subject).
		Scan(&c.ID, &c.ContractID, &c.ProjectID, &c.Subject, &c.LastMessageAt, &c.CreatedAt)
	return c, err
}

// AddParticipant adds a user to a conversation (idempotent on the composite pk).
func (s *Store) AddParticipant(ctx context.Context, tx pgx.Tx, conversationID, userID, role string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO conversation_participants (conversation_id, user_id, role)
		VALUES ($1,$2,$3)
		ON CONFLICT (conversation_id, user_id) DO NOTHING`, conversationID, userID, role)
	return err
}

// IsParticipant reports whether the user is a member of the conversation.
func (s *Store) IsParticipant(ctx context.Context, q querier, conversationID, userID string) (bool, error) {
	var one int
	err := q.QueryRow(ctx, `
		SELECT 1 FROM conversation_participants WHERE conversation_id=$1 AND user_id=$2`,
		conversationID, userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Message is the read/write model for messages.
type Message struct {
	ID             string
	ConversationID string
	SenderID       string
	Body           *string
	Kind           string
	CreatedAt      time.Time
	EditedAt       *time.Time
	DeletedAt      *time.Time
}

// InsertMessage inserts into the partitioned messages table. id and created_at are
// database-generated, so they are not supplied and are returned via RETURNING.
func (s *Store) InsertMessage(ctx context.Context, tx pgx.Tx, conversationID, senderID string, body *string, kind string) (Message, error) {
	var m Message
	err := tx.QueryRow(ctx, `
		INSERT INTO messages (conversation_id, sender_id, body, kind)
		VALUES ($1,$2,$3,$4)
		RETURNING id, created_at`, conversationID, senderID, body, kind).
		Scan(&m.ID, &m.CreatedAt)
	if err != nil {
		return m, err
	}
	m.ConversationID = conversationID
	m.SenderID = senderID
	m.Body = body
	m.Kind = kind
	return m, nil
}

// TouchConversation bumps last_message_at to now() after a message is posted.
func (s *Store) TouchConversation(ctx context.Context, tx pgx.Tx, conversationID string) error {
	_, err := tx.Exec(ctx, `UPDATE conversations SET last_message_at=now() WHERE id=$1`, conversationID)
	return err
}

// ListMessages returns non-deleted messages newest-first, optionally before a timestamp.
func (s *Store) ListMessages(ctx context.Context, conversationID string, limit int, before *time.Time) ([]Message, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, conversation_id, sender_id, body, kind, created_at, edited_at, deleted_at
		FROM messages
		WHERE conversation_id=$1
		  AND deleted_at IS NULL
		  AND ($2::timestamptz IS NULL OR created_at < $2)
		ORDER BY created_at DESC
		LIMIT $3`, conversationID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.ID, &m.ConversationID, &m.SenderID, &m.Body, &m.Kind, &m.CreatedAt, &m.EditedAt, &m.DeletedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListConversations returns conversations the user participates in, most-recent first.
func (s *Store) ListConversations(ctx context.Context, userID string) ([]Conversation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.contract_id, c.project_id, c.subject, c.last_message_at, c.created_at
		FROM conversations c
		JOIN conversation_participants p ON p.conversation_id = c.id
		WHERE p.user_id = $1
		ORDER BY c.last_message_at DESC NULLS LAST, c.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		var c Conversation
		if err := rows.Scan(&c.ID, &c.ContractID, &c.ProjectID, &c.Subject, &c.LastMessageAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MarkRead stamps the participant's last_read_at (read receipt). Returns ErrNotParticipant
// when the user is not a member of the conversation.
func (s *Store) MarkRead(ctx context.Context, tx pgx.Tx, conversationID, userID string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE conversation_participants SET last_read_at=now()
		WHERE conversation_id=$1 AND user_id=$2`, conversationID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotParticipant
	}
	return nil
}

// AddAttachment links a stored file to a message.
func (s *Store) AddAttachment(ctx context.Context, tx pgx.Tx, messageID, s3Key, filename string, sizeBytes int64, contentType string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO message_attachments (message_id, s3_key, filename, size_bytes, content_type)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id`, messageID, s3Key, filename, sizeBytes, contentType).Scan(&id)
	return id, err
}

// querier abstracts *pgxpool.Pool and pgx.Tx so participant checks work in both contexts.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PoolQuerier exposes the pool as a querier for non-transactional reads.
func (s *Store) PoolQuerier() querier { return s.pool }
