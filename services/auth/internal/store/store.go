// Package store is the auth service's data-access layer over PostgreSQL (pgx).
// It owns only credential/session tables; profile data is fetched from the user service.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the pool for transactions spanning store + outbox.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

type User struct {
	ID            string
	Email         string
	PasswordHash  string
	Status        string
	PrimaryType   string
	EmailVerified bool
	MFAEnabled    bool
	Residency     string
	FailedLogins  int
	LockedUntil   *time.Time
}

// CreateUser inserts a new pending user inside tx and returns its id.
func (s *Store) CreateUser(ctx context.Context, tx pgx.Tx, email, hash, accountType, residency, locale string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, primary_type, residency_country, locale, status)
		VALUES ($1,$2,$3,$4,$5,'pending')
		RETURNING id`,
		email, hash, accountType, residency, locale).Scan(&id)
	return id, err
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, coalesce(password_hash,''), status, primary_type,
		       email_verified, mfa_enabled, coalesce(residency_country,''),
		       failed_login_count, locked_until
		FROM users WHERE email = $1 AND deleted_at IS NULL`, email).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Status, &u.PrimaryType,
			&u.EmailVerified, &u.MFAEnabled, &u.Residency, &u.FailedLogins, &u.LockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// GetUserByID powers the /v1/auth/me identity lookup.
func (s *Store) GetUserByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, email, coalesce(password_hash,''), status, primary_type,
		       email_verified, mfa_enabled, coalesce(residency_country,''),
		       failed_login_count, locked_until
		FROM users WHERE id = $1 AND deleted_at IS NULL`, id).
		Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Status, &u.PrimaryType,
			&u.EmailVerified, &u.MFAEnabled, &u.Residency, &u.FailedLogins, &u.LockedUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

func (s *Store) RecordLoginSuccess(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET last_login_at = now(), failed_login_count = 0, locked_until = NULL
		WHERE id = $1`, userID)
	return err
}

// RecordLoginFailure increments the counter and locks the account after a threshold.
func (s *Store) RecordLoginFailure(ctx context.Context, userID string, threshold int, lockFor time.Duration) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users
		SET failed_login_count = failed_login_count + 1,
		    locked_until = CASE WHEN failed_login_count + 1 >= $2 THEN now() + $3::interval ELSE locked_until END
		WHERE id = $1`, userID, threshold, lockFor.String())
	return err
}

// Roles+permissions for a user, used to populate JWT claims.
func (s *Store) RolesAndPermissions(ctx context.Context, userID string) ([]string, []string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.name FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = $1`, userID)
	if err != nil {
		return nil, nil, err
	}
	var roles []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			rows.Close()
			return nil, nil, err
		}
		roles = append(roles, r)
	}
	rows.Close()

	prows, err := s.pool.Query(ctx, `
		SELECT DISTINCT p.code
		FROM user_roles ur
		JOIN role_permissions rp ON rp.role_id = ur.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE ur.user_id = $1`, userID)
	if err != nil {
		return nil, nil, err
	}
	defer prows.Close()
	var perms []string
	for prows.Next() {
		var c string
		if err := prows.Scan(&c); err != nil {
			return nil, nil, err
		}
		perms = append(perms, c)
	}
	return roles, perms, nil
}

func (s *Store) AssignRole(ctx context.Context, tx pgx.Tx, userID, roleName string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id, scope)
		SELECT $1, r.id, '' FROM roles r WHERE r.name = $2
		ON CONFLICT DO NOTHING`, userID, roleName)
	return err
}

// ── Sessions & refresh tokens ───────────────────────────────────────────────

type Session struct {
	ID           string
	DeviceName   string
	IP           string
	UserAgent    string
	CreatedAt    time.Time
	LastActiveAt time.Time
}

func (s *Store) CreateSession(ctx context.Context, tx pgx.Tx, userID, ip, ua string, ttl time.Duration) (sessionID, familyID string, err error) {
	err = tx.QueryRow(ctx, `
		INSERT INTO sessions (user_id, ip, user_agent, expires_at)
		VALUES ($1,$2,$3, now() + $4::interval)
		RETURNING id, family_id`, userID, ip, ua, ttl.String()).Scan(&sessionID, &familyID)
	return
}

// InsertRefreshToken stores only the hash; the raw token is returned to the client once.
func (s *Store) InsertRefreshToken(ctx context.Context, tx pgx.Tx, sessionID, userID string, tokenHash []byte, ttl time.Duration) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO refresh_tokens (session_id, user_id, token_hash, expires_at)
		VALUES ($1,$2,$3, now() + $4::interval)
		RETURNING id`, sessionID, userID, tokenHash, ttl.String()).Scan(&id)
	return id, err
}

