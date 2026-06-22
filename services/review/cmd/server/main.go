// Command server boots the review service.
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

	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/pg"
	"github.com/aizorix/platform/review/internal/contractparties"
	"github.com/aizorix/platform/review/internal/httpapi"
	"github.com/aizorix/platform/review/internal/service"
	"github.com/aizorix/platform/review/internal/store"
)

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "review", base.Environment)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pg.Connect(ctx, base.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	// Contract service base URL for the internal parties lookup that authorizes reviews.
	contractURL := config.Get("CONTRACT_URL", "http://contract:8080")
	svc := service.New(store.New(pool), contractparties.New(contractURL))
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(base.HTTPPort),
		Handler:           httpapi.New(svc, logger).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		logger.Info("review http listening", "addr", srv.Addr)
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
