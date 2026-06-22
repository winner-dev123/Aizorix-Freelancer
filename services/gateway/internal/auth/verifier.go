// Package auth holds the gateway's identity layer: a JWKS-backed token verifier
// that refreshes the auth service's public keys in the background, plus the HTTP
// middleware that authenticates protected routes and injects trusted identity
// headers for downstream services.
package auth

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/aizorix/platform/pkg/token"
)

// Verifier wraps a token.Verifier whose key set is periodically refreshed from the
// auth service's JWKS endpoint. The underlying verifier is swapped atomically under
// an RWMutex so request-path verification never blocks on a refresh.
type Verifier struct {
	jwksURL  string
	issuer   string
	audience string
	client   *http.Client
	logger   *slog.Logger

	mu    sync.RWMutex
	inner *token.Verifier
}

// NewVerifier performs the initial JWKS fetch and returns a ready Verifier. It
// fails if the first fetch fails — the gateway should not start unable to verify
// any token (that would 401 all protected traffic).
func NewVerifier(ctx context.Context, jwksURL, issuer, audience string, logger *slog.Logger) (*Verifier, error) {
	v := &Verifier{
		jwksURL:  jwksURL,
		issuer:   issuer,
		audience: audience,
		client:   &http.Client{Timeout: 10 * time.Second},
		logger:   logger,
	}
	if err := v.refresh(ctx); err != nil {
		return nil, fmt.Errorf("initial jwks fetch: %w", err)
	}
	return v, nil
}

// Verify validates a raw bearer token against the current key set.
func (v *Verifier) Verify(raw string) (*token.Claims, error) {
	v.mu.RLock()
	inner := v.inner
	v.mu.RUnlock()
	return inner.Verify(raw)
}

// refresh fetches the JWKS document and atomically swaps in a new verifier. On any
// failure it keeps the previously loaded keys (verification continues to work
// through transient auth-service blips and key-rotation windows).
func (v *Verifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	keys, err := token.ParseJWKS(body)
	if err != nil {
		return err
	}
	v.swap(keys)
	return nil
}

func (v *Verifier) swap(keys map[string]*ecdsa.PublicKey) {
	inner := token.NewVerifier(keys, v.issuer, v.audience)
	v.mu.Lock()
	v.inner = inner
	v.mu.Unlock()
}

// Run refreshes the key set on the given interval until ctx is cancelled. Refresh
// failures are logged and the existing keys are retained — a flaky JWKS endpoint
// must never take down token verification.
func (v *Verifier) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if err := v.refresh(rctx); err != nil {
				v.logger.Warn("jwks refresh failed; keeping existing keys", "err", err)
			} else {
				v.logger.Debug("jwks refreshed")
			}
			cancel()
		}
	}
}
