// Package store is the time-tracking data layer: work sessions, time slices, and the
// weekly timesheet rollup that drives hourly billing.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("store: not found")

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) OpenSession(ctx context.Context, tx pgx.Tx, contractID, freelancerID, deviceID, tz, billingWeek string, startedAt time.Time) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO work_sessions (contract_id, freelancer_id, device_id, status, started_at, timezone, billing_week)
		VALUES ($1,$2,NULLIF($3,'')::uuid,'open',$4,$5,$6)
		RETURNING id`, contractID, freelancerID, deviceID, startedAt, tz, billingWeek).Scan(&id)
	return id, err
}

// Slice carries one ingested billing unit.
type Slice struct {
	SessionID      string
	ContractID     string
	Start, End     time.Time
	KeyboardEvents int
	MouseEvents    int
	ActiveSeconds  int
	ActivityPct    int
	ActiveApp      string
	ActiveAppTitle string
	BrowserHost    string
	ScreenshotID   string
	IsIdle         bool
	IsManual       bool
	Flagged        bool
	FlagReason     string
}

// UpsertSlice is idempotent on (session_id, slice_start) so retried/offline batches are safe.
func (s *Store) UpsertSlice(ctx context.Context, tx pgx.Tx, sl Slice) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO time_slices
		  (session_id, contract_id, slice_start, slice_end, keyboard_events, mouse_events,
		   active_seconds, activity_pct, active_app, active_app_title, browser_url_host,
		   screenshot_id, flagged, flag_reason, is_manual)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,'')::uuid,$13,$14,$15)
		ON CONFLICT (session_id, slice_start) DO UPDATE SET
		  keyboard_events  = EXCLUDED.keyboard_events,
		  mouse_events     = EXCLUDED.mouse_events,
		  active_seconds   = EXCLUDED.active_seconds,
		  activity_pct     = EXCLUDED.activity_pct,
		  active_app       = EXCLUDED.active_app,
		  active_app_title = EXCLUDED.active_app_title,
		  browser_url_host = EXCLUDED.browser_url_host,
		  screenshot_id    = EXCLUDED.screenshot_id,
		  flagged          = EXCLUDED.flagged,
		  flag_reason      = EXCLUDED.flag_reason,
		  is_manual        = EXCLUDED.is_manual`,
		sl.SessionID, sl.ContractID, sl.Start, sl.End, sl.KeyboardEvents, sl.MouseEvents,
		sl.ActiveSeconds, sl.ActivityPct, sl.ActiveApp, sl.ActiveAppTitle, sl.BrowserHost,
		sl.ScreenshotID, sl.Flagged, sl.FlagReason, sl.IsManual)
	return err
}

// CloseSession aggregates the slices and stamps the session summary.
func (s *Store) CloseSession(ctx context.Context, tx pgx.Tx, sessionID string, endedAt time.Time, memo string) (active, idle, avgPct int, contractID, billingWeek string, err error) {
	err = tx.QueryRow(ctx, `
		SELECT coalesce(sum(active_seconds),0),
		       coalesce(sum(GREATEST(extract(epoch FROM (slice_end-slice_start))::int - active_seconds,0)),0),
		       coalesce(round(avg(activity_pct)),0)::int
		FROM time_slices WHERE session_id = $1`, sessionID).Scan(&active, &idle, &avgPct)
	if err != nil {
		return
	}
	err = tx.QueryRow(ctx, `
		UPDATE work_sessions
		SET status='closed', ended_at=$2, memo=$3, active_seconds=$4, idle_seconds=$5,
		    billed_seconds=$4, avg_activity_pct=$6
		WHERE id=$1
		RETURNING contract_id, billing_week`, sessionID, endedAt, memo, active, idle, avgPct).
		Scan(&contractID, &billingWeek)
	return
}

// AccrueTimesheet adds billable seconds to the weekly rollup (idempotent upsert by week).
func (s *Store) AccrueTimesheet(ctx context.Context, tx pgx.Tx, contractID, week string, billableSeconds, avgPct int, rateCents int64) error {
	amount := rateCents * int64(billableSeconds) / 3600
	_, err := tx.Exec(ctx, `
		INSERT INTO timesheets (contract_id, billing_week, total_seconds, billable_seconds, amount_cents, avg_activity_pct, status)
		VALUES ($1,$2,$3,$3,$4,$5,'accumulating')
		ON CONFLICT (contract_id, billing_week) DO UPDATE SET
		  total_seconds   = timesheets.total_seconds + EXCLUDED.total_seconds,
		  billable_seconds= timesheets.billable_seconds + EXCLUDED.billable_seconds,
		  amount_cents    = timesheets.amount_cents + EXCLUDED.amount_cents,
		  avg_activity_pct= EXCLUDED.avg_activity_pct,
		  updated_at      = now()`,
		contractID, week, billableSeconds, amount, avgPct)
	return err
}

type ContractTerms struct {
	HourlyRateCents int64
	WeeklyHourLimit int
}

func (s *Store) ContractTerms(ctx context.Context, contractID string) (ContractTerms, error) {
	var t ContractTerms
	var rate *int64
	var limit *int
	err := s.pool.QueryRow(ctx, `
		SELECT hourly_rate_cents, weekly_hour_limit FROM contracts WHERE id=$1`, contractID).
		Scan(&rate, &limit)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if rate != nil {
		t.HourlyRateCents = *rate
	}
	if limit != nil {
		t.WeeklyHourLimit = *limit
	}
	return t, err
}

type TimesheetView struct {
	Week            string
	BillableSeconds int
	AmountCents     int64
	Status          string
	AvgActivityPct  int
}

func (s *Store) GetTimesheet(ctx context.Context, contractID, week string) (TimesheetView, error) {
	var v TimesheetView
	err := s.pool.QueryRow(ctx, `
		SELECT billing_week, billable_seconds, amount_cents, status, coalesce(avg_activity_pct,0)
		FROM timesheets WHERE contract_id=$1 AND billing_week=$2`, contractID, week).
		Scan(&v.Week, &v.BillableSeconds, &v.AmountCents, &v.Status, &v.AvgActivityPct)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}
