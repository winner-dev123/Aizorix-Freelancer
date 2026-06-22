// Command server boots the wsgateway: a JWT-authenticated WebSocket fan-out gateway for
// real-time messaging and presence. It terminates WebSocket connections (sticky via an NLB) and
// uses Redis pub/sub to fan events out across replicas, so the service scales horizontally.
package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/wsgateway/internal/auth"
	wsconfig "github.com/aizorix/platform/wsgateway/internal/config"
	"github.com/aizorix/platform/wsgateway/internal/httpapi"
	"github.com/aizorix/platform/wsgateway/internal/hub"
	"github.com/redis/go-redis/v9"
)

func main() {
	cfg := wsconfig.Load()
	logger := log.New(cfg.Base.LogLevel, "wsgateway", cfg.Base.Environment)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// JWT verifier: fetch the JWKS now and refresh it in the background. Failing the first
	// fetch is fatal — a gateway that can verify no tokens would reject every upgrade.
	verifier, err := auth.NewVerifier(ctx, cfg.JWKSURL, cfg.Issuer, cfg.Audience, logger)
	if err != nil {
		logger.Error("jwks init failed", "err", err, "jwks_url", cfg.JWKSURL)
		os.Exit(1)
	}
	go verifier.Run(ctx, cfg.JWKSRefresh)

	// Redis backs cross-replica pub/sub fan-out and presence. An empty addr degrades to
	// single-replica local delivery (useful for local dev).
	var rdb *redis.Client
	if cfg.RedisAddr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
		defer rdb.Close()
	} else {
		logger.Warn("REDIS_ADDR empty: running single-replica with local-only fan-out")
	}

	bus := hub.NewBus(rdb, cfg.PresenceTTL, logger)
	persister := hub.NewPersister(cfg.MessagingURL)
	h := hub.New(cfg, bus, persister, logger)

	api := httpapi.New(cfg, verifier, h, logger)
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(cfg.Base.HTTPPort),
		Handler:           api.Routes(),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		// BaseContext ties every connection (including hijacked WebSockets) to ctx, so a
		// shutdown signal propagates into the per-connection write pumps and closes them.
		BaseContext: func(_ net.Listener) context.Context { return ctx },
	}

	go func() {
		logger.Info("wsgateway listening", "addr", srv.Addr, "redis", cfg.RedisAddr, "messaging", cfg.MessagingURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
