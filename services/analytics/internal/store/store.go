// Package store is the analytics service's data-access layer over PostgreSQL (pgx).
//
// The analytics service owns NO tables in the core domain migrations — it is a read model.
// It DOES own a small set of pre-aggregated rollup tables that it maintains from the domain
// event stream:
//
//	// analytics owns these rollup tables via its own migration 000011_analytics.up.sql (not shown)
//	//   event_counts(day date, event_type text, count bigint, pk(day,event_type))
//	//   gmv_daily(day date, currency char(3), gross_cents bigint, fee_cents bigint,
//	//             net_cents bigint, contracts int, pk(day,currency))
//	//   funnel_daily(day date, projects_published bigint, proposals_submitted bigint,
//	//                contracts_activated bigint, pk(day))
//
// Rollup writes are idempotent-ish UPSERTs (INSERT ... ON CONFLICT DO UPDATE) so a Kafka
// consumer can apply events with at-least-once delivery. They take a pgx.Tx so the several
// rollups touched by one event commit together. Reads serve the query endpoints. The users
// table (owned by the user service) is read directly for the top-line summary headcount.
package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the pool for transactions spanning multiple rollup upserts.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// ── row types ─────────────────────────────────────────────────────────────────

// EventCountRow is one (day, event_type) bucket from event_counts.
type EventCountRow struct {
	Day       time.Time
	EventType string
	Count     int64
}

// GMVRow is one (day, currency) bucket from gmv_daily.
type GMVRow struct {
	Day        time.Time
	Currency   string
	GrossCents int64
	FeeCents   int64
	NetCents   int64
	Contracts  int
}

// FunnelRow is one day from funnel_daily.
type FunnelRow struct {
	Day                 time.Time
	ProjectsPublished   int64
	ProposalsSubmitted  int64
	ContractsActivated  int64
}

// ── rollup upserts (called by the consumer, inside one tx per event) ──────────

// BumpEventCount increments the per-day count for an event type.
func (s *Store) BumpEventCount(ctx context.Context, tx pgx.Tx, day time.Time, eventType string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO event_counts (day, event_type, count)
		VALUES ($1, $2, 1)
		ON CONFLICT (day, event_type)
		DO UPDATE SET count = event_counts.count + 1`,
		day, eventType)
	return err
}

// AddGMV adds a captured money movement (payment.captured) to the per-day, per-currency GMV
// rollup. It does NOT touch the contracts counter: gross/net come from money events while
// the contract count is bumped separately on contract.activated (see AddContractCount), so a
// contract that receives several payments is not counted multiple times.
func (s *Store) AddGMV(ctx context.Context, tx pgx.Tx, day time.Time, currency string, grossCents, feeCents, netCents int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO gmv_daily (day, currency, gross_cents, fee_cents, net_cents, contracts)
		VALUES ($1, $2, $3, $4, $5, 0)
		ON CONFLICT (day, currency)
		DO UPDATE SET
			gross_cents = gmv_daily.gross_cents + EXCLUDED.gross_cents,
			fee_cents   = gmv_daily.fee_cents   + EXCLUDED.fee_cents,
			net_cents   = gmv_daily.net_cents   + EXCLUDED.net_cents`,
		day, currency, grossCents, feeCents, netCents)
	return err
}

// AddContractCount increments the per-day, per-currency contracts counter by one. Called
// once per contract on its contract.activated event (a 1:1 lifecycle signal), keeping the
// contract count independent of how many payments/approvals the contract later generates.
func (s *Store) AddContractCount(ctx context.Context, tx pgx.Tx, day time.Time, currency string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO gmv_daily (day, currency, gross_cents, fee_cents, net_cents, contracts)
		VALUES ($1, $2, 0, 0, 0, 1)
		ON CONFLICT (day, currency)
		DO UPDATE SET contracts = gmv_daily.contracts + 1`,
		day, currency)
	return err
}

// BumpFunnel increments exactly one of the funnel_daily columns for the day. The column is
// chosen by the caller and validated there; it is interpolated into a fixed set of literals.
func (s *Store) BumpFunnel(ctx context.Context, tx pgx.Tx, day time.Time, column string) error {
	// column is one of a fixed, caller-validated allow-list, so the format is safe.
	q := `
		INSERT INTO funnel_daily (day, ` + column + `)
		VALUES ($1, 1)
		ON CONFLICT (day)
		DO UPDATE SET ` + column + ` = funnel_daily.` + column + ` + 1`
	_, err := tx.Exec(ctx, q, day)
	return err
}

// ── query reads ───────────────────────────────────────────────────────────────

// EventCounts returns event_counts rows in [from, to], optionally filtered by event type.
func (s *Store) EventCounts(ctx context.Context, from, to time.Time, eventType *string) ([]EventCountRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT day, event_type, count
		FROM event_counts
		WHERE day >= $1 AND day <= $2
		  AND ($3::text IS NULL OR event_type = $3)
		ORDER BY day, event_type`, from, to, eventType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventCountRow
	for rows.Next() {
		var r EventCountRow
		if err := rows.Scan(&r.Day, &r.EventType, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GMV returns gmv_daily rows in [from, to], optionally filtered by currency.
func (s *Store) GMV(ctx context.Context, from, to time.Time, currency *string) ([]GMVRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT day, currency, gross_cents, fee_cents, net_cents, contracts
		FROM gmv_daily
		WHERE day >= $1 AND day <= $2
		  AND ($3::text IS NULL OR currency = $3)
		ORDER BY day, currency`, from, to, currency)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GMVRow
	for rows.Next() {
		var r GMVRow
		if err := rows.Scan(&r.Day, &r.Currency, &r.GrossCents, &r.FeeCents, &r.NetCents, &r.Contracts); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Funnel returns funnel_daily rows in [from, to].
func (s *Store) Funnel(ctx context.Context, from, to time.Time) ([]FunnelRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT day, projects_published, proposals_submitted, contracts_activated
		FROM funnel_daily
		WHERE day >= $1 AND day <= $2
		ORDER BY day`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FunnelRow
	for rows.Next() {
		var r FunnelRow
		if err := rows.Scan(&r.Day, &r.ProjectsPublished, &r.ProposalsSubmitted, &r.ContractsActivated); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ── summary reads ─────────────────────────────────────────────────────────────

// GMVTotals returns the all-time gross GMV (in cents) and contract count across currencies.
// Mixing currencies in one cents total is a known simplification for the scaffold; a real
// summary would convert to a base currency.
func (s *Store) GMVTotals(ctx context.Context) (grossCents int64, contracts int64, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(gross_cents), 0), COALESCE(SUM(contracts), 0)
		FROM gmv_daily`).Scan(&grossCents, &contracts)
	return
}

// UserCount returns the total number of users. CROSS-SERVICE read: the users table is owned
// by the user service; in prod this headcount would come from that service's API.
func (s *Store) UserCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}
