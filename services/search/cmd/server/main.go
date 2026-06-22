// Command server boots the search service. It constructs the search engine (an OpenSearch
// stub in dev/tests that falls back to Postgres full-text search) and hands it to the
// service alongside the Postgres store.
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

	"github.com/aizorix/platform/search/internal/httpapi"
	"github.com/aizorix/platform/search/internal/service"
	"github.com/aizorix/platform/search/internal/store"
	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/pg"
)

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "search", base.Environment)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pg.Connect(ctx, base.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	st := store.New(pool)
	// Select the search engine at runtime: a configured ELASTICSEARCH_URL (or OPENSEARCH_URL)
	// wires the real OpenSearch client; otherwise the Postgres-FTS stub is used. The selection
	// lives in store.NewSearchEngine so cmd/server and cmd/consumer stay in sync.
	esURL := config.Get("ELASTICSEARCH_URL", config.Get("OPENSEARCH_URL", ""))
	es := store.NewSearchEngine(pool, esURL,
		config.Get("OPENSEARCH_USERNAME", ""), config.Get("OPENSEARCH_PASSWORD", ""), logger)
	svc := service.New(st, es)
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(base.HTTPPort),
		Handler:           httpapi.New(svc, logger).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		logger.Info("search http listening", "addr", srv.Addr)
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
