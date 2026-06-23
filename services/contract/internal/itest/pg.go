//go:build integration

// Package itest provides shared helpers for the contract integration tests: a throwaway
// Postgres container (testcontainers) with the FULL repo migration set applied in order,
// plus a seed helper for the user/project/proposal rows a contract references. Behind the
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

// Parties holds the seeded user/project/proposal ids a contract is created against.
type Parties struct {
	ClientID     string
	FreelancerID string
	OutsiderID   string // a third user who is NOT a party to the contract
	ProjectID    string
	ProposalID   string
}

// SeedParties creates two contract parties, an unrelated outsider, and the project/proposal
// the contract references (all valid per schema FKs).
func SeedParties(ctx context.Context, t *testing.T, pool *pgxpool.Pool) Parties {
	t.Helper()
	var p Parties
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, primary_type, status) VALUES ('client@ex.com','client','active') RETURNING id`), &p.ClientID)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, primary_type, status) VALUES ('freelancer@ex.com','freelancer','active') RETURNING id`), &p.FreelancerID)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, primary_type, status) VALUES ('outsider@ex.com','client','active') RETURNING id`), &p.OutsiderID)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO projects (client_id, title, description, budget_type, status)
		 VALUES ($1,'Proj','Desc','fixed','published') RETURNING id`, p.ClientID), &p.ProjectID)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO proposals (project_id, freelancer_id, cover_letter, bid_amount_cents)
		 VALUES ($1,$2,'cover',100000) RETURNING id`, p.ProjectID, p.FreelancerID), &p.ProposalID)
	return p
}

func mustScan(t *testing.T, row interface{ Scan(...any) error }, dst *string) {
	t.Helper()
	if err := row.Scan(dst); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
}