type RefreshToken struct {
	ID        string
	SessionID string
	UserID    string
	FamilyID  string
	UsedAt    *time.Time
	RevokedAt *time.Time
	ExpiresAt time.Time
}

// LookupRefreshToken joins to the session for family + revocation context.
func (s *Store) LookupRefreshToken(ctx context.Context, tx pgx.Tx, tokenHash []byte) (*RefreshToken, error) {
	rt := &RefreshToken{}
	err := tx.QueryRow(ctx, `
		SELECT rt.id, rt.session_id, rt.user_id, s.family_id, rt.used_at, rt.revoked_at, rt.expires_at
		FROM refresh_tokens rt JOIN sessions s ON s.id = rt.session_id
		WHERE rt.token_hash = $1
		FOR UPDATE OF rt`, tokenHash).
		Scan(&rt.ID, &rt.SessionID, &rt.UserID, &rt.FamilyID, &rt.UsedAt, &rt.RevokedAt, &rt.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return rt, err
}

func (s *Store) MarkTokenUsed(ctx context.Context, tx pgx.Tx, id, replacedBy string) error {
	_, err := tx.Exec(ctx, `UPDATE refresh_tokens SET used_at = now(), replaced_by = $2 WHERE id = $1`, id, replacedBy)
	return err
}

// RevokeFamily is the breach response: a reused (already-rotated) token revokes every
// session in its family and all their tokens.
func (s *Store) RevokeFamily(ctx context.Context, tx pgx.Tx, familyID, reason string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE sessions SET revoked_at = now(), revoked_reason = $2
		WHERE family_id = $1 AND revoked_at IS NULL`, familyID, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = now()
		WHERE session_id IN (SELECT id FROM sessions WHERE family_id = $1) AND revoked_at IS NULL`, familyID)
	return err
}

// RevokeSessionByID revokes a session and all its refresh tokens within tx (used by logout,
// where the caller is identified by the refresh token rather than a user id).
func (s *Store) RevokeSessionByID(ctx context.Context, tx pgx.Tx, sessionID, reason string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE sessions SET revoked_at = now(), revoked_reason = $2
		WHERE id = $1 AND revoked_at IS NULL`, sessionID, reason); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = now()
		WHERE session_id = $1 AND revoked_at IS NULL`, sessionID)
	return err
}

// SessionRevokedForUpdate locks the session row (FOR UPDATE) and reports whether it is
// revoked. Taking the lock in the refresh-rotation path serializes against RevokeFamily,
// closing the TOCTOU window where a concurrently-revoked family could still mint a new token.
func (s *Store) SessionRevokedForUpdate(ctx context.Context, tx pgx.Tx, sessionID string) (bool, error) {
	var revokedAt *time.Time
	err := tx.QueryRow(ctx, `SELECT revoked_at FROM sessions WHERE id = $1 FOR UPDATE`, sessionID).Scan(&revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return true, err
	}
	return revokedAt != nil, nil
}

func (s *Store) RevokeSession(ctx context.Context, userID, sessionID, reason string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = now(), revoked_reason = $3
		WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`, sessionID, userID, reason)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, coalesce(d.display_name,''), coalesce(host(s.ip),''), coalesce(s.user_agent,''),
		       s.created_at, s.last_active_at
		FROM sessions s LEFT JOIN devices d ON d.id = s.device_id
		WHERE s.user_id = $1 AND s.revoked_at IS NULL
		ORDER BY s.last_active_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var ses Session
		if err := rows.Scan(&ses.ID, &ses.DeviceName, &ses.IP, &ses.UserAgent, &ses.CreatedAt, &ses.LastActiveAt); err != nil {
			return nil, err
		}
		out = append(out, ses)
	}
	return out, nil
}
