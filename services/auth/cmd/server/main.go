// Command server boots the auth service: loads config, connects Postgres, loads the
// ES256 signing key, wires the service + HTTP transport, and runs the outbox relay.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aizorix/platform/auth/internal/httpapi"
	"github.com/aizorix/platform/auth/internal/service"
	"github.com/aizorix/platform/auth/internal/store"
	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/crypto"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/pg"
	"github.com/aizorix/platform/pkg/token"
)

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "auth", base.Environment)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pg.Connect(ctx, base.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	priv, kid, err := loadSigningKey(config.Get("JWT_ACCESS_PRIVATE_KEY_PEM", ""), base.Environment, logger)
	if err != nil {
		logger.Error("signing key", "err", err)
		os.Exit(1)
	}
	issuer := token.NewIssuer(priv, kid, "https://auth.aizorix.com", "aizorix",
		config.GetDuration("JWT_ACCESS_TTL", 15*time.Minute))

	svc := service.New(store.New(pool), issuer, service.Config{
		AccessTTL:  config.GetDuration("JWT_ACCESS_TTL", 15*time.Minute),
		RefreshTTL: config.GetDuration("JWT_REFRESH_TTL", 720*time.Hour),
		Argon2: crypto.Argon2Params{
			Memory:      uint32(config.GetInt("ARGON2_MEMORY_KIB", 65536)),
			Iterations:  uint32(config.GetInt("ARGON2_ITERATIONS", 3)),
			Parallelism: uint8(config.GetInt("ARGON2_PARALLELISM", 2)),
			SaltLength:  16, KeyLength: 32,
		},
	})

	api := httpapi.New(svc)
	// Secure cookies only over HTTPS/production; local HTTP dev needs them non-Secure or the
	// browser won't store the refresh cookie (silently breaking cookie-gated navigation).
	api.SetCookieSecure(base.Environment == "production")
	if jwksJSON, jerr := buildJWKS(kid, &priv.PublicKey); jerr == nil {
		api.SetJWKS(jwksJSON)
	} else {
		logger.Warn("jwks build failed", "err", jerr)
	}

	srv := &http.Server{
		Addr:              ":" + itoa(base.HTTPPort),
		Handler:           api.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// NOTE: in production a separate deployment runs the outbox relay against this DB and
	// publishes to Kafka; it is omitted from the API pod to keep responsibilities clean.

	go func() {
		logger.Info("auth http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// loadSigningKey reads an EC P-256 PEM private key. In local/dev with no key configured,
// it generates an ephemeral key so the service runs out of the box (tokens reset on restart).
func loadSigningKey(pemPath, env string, logger *slog.Logger) (*ecdsa.PrivateKey, string, error) {
	if pemPath == "" {
		if env == "production" {
			return nil, "", errors.New("JWT_ACCESS_PRIVATE_KEY_PEM is required in production")
		}
		logger.Warn("no signing key configured; generating ephemeral dev key")
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		return k, "dev-ephemeral", err
	}
	data, err := os.ReadFile(pemPath)
	if err != nil {
		return nil, "", err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, "", errors.New("invalid PEM")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS#8.
		pk, perr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if perr != nil {
			return nil, "", err
		}
		ek, ok := pk.(*ecdsa.PrivateKey)
		if !ok {
			return nil, "", errors.New("not an EC key")
		}
		key = ek
	}
	return key, config.Get("JWT_ACCESS_KID", "auth-1"), nil
}

// buildJWKS renders the issuer's public key as a JWKS document for /.well-known/jwks.json.
func buildJWKS(kid string, pub *ecdsa.PublicKey) (string, error) {
	keys, err := token.MarshalJWKS(map[string]*ecdsa.PublicKey{kid: pub})
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{"keys": keys})
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
