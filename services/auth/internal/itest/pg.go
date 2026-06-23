//go:build integration

// Package itest provides shared helpers for the integration tests: it spins up a
// throwaway Postgres container (via testcontainers), applies the FULL repo migration
// set in order, and returns a pgx pool wired to the fresh database. Everything here is
// behind the `integration` build tag so the default `go test` (no Docker) ignores it.
package itest

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// NewPostgres starts a disposable Postgres container, applies every db/migrations/*.up.sql
// in lexical (== numeric) order, and returns a connected pool. The container and pool are
// torn down automatically via t.Cleanup. The test is skipped if Docker is unavailable so
// the suite degrades gracefully on machines without a container runtime.
func NewPostgres(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("aizorix_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		// The official postgres image opens the port, runs init, then RESTARTS the server — so a
		// bare ForListeningPort can fire mid-restart and the first connection gets "connection reset
		// by peer". Also wait for the readiness log line to appear TWICE (before + after the
		// restart), the documented fix for that flake.
		testcontainers.WithWaitStrategy(
			wait.ForAll(
				wait.ForListeningPort("5432/tcp"),
				wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			).WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("skipping integration test: cannot start postgres container (is Docker running?): %v", err)
	}
	t.Cleanup(func() {
		_ = ctr.Terminate(context.Background())
	})

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := applyMigrations(ctx, pool); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}
	return pool
}

// applyMigrations reads every *.up.sql under the repo's db/migrations directory, sorts them
// by filename (so 000001 runs before 000002, ...), and executes each file's full contents.
func applyMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	dir, err := migrationsDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, name := range files {
		sql, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			return err
		}
	}
	return nil
}

// migrationsDir walks up from the working directory until it finds db/migrations, so the
// helper works regardless of which module's test invokes it.
func migrationsDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; ; {
		candidate := filepath.Join(dir, "db", "migrations")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
