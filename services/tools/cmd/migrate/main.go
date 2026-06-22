// Command migrate applies db/migrations/*.up.sql in order against DATABASE_URL. It exists
// because the `migrate` CLI isn't always installed; it tracks applied versions in a
// schema_migrations ledger so re-runs are idempotent. Uses the simple query protocol so a
// whole migration file (BEGIN/COMMIT + function bodies with ';') runs as one unit.
//
//	cd services/tools && go run ./cmd/migrate              # apply up
//	MIGRATIONS_DIR=../../db/migrations go run ./cmd/migrate
package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func main() {
	dir := getenv("MIGRATIONS_DIR", "../../db/migrations")
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		fatal("DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		fatal("connect: " + err.Error())
	}
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		fatal("create ledger: " + err.Error())
	}

	entries, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil || len(entries) == 0 {
		fatal("no .up.sql files found in " + dir)
	}
	sort.Strings(entries)

	applied := 0
	for _, path := range entries {
		version := strings.TrimSuffix(filepath.Base(path), ".up.sql")

		var exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&exists); err != nil {
			fatal("check " + version + ": " + err.Error())
		}
		if exists {
			info("skip   " + version + " (already applied)")
			continue
		}

		sqlBytes, err := os.ReadFile(path)
		if err != nil {
			fatal("read " + path + ": " + err.Error())
		}
		// Simple protocol: execute the entire file (multi-statement) atomically.
		mrr := conn.PgConn().Exec(ctx, string(sqlBytes))
		if _, err := mrr.ReadAll(); err != nil {
			fatal("apply " + version + ": " + err.Error())
		}
		if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			fatal("record " + version + ": " + err.Error())
		}
		info("apply  " + version)
		applied++
	}
	info("done: applied " + itoa(applied) + " migration(s), " + itoa(len(entries)) + " total")
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func info(s string)  { os.Stdout.WriteString(s + "\n") }
func fatal(s string) { os.Stderr.WriteString("migrate: " + s + "\n"); os.Exit(1) }
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
