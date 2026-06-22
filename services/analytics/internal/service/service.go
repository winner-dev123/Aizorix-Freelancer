// Package service holds the analytics business logic: it maintains pre-aggregated rollups
// from the domain event stream and answers rollup queries.
//
// IngestEvent is the write path. A Kafka consumer subscribed to the platform's domain-event
// topics calls IngestEvent for each message; the same method is also exposed over HTTP
// (POST /v1/analytics/internal/ingest) for testing and manual backfill. Every event bumps
// event_counts; money events additionally fold into gmv_daily; lifecycle events additionally
// bump funnel_daily. All rollups touched by one event are written in ONE transaction so a
// retried delivery never leaves the buckets half-updated.
package service

import (
	"context"
	"time"

	"github.com/aizorix/platform/analytics/internal/store"
)

type Service struct{ store *store.Store }

func New(st *store.Store) *Service { return &Service{store: st} }

// gmvEvents are the event types that move money into the GMV rollup. ONLY payment.captured
// counts toward GMV: it is the single authoritative cash-in event. milestone.approved was
// previously also counted here, which double-counted gross/net (~2x) for fixed-price work
// that is both captured and approved, so it is deliberately excluded.
var gmvEvents = map[string]bool{
	"payment.captured": true,
}

// contractCountEvent is the 1:1 lifecycle event used to count contracts in the GMV rollup.
// A contract is counted exactly once when it activates — NOT on every money event, which
// previously inflated the contract count by the number of payments/approvals per contract.
const contractCountEvent = "contract.activated"

// funnelColumns maps a lifecycle event type to the funnel_daily column it advances.
var funnelColumns = map[string]string{
	"project.published":   "projects_published",
	"proposal.submitted":  "proposals_submitted",
	"contract.activated":  "contracts_activated",
}

// platformFeeBps is the assumed marketplace take rate used to split gross into fee/net when
// an event does not carry an explicit breakdown. 10% = 1000 bps.
const platformFeeBps = 1000

// IngestEvent applies one domain event to the rollups. occurredAt is truncated to its UTC
// calendar day for bucketing. amountCents/currency are only consulted for money events.
func (s *Service) IngestEvent(ctx context.Context, eventType string, occurredAt time.Time, amountCents int64, currency string) error {
	day := occurredAt.UTC().Truncate(24 * time.Hour)
	if currency == "" {
		currency = "USD"
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err := s.store.BumpEventCount(ctx, tx, day, eventType); err != nil {
		return err
	}
	if gmvEvents[eventType] {
		fee := amountCents * platformFeeBps / 10000
		net := amountCents - fee
		if err := s.store.AddGMV(ctx, tx, day, currency, amountCents, fee, net); err != nil {
			return err
		}
	}
	if eventType == contractCountEvent {
		// Count the contract exactly once, on its 1:1 activation event.
		if err := s.store.AddContractCount(ctx, tx, day, currency); err != nil {
			return err
		}
	}
	if col, ok := funnelColumns[eventType]; ok {
		if err := s.store.BumpFunnel(ctx, tx, day, col); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ── query results ─────────────────────────────────────────────────────────────

// GMVResult bundles the per-bucket GMV rows with a roll-up total across them.
type GMVResult struct {
	Rows  []store.GMVRow
	Total GMVTotal
}

// GMVTotal is the summed GMV over the returned rows.
type GMVTotal struct {
	GrossCents int64
	FeeCents   int64
	NetCents   int64
	Contracts  int64
}

// FunnelResult bundles the per-day funnel rows with computed conversion rates over the
// summed totals of the range.
type FunnelResult struct {
	Rows                  []store.FunnelRow
	ProjectsPublished     int64
	ProposalsSubmitted    int64
	ContractsActivated    int64
	ProposalRate          float64 // proposals / projects
	ActivationRate        float64 // contracts / proposals
}

// Summary is the top-line dashboard snapshot.
type Summary struct {
	TotalGMVCents   int64
	TotalContracts  int64
	TotalUsers      int64
}

// ── query operations ──────────────────────────────────────────────────────────

// EventCounts returns event counts in [from, to], optionally filtered by event type.
func (s *Service) EventCounts(ctx context.Context, from, to time.Time, eventType *string) ([]store.EventCountRow, error) {
	return s.store.EventCounts(ctx, from, to, eventType)
}

// GMV returns per-(day,currency) GMV in [from, to] plus a summed total over the rows.
func (s *Service) GMV(ctx context.Context, from, to time.Time, currency *string) (*GMVResult, error) {
	rows, err := s.store.GMV(ctx, from, to, currency)
	if err != nil {
		return nil, err
	}
	var t GMVTotal
	for i := range rows {
		t.GrossCents += rows[i].GrossCents
		t.FeeCents += rows[i].FeeCents
		t.NetCents += rows[i].NetCents
		t.Contracts += int64(rows[i].Contracts)
	}
	return &GMVResult{Rows: rows, Total: t}, nil
}

// Funnel returns per-day funnel rows in [from, to] plus range totals and conversion rates.
func (s *Service) Funnel(ctx context.Context, from, to time.Time) (*FunnelResult, error) {
	rows, err := s.store.Funnel(ctx, from, to)
	if err != nil {
		return nil, err
	}
	res := &FunnelResult{Rows: rows}
	for i := range rows {
		res.ProjectsPublished += rows[i].ProjectsPublished
		res.ProposalsSubmitted += rows[i].ProposalsSubmitted
		res.ContractsActivated += rows[i].ContractsActivated
	}
	res.ProposalRate = ratio(res.ProposalsSubmitted, res.ProjectsPublished)
	res.ActivationRate = ratio(res.ContractsActivated, res.ProposalsSubmitted)
	return res, nil
}

// Summary returns top-line totals for the dashboard.
func (s *Service) Summary(ctx context.Context) (*Summary, error) {
	gross, contracts, err := s.store.GMVTotals(ctx)
	if err != nil {
		return nil, err
	}
	users, err := s.store.UserCount(ctx)
	if err != nil {
		return nil, err
	}
	return &Summary{TotalGMVCents: gross, TotalContracts: contracts, TotalUsers: users}, nil
}

func ratio(num, denom int64) float64 {
	if denom == 0 {
		return 0
	}
	return float64(num) / float64(denom)
}
