// Command server boots the contract service.
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

	"github.com/aizorix/platform/contract/internal/httpapi"
	"github.com/aizorix/platform/contract/internal/proposallookup"
	"github.com/aizorix/platform/contract/internal/service"
	"github.com/aizorix/platform/contract/internal/store"
	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/pg"
)

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "contract", base.Environment)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pg.Connect(ctx, base.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	// PROPOSAL_URL is the proposal service's internal base URL (server-to-server). Default matches
	// the k8s/compose service DNS used by the other internal lookups.
	proposals := proposallookup.New(config.Get("PROPOSAL_URL", "http://proposal:8080"))
	svc := service.New(store.New(pool), proposals)
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(base.HTTPPort),
		Handler:           httpapi.New(svc, logger).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		logger.Info("contract http listening", "addr", srv.Addr)
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
