//go:build integration

// Package itest provides shared helpers for the messaging integration tests: a throwaway
// Postgres container (testcontainers) with the FULL repo migration set applied in order,
// plus a seed helper for the users a conversation's participants reference. Behind the
// `integration` build tag.
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
// in lexical order, and returns a connected pool (torn down via t.Cleanup). Skips the test
// when Docker is unavailable.
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
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })

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

// Users holds three seeded user ids: two conversation participants and an outsider who is
// NOT a participant in any conversation under test.
type Users struct {
	Alice   string // participant + conversation creator
	Bob     string // participant
	Mallory string // outsider — not a participant
}

// SeedUsers inserts the three users a participant-authorization test needs.
func SeedUsers(ctx context.Context, t *testing.T, pool *pgxpool.Pool) Users {
	t.Helper()
	var u Users
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, primary_type, status) VALUES ('alice@ex.com','freelancer','active') RETURNING id`), &u.Alice)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, primary_type, status) VALUES ('bob@ex.com','client','active') RETURNING id`), &u.Bob)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, primary_type, status) VALUES ('mallory@ex.com','client','active') RETURNING id`), &u.Mallory)
	return u
}

func mustScan(t *testing.T, row interface{ Scan(...any) error }, dst *string) {
	t.Helper()
	if err := row.Scan(dst); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
}
