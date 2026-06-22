// Package service holds the auth business logic: registration, login (with lockout),
// refresh-token rotation with reuse detection, and session management. Transport
// (HTTP/gRPC) is a thin adapter over these methods.
package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aizorix/platform/auth/internal/store"
	"github.com/aizorix/platform/pkg/crypto"
	"github.com/aizorix/platform/pkg/outbox"
	"github.com/aizorix/platform/pkg/token"
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrAccountLocked      = errors.New("account temporarily locked")
	ErrEmailTaken         = errors.New("email already registered")
	ErrTokenReuse         = errors.New("refresh token reuse detected")
	ErrTokenExpired       = errors.New("refresh token expired or revoked")
)

const (
	lockoutThreshold = 5
	lockoutDuration  = 15 * time.Minute
)

type Config struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	Argon2     crypto.Argon2Params
}

type Service struct {
	store  *store.Store
	issuer *token.Issuer
	cfg    Config
}

func New(st *store.Store, iss *token.Issuer, cfg Config) *Service {
	return &Service{store: st, issuer: iss, cfg: cfg}
}

type Tokens struct {
	AccessToken     string
	RefreshToken    string
	AccessExpiresIn int64
	UserID          string
}

// Register creates a user, assigns the default role for the account type, issues tokens,
// and enqueues a user.registered event — all in one transaction (outbox pattern).
func (s *Service) Register(ctx context.Context, email, password, accountType, residency, locale, ip, ua string) (*Tokens, error) {
	hash, err := crypto.HashPassword(password, s.cfg.Argon2)
	if err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	userID, err := s.store.CreateUser(ctx, tx, email, hash, accountType, residency, locale)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	if err := s.store.AssignRole(ctx, tx, userID, accountType); err != nil {
		return nil, err
	}
	toks, err := s.issueSession(ctx, tx, userID, accountType, residency, ip, ua)
	if err != nil {
		return nil, err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "user", AggregateID: userID, EventType: "user.registered",
		Topic: "user.events", PartitionKey: userID,
		Payload: map[string]any{"user_id": userID, "account_type": accountType, "residency_country": residency},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return toks, nil
}

// Login verifies the password (constant-time), enforces lockout, and issues a session.
func (s *Service) Login(ctx context.Context, email, password, ip, ua string) (*Tokens, error) {
	u, err := s.store.GetUserByEmail(ctx, email)
	if errors.Is(err, store.ErrNotFound) {
		// Run a dummy hash to keep timing uniform and resist user enumeration.
		_, _ = crypto.HashPassword(password, s.cfg.Argon2)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}
	if u.LockedUntil != nil && u.LockedUntil.After(time.Now()) {
		return nil, ErrAccountLocked
	}
	ok, err := crypto.VerifyPassword(password, u.PasswordHash)
	if err != nil || !ok {
		_ = s.store.RecordLoginFailure(ctx, u.ID, lockoutThreshold, lockoutDuration)
		return nil, ErrInvalidCredentials
	}
	_ = s.store.RecordLoginSuccess(ctx, u.ID)

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	toks, err := s.issueSession(ctx, tx, u.ID, u.PrimaryType, u.Residency, ip, ua)
	if err != nil {
		return nil, err
	}
	_ = outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "session", AggregateID: toks.UserID, EventType: "session.created",
		Topic: "user.events", PartitionKey: toks.UserID,
		Payload: map[string]any{"user_id": toks.UserID, "ip": ip},
	})
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return toks, nil
}

