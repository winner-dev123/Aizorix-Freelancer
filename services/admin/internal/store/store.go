// Package store is the admin service's data-access layer over PostgreSQL (pgx).
// It OWNS migration 000010: admin_actions (every privileged operation an admin performs)
// and audit_logs (append-only, partitioned, tamper-evident accountability log).
//
// It also reads and mutates tables OWNED BY OTHER SERVICES — users (user svc), disputes
// (contract svc) and screenshots (screenshot svc). In production these cross-service
// effects would go through the owning service's API/gRPC; here every service shares one
// logical database, so for the scaffold we touch the shared schema directly. Each such
// method is marked CROSS-SERVICE.
//
// Write methods take a pgx.Tx so the cross-service state change, the admin_actions row and
// the audit_logs row commit atomically (and, where applicable, alongside the outbox event).
package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a row lookup yields pgx.ErrNoRows.
var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the pool for transactions spanning store + outbox.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ── domain types ────────────────────────────────────────────────────────────

// AdminAction is one row of admin_actions: a record of a privileged operation.
type AdminAction struct {
	ID         string
	AdminID    string
	Action     string
	TargetType string
	TargetID   string
	Reason     *string // nullable
	Metadata   []byte  // jsonb
	CreatedAt  time.Time
}

// AuditLog is one row of the append-only, partitioned audit_logs table.
type AuditLog struct {
	ID           int64
	OccurredAt   time.Time
	ActorID      *string // nullable
	ActorType    string
	Action       string
	ResourceType string
	ResourceID   *string // nullable
	IP           *string // nullable (inet rendered as text)
	UserAgent    *string // nullable
	Context      []byte  // jsonb
}

// Screenshot is a projection of the screenshot service's screenshots table, listed here
// for audit only. CROSS-SERVICE read.
type Screenshot struct {
	ID            string
	WorkSessionID string
	UserID        string
	Status        string
	CapturedAt    time.Time
	S3Key         string
}

// ── owned writes: admin_actions ──────────────────────────────────────────────

