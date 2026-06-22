//go:build integration

// Package itest provides shared helpers for the escrow integration tests: a throwaway
// Postgres container (testcontainers) with the FULL repo migration set applied in order,
// plus seed helpers to materialize the FK chain (user -> project -> proposal -> contract ->
// milestone -> escrow) the money flows depend on. Behind the `integration` build tag.
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
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5432/tcp").WithStartupTimeout(60*time.Second),
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

// Seed bundles the ids of a freshly-created FK chain for a fixed-price contract.
type Seed struct {
	ClientID     string
	FreelancerID string
	ProjectID    string
	ProposalID   string
	ContractID   string
	MilestoneID  string
}

// SeedFixedContract creates the user -> project -> proposal -> contract -> milestone rows
// (all valid per the schema's FKs/CHECKs) so escrow money flows have real targets to
// reference. The milestone amount is milestoneCents.
func SeedFixedContract(ctx context.Context, t *testing.T, pool *pgxpool.Pool, milestoneCents int64) Seed {
	t.Helper()
	var s Seed
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, primary_type, status) VALUES ('client@ex.com','client','active') RETURNING id`), &s.ClientID)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO users (email, primary_type, status) VALUES ('freelancer@ex.com','freelancer','active') RETURNING id`), &s.FreelancerID)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO projects (client_id, title, description, budget_type, status)
		 VALUES ($1,'Proj','Desc','fixed','published') RETURNING id`, s.ClientID), &s.ProjectID)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO proposals (project_id, freelancer_id, cover_letter, bid_amount_cents)
		 VALUES ($1,$2,'cover',$3) RETURNING id`, s.ProjectID, s.FreelancerID, milestoneCents), &s.ProposalID)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO contracts (project_id, proposal_id, client_id, freelancer_id, budget_type, total_amount_cents, status)
		 VALUES ($1,$2,$3,$4,'fixed',$5,'active') RETURNING id`,
		s.ProjectID, s.ProposalID, s.ClientID, s.FreelancerID, milestoneCents), &s.ContractID)
	mustScan(t, pool.QueryRow(ctx,
		`INSERT INTO milestones (contract_id, seq, title, amount_cents, status)
		 VALUES ($1,1,'M1',$2,'funded') RETURNING id`, s.ContractID, milestoneCents), &s.MilestoneID)
	return s
}

func mustScan(t *testing.T, row interface{ Scan(...any) error }, dst *string) {
	t.Helper()
	if err := row.Scan(dst); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
}