// Refresh implements rotation with reuse detection. A valid, unused token is rotated;
// a token that was already used (i.e. an attacker replayed a stolen, rotated token)
// triggers revocation of the entire session family.
func (s *Service) Refresh(ctx context.Context, rawRefresh, ip, ua string) (*Tokens, error) {
	hash := sha256Sum(rawRefresh)
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	rt, err := s.store.LookupRefreshToken(ctx, tx, hash)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrTokenExpired
	}
	if err != nil {
		return nil, err
	}
	if rt.RevokedAt != nil || rt.ExpiresAt.Before(time.Now()) {
		return nil, ErrTokenExpired
	}
	if rt.UsedAt != nil {
		// Reuse of a rotated token => credential theft. Burn the family.
		_ = s.store.RevokeFamily(ctx, tx, rt.FamilyID, "refresh_token_reuse")
		_ = tx.Commit(ctx)
		return nil, ErrTokenReuse
	}

	// Re-check session/family revocation under a row lock before minting a new token. This
	// serializes against a concurrent reuse-triggered RevokeFamily, so a burned family can
	// never hand out a fresh, live token (closes the rotate-vs-revoke TOCTOU race).
	if revoked, err := s.store.SessionRevokedForUpdate(ctx, tx, rt.SessionID); err != nil {
		return nil, err
	} else if revoked {
		return nil, ErrTokenExpired
	}

	roles, perms, err := s.store.RolesAndPermissions(ctx, rt.UserID)
	if err != nil {
		return nil, err
	}
	newRaw, newHash := newOpaqueToken()
	newID, err := s.store.InsertRefreshToken(ctx, tx, rt.SessionID, rt.UserID, newHash, s.cfg.RefreshTTL)
	if err != nil {
		return nil, err
	}
	if err := s.store.MarkTokenUsed(ctx, tx, rt.ID, newID); err != nil {
		return nil, err
	}
	access, err := s.issuer.Issue(token.Claims{
		UserID: rt.UserID, SessionID: rt.SessionID, Roles: roles, Permissions: perms,
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Tokens{AccessToken: access, RefreshToken: newRaw,
		AccessExpiresIn: int64(s.cfg.AccessTTL.Seconds()), UserID: rt.UserID}, nil
}

func (s *Service) ListSessions(ctx context.Context, userID string) ([]store.Session, error) {
	return s.store.ListSessions(ctx, userID)
}

func (s *Service) RevokeSession(ctx context.Context, userID, sessionID string) error {
	return s.store.RevokeSession(ctx, userID, sessionID, "user_revoked")
}

// Logout revokes the session behind a refresh token (or the whole family when allSessions).
// It is idempotent: an unknown/already-revoked token is a no-op success.
func (s *Service) Logout(ctx context.Context, rawRefresh string, allSessions bool) error {
	if rawRefresh == "" {
		return nil
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rt, err := s.store.LookupRefreshToken(ctx, tx, sha256Sum(rawRefresh))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if allSessions {
		err = s.store.RevokeFamily(ctx, tx, rt.FamilyID, "user_logout_all")
	} else {
		err = s.store.RevokeSessionByID(ctx, tx, rt.SessionID, "user_logout")
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Identity is the /v1/auth/me response: who the bearer is, from credentials + roles.
type Identity struct {
	UserID        string
	Email         string
	AccountType   string
	Roles         []string
	EmailVerified bool
	MFAEnabled    bool
}

func (s *Service) Me(ctx context.Context, userID string) (*Identity, error) {
	u, err := s.store.GetUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	roles, _, err := s.store.RolesAndPermissions(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &Identity{
		UserID: u.ID, Email: u.Email, AccountType: u.PrimaryType, Roles: roles,
		EmailVerified: u.EmailVerified, MFAEnabled: u.MFAEnabled,
	}, nil
}

// issueSession creates a session + initial refresh token + signed access token inside tx.
func (s *Service) issueSession(ctx context.Context, tx pgx.Tx, userID, accountType, residency, ip, ua string) (*Tokens, error) {
	sessionID, _, err := s.store.CreateSession(ctx, tx, userID, ip, ua, s.cfg.RefreshTTL)
	if err != nil {
		return nil, err
	}
	rawRefresh, refreshHash := newOpaqueToken()
	if _, err := s.store.InsertRefreshToken(ctx, tx, sessionID, userID, refreshHash, s.cfg.RefreshTTL); err != nil {
		return nil, err
	}
	roles, perms, err := s.store.RolesAndPermissions(ctx, userID)
	if err != nil {
		return nil, err
	}
	access, err := s.issuer.Issue(token.Claims{
		UserID: userID, SessionID: sessionID, Roles: roles, Permissions: perms,
		ResidencyCountry: residency, AccountType: accountType,
	})
	if err != nil {
		return nil, err
	}
	return &Tokens{AccessToken: access, RefreshToken: rawRefresh,
		AccessExpiresIn: int64(s.cfg.AccessTTL.Seconds()), UserID: userID}, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

func newOpaqueToken() (raw string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	raw = base64.RawURLEncoding.EncodeToString(b)
	return raw, sha256Sum(raw)
}

func sha256Sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func isUniqueViolation(err error) bool {
	// pgx surfaces SQLSTATE 23505 for unique violations; checked via error string here to
	// avoid importing pgconn in this snippet — production uses errors.As(*pgconn.PgError).
	return err != nil && (containsCode(err.Error(), "23505") || containsCode(err.Error(), "uq_users_email_active"))
}

func containsCode(s, code string) bool {
	for i := 0; i+len(code) <= len(s); i++ {
		if s[i:i+len(code)] == code {
			return true
		}
	}
	return false
}