// InsertAdminAction appends an admin_actions row inside tx and returns its id. metadata
// may be nil, in which case the column default '{}' is used.
func (s *Store) InsertAdminAction(ctx context.Context, tx pgx.Tx, a AdminAction) (string, error) {
	if a.Metadata == nil {
		a.Metadata = []byte("{}")
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO admin_actions (admin_id, action, target_type, target_id, reason, metadata)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id`,
		a.AdminID, a.Action, a.TargetType, a.TargetID, a.Reason, a.Metadata).Scan(&id)
	return id, err
}

// ── owned writes: audit_logs ─────────────────────────────────────────────────

// InsertAuditLog appends an audit_logs row inside tx. occurred_at defaults to now(); the
// partition is selected by that default. ip is cast to inet ($6::inet); pass nil for none.
//
// row_hash is a self-contained integrity stamp over the row's content, computed at insert (cheap,
// no extra query/contention). Paired with the append-only enforcement (migration 000014, which
// blocks UPDATE/DELETE), it makes any edit to an audit row detectable. (prev_hash — cross-row
// chaining — remains a dedicated signer's job; row_hash is now always populated, never NULL.)
func (s *Store) InsertAuditLog(ctx context.Context, tx pgx.Tx, l AuditLog) error {
	if l.Context == nil {
		l.Context = []byte("{}")
	}
	if l.ActorType == "" {
		l.ActorType = "user"
	}
	rowHash := auditRowHash(l)
	_, err := tx.Exec(ctx, `
		INSERT INTO audit_logs
			(actor_id, actor_type, action, resource_type, resource_id, ip, user_agent, context, row_hash)
		VALUES ($1,$2,$3,$4,$5,$6::inet,$7,$8,$9)`,
		l.ActorID, l.ActorType, l.Action, l.ResourceType, l.ResourceID, l.IP, l.UserAgent, l.Context, rowHash)
	return err
}

// auditRowHash deterministically hashes an audit row's content. Fields are separated by an
// ASCII record-separator (0x1e) so distinct field boundaries can't be forged by concatenation.
func auditRowHash(l AuditLog) []byte {
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	h := sha256.New()
	for _, f := range []string{l.ActorType, deref(l.ActorID), l.Action, l.ResourceType, deref(l.ResourceID), deref(l.IP), deref(l.UserAgent)} {
		h.Write([]byte{0x1e})
		h.Write([]byte(f))
	}
	h.Write([]byte{0x1e})
	h.Write(l.Context)
	return h.Sum(nil)
}

// ── cross-service writes ──────────────────────────────────────────────────────

// SetUserStatus updates users.status (a user_status enum value). CROSS-SERVICE write —
// in prod this is the user service's responsibility. Returns false (no error) when no row
// matched so the caller can map ErrNotFound.
func (s *Store) SetUserStatus(ctx context.Context, tx pgx.Tx, userID, status string) (bool, error) {
	ct, err := tx.Exec(ctx, `UPDATE users SET status = $2 WHERE id = $1`, userID, status)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// ResolveDispute sets a dispute's terminal resolution. resolution must be one of the
// dispute_status enum resolution values. CROSS-SERVICE write (contract service owns
// disputes). Returns false when no row matched.
func (s *Store) ResolveDispute(ctx context.Context, tx pgx.Tx, disputeID, resolution string, note *string, adminID string) (bool, error) {
	ct, err := tx.Exec(ctx, `
		UPDATE disputes
		SET status = $2, resolution_note = $3, assigned_admin = $4, resolved_at = now()
		WHERE id = $1`,
		disputeID, resolution, note, adminID)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 1, nil
}

// ── cross-service reads ────────────────────────────────────────────────────────

// ScreenshotFilter narrows ListScreenshots; nil fields are ignored.
type ScreenshotFilter struct {
	UserID *string
	Status *string
	Limit  int
}

// ListScreenshots returns screenshots for audit, newest first. CROSS-SERVICE read.
func (s *Store) ListScreenshots(ctx context.Context, f ScreenshotFilter) ([]Screenshot, error) {
	// The screenshots table (migration 000007) has session_id + freelancer_id — NOT
	// work_session_id / user_id (the old query 42703'd on every call). f.UserID filters the
	// freelancer who owns the captures.
	rows, err := s.pool.Query(ctx, `
		SELECT id, session_id, freelancer_id, status, captured_at, s3_key
		FROM screenshots
		WHERE ($1::uuid IS NULL OR freelancer_id = $1)
		  AND ($2::text IS NULL OR status = $2)
		ORDER BY captured_at DESC
		LIMIT $3`, f.UserID, f.Status, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Screenshot
	for rows.Next() {
		var sc Screenshot
		if err := rows.Scan(&sc.ID, &sc.WorkSessionID, &sc.UserID, &sc.Status, &sc.CapturedAt, &sc.S3Key); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// UserExists reports whether a user row exists (used to map suspend/reinstate to NotFound
// even when the status update is a no-op because the user is already in that status).
func (s *Store) UserExists(ctx context.Context, tx pgx.Tx, userID string) (bool, error) {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM users WHERE id = $1`, userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ── owned reads ───────────────────────────────────────────────────────────────

// ListAdminActions returns admin_actions newest first.
func (s *Store) ListAdminActions(ctx context.Context, limit int) ([]AdminAction, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, admin_id, action, target_type, target_id, reason, metadata, created_at
		FROM admin_actions
		ORDER BY created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminAction
	for rows.Next() {
		var a AdminAction
		if err := rows.Scan(&a.ID, &a.AdminID, &a.Action, &a.TargetType, &a.TargetID, &a.Reason, &a.Metadata, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AuditLogFilter narrows ListAuditLogs; nil fields are ignored.
type AuditLogFilter struct {
	ActorID *string
	Action  *string
	Limit   int
}

// ListAuditLogs returns audit_logs newest first. ip is rendered to text via host().
func (s *Store) ListAuditLogs(ctx context.Context, f AuditLogFilter) ([]AuditLog, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, occurred_at, actor_id, actor_type, action, resource_type, resource_id,
		       host(ip), user_agent, context
		FROM audit_logs
		WHERE ($1::uuid IS NULL OR actor_id = $1)
		  AND ($2::text IS NULL OR action = $2)
		ORDER BY occurred_at DESC, id DESC
		LIMIT $3`, f.ActorID, f.Action, f.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditLog
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(&l.ID, &l.OccurredAt, &l.ActorID, &l.ActorType, &l.Action,
			&l.ResourceType, &l.ResourceID, &l.IP, &l.UserAgent, &l.Context); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}
