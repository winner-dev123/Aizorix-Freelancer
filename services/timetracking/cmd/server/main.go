// Command server boots the time-tracking service.
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
	"github.com/aizorix/platform/timetracking/internal/contractparties"
	"github.com/aizorix/platform/timetracking/internal/httpapi"
	"github.com/aizorix/platform/timetracking/internal/service"
	"github.com/aizorix/platform/timetracking/internal/store"
)

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "timetracking", base.Environment)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pg.Connect(ctx, base.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Contract service base URL for the internal parties lookup that authorizes session starts.
	contractURL := config.Get("CONTRACT_URL", "http://contract:8080")
	svc := service.New(store.New(pool), contractparties.New(contractURL))
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(base.HTTPPort),
		Handler:           httpapi.New(svc).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		logger.Info("timetracking http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	logger.Info("shutting down")
	sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sc)
}
