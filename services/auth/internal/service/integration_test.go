//go:build integration

package service_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/aizorix/platform/auth/internal/itest"
	"github.com/aizorix/platform/auth/internal/service"
	"github.com/aizorix/platform/auth/internal/store"
	"github.com/aizorix/platform/pkg/crypto"
	"github.com/aizorix/platform/pkg/token"
)

func newService(t *testing.T) *service.Service {
	t.Helper()
	pool := itest.NewPostgres(t)
	st := store.New(pool)

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	iss := token.NewIssuer(priv, "auth-it-1", "https://auth.aizorix.com", "aizorix", 15*time.Minute)

	// Cheap Argon2 params keep registration/login fast in the test container.
	cfg := service.Config{
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
		Argon2:     crypto.Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
	}
	return service.New(st, iss, cfg)
}

// TestRegisterLoginRefreshReuse drives the full happy path and the reuse-detection breach
// response: register -> login -> refresh (rotation issues a NEW refresh token) -> replay the
// OLD refresh token (family revoked, ErrTokenReuse) -> subsequent refresh of the rotated
// token also fails because the whole family is burned.
func TestRegisterLoginRefreshReuse(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	reg, err := svc.Register(ctx, "alice@example.com", "correct horse battery staple",
		"freelancer", "US", "en-US", "1.2.3.4", "test-agent")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if reg.RefreshToken == "" || reg.AccessToken == "" {
		t.Fatal("register returned empty tokens")
	}

	// Login issues an independent session/refresh token.
	login, err := svc.Login(ctx, "alice@example.com", "correct horse battery staple", "1.2.3.4", "test-agent")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if login.RefreshToken == "" {
		t.Fatal("login returned empty refresh token")
	}

	// Refresh rotates: a brand new refresh token, distinct from the one presented.
	rot, err := svc.Refresh(ctx, login.RefreshToken, "1.2.3.4", "test-agent")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if rot.RefreshToken == "" {
		t.Fatal("refresh returned empty token")
	}
	if rot.RefreshToken == login.RefreshToken {
		t.Fatal("rotation must issue a NEW refresh token, got the same one back")
	}

	// Reuse the OLD (already-rotated) refresh token => credential theft => burn the family.
	if _, err := svc.Refresh(ctx, login.RefreshToken, "9.9.9.9", "attacker"); !errors.Is(err, service.ErrTokenReuse) {
		t.Fatalf("expected ErrTokenReuse on reuse of rotated token, got %v", err)
	}

	// The family is now revoked: the legitimately-rotated token can no longer refresh either.
	if _, err := svc.Refresh(ctx, rot.RefreshToken, "1.2.3.4", "test-agent"); err == nil {
		t.Fatal("expected refresh of a token in a burned family to fail")
	} else if !errors.Is(err, service.ErrTokenExpired) && !errors.Is(err, service.ErrTokenReuse) {
		t.Fatalf("expected ErrTokenExpired/ErrTokenReuse after family revocation, got %v", err)
	}
}

// TestRegisterDuplicateEmail asserts the unique-active-email guard maps to ErrEmailTaken.
func TestRegisterDuplicateEmail(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	if _, err := svc.Register(ctx, "bob@example.com", "pw-12345678", "client", "US", "en-US", "1.1.1.1", "ua"); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if _, err := svc.Register(ctx, "bob@example.com", "pw-12345678", "client", "US", "en-US", "1.1.1.1", "ua"); !errors.Is(err, service.ErrEmailTaken) {
		t.Fatalf("expected ErrEmailTaken on duplicate email, got %v", err)
	}
}
