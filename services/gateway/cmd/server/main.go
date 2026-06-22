// Command server boots the API gateway: the single public entry point for the
// platform. It loads config, fetches the auth service's JWKS (and refreshes it in
// the background), wires the middleware chain (recover -> request-id -> access-log
// -> rate-limit -> auth -> reverse-proxy), exposes /metrics and /healthz, and runs
// an HTTP server with graceful shutdown.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/aizorix/platform/gateway/internal/auth"
	gwconfig "github.com/aizorix/platform/gateway/internal/config"
	"github.com/aizorix/platform/gateway/internal/observe"
	"github.com/aizorix/platform/gateway/internal/proxy"
	"github.com/aizorix/platform/gateway/internal/ratelimit"
	"github.com/aizorix/platform/pkg/log"
)

func main() {
	cfg := gwconfig.Load()
	logger := log.New(cfg.Base.LogLevel, "gateway", cfg.Base.Environment)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Build the JWKS-backed verifier (fails fast if auth's keys are unreachable on
	// boot — the gateway must be able to verify tokens to serve protected traffic).
	bootCtx, cancelBoot := context.WithTimeout(ctx, 15*time.Second)
	verifier, err := auth.NewVerifier(bootCtx, cfg.JWKSURL, cfg.Issuer, cfg.Audience, logger)
	cancelBoot()
	if err != nil {
		logger.Error("jwks bootstrap failed", "url", cfg.JWKSURL, "err", err)
		os.Exit(1)
	}
	go verifier.Run(ctx, cfg.JWKSRefresh)

	// Distributed rate limiter (fails open if Redis is down).
	limiter := ratelimit.New(
		cfg.RedisAddr,
		ratelimit.Bucket{Capacity: cfg.RateGeneralLimit, Window: cfg.RateWindow},
		ratelimit.Bucket{Capacity: cfg.RateAuthLimit, Window: cfg.RateWindow},
		cfg.TrustedProxies,
		logger,
	)
	defer func() { _ = limiter.Close() }()

	metrics := observe.NewMetrics()

	router, err := proxy.NewRouter(cfg, logger)
	if err != nil {
		logger.Error("router init failed", "err", err)
		os.Exit(1)
	}

	handler := proxy.Handler(router, verifier, limiter, metrics, logger)

	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Base.HTTPPort),
		Handler:           handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	go func() {
		logger.Info("gateway http listening", "addr", srv.Addr, "env", cfg.Base.Environment)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
	}
}
