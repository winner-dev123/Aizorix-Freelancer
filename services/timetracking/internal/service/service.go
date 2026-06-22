// Package service implements time-tracking business logic: opening/closing work sessions,
// ingesting activity slices (server re-validates the device's activity %), accruing the
// weekly timesheet, and emitting events for billing and fraud.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/aizorix/platform/pkg/outbox"
	"github.com/aizorix/platform/pkg/rbac"
	"github.com/aizorix/platform/timetracking/internal/activity"
	"github.com/aizorix/platform/timetracking/internal/contractparties"
	"github.com/aizorix/platform/timetracking/internal/store"
)

var (
	// ErrNotFound re-exports the store sentinel so the transport can map a missing
	// session to 404.
	ErrNotFound = store.ErrNotFound
	// ErrForbidden re-exports the rbac sentinel so the transport maps a caller that is
	// not the session's freelancer to 403.
	ErrForbidden = rbac.ErrForbidden
)

// partiesClient resolves a contract's parties from the contract service. It authorizes that
// the caller opening a session is the contract's freelancer (see contractparties.Client).
type partiesClient interface {
	Get(ctx context.Context, contractID string) (contractparties.Parties, error)
}

type Service struct {
	store   *store.Store
	params  activity.Params
	parties partiesClient
}

func New(st *store.Store, parties partiesClient) *Service {
	return &Service{store: st, params: activity.DefaultParams(), parties: parties}
}

type StartResult struct {
	SessionID       string
	BillingWeek     string
	CaptureInterval int
}

func (s *Service) StartSession(ctx context.Context, contractID, freelancerID, deviceID, tz string, startedAt time.Time) (*StartResult, error) {
	// Authorize against the contract service BEFORE opening a billable session: the caller must
	// be the contract's freelancer and the contract must be active. The lookup FAILS CLOSED —
	// any error (contract missing, contract service unreachable) denies (rbac.ErrForbidden), so
	// a session is never opened on an unverifiable or client-spoofed contract_id.
	if contractID == "" {
		return nil, rbac.ErrForbidden
	}
	p, err := s.parties.Get(ctx, contractID)
	if err != nil {
		return nil, rbac.ErrForbidden
	}
	if freelancerID == "" || freelancerID != p.FreelancerID || p.Status != "active" {
		return nil, rbac.ErrForbidden
	}
	week := isoWeek(startedAt)
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	id, err := s.store.OpenSession(ctx, tx, contractID, freelancerID, deviceID, tz, week, startedAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	// Capture interval is server-controlled (default 15 min) so the platform — not the
	// client — decides cadence; the tracker may jitter within a tolerance.
	return &StartResult{SessionID: id, BillingWeek: week, CaptureInterval: 900}, nil
}

// IngestSlice is the per-slice ingestion. The tracker sends raw samples; the SERVER
// recomputes the activity % (never trust the client) and stores the slice, flagging
// suspicious patterns for the fraud service.
type IncomingSlice struct {
	SessionID      string
	ContractID     string
	Start, End     time.Time
	Samples        []activity.Sample
	ActiveApp      string
	ActiveAppTitle string
	BrowserHost    string
	ScreenshotID   string
	IsManual       bool
}

// requireSessionOwner returns rbac.ErrForbidden unless caller owns the session (is its
// freelancer). Shared by the slice-ingest and stop paths so the ownership guard stays
// consistent; surfaces store.ErrNotFound for an unknown session.
func (s *Service) requireSessionOwner(ctx context.Context, sessionID, caller string) error {
	freelancerID, err := s.store.SessionFreelancer(ctx, sessionID)
	if err != nil {
		return err
	}
	return rbac.RequireOneOf(caller, freelancerID)
}

func (s *Service) IngestSlices(ctx context.Context, sessionID, contractID, caller string, slices []IncomingSlice) (int, error) {
	if err := s.requireSessionOwner(ctx, sessionID, caller); err != nil {
		return 0, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	accepted := 0
	for _, in := range slices {
		res := s.params.Compute(in.Samples, in.Start, in.End)
		kb, mouse := sum(in.Samples)
		row := store.Slice{
			SessionID: in.SessionID, ContractID: in.ContractID, Start: in.Start, End: in.End,
			KeyboardEvents: kb, MouseEvents: mouse, ActiveSeconds: res.ActiveSeconds,
			ActivityPct: res.ActivityPct, ActiveApp: in.ActiveApp, ActiveAppTitle: in.ActiveAppTitle,
			BrowserHost: in.BrowserHost, ScreenshotID: in.ScreenshotID,
			IsIdle: res.ActiveSeconds == 0, IsManual: in.IsManual, Flagged: res.Suspicious,
			FlagReason: joinReasons(res.SuspectReasons),
		}
		if err := s.store.UpsertSlice(ctx, tx, row); err != nil {
			return accepted, err
		}
		if res.Suspicious {
			// Non-blocking signal to the fraud service.
			_ = outbox.Enqueue(ctx, tx, outbox.Event{
				AggregateType: "contract", AggregateID: in.ContractID, EventType: "activity.suspicious",
				Topic: "worksession.events", PartitionKey: in.ContractID,
				Payload: map[string]any{
					"contract_id": in.ContractID, "session_id": in.SessionID,
					"slice_start": in.Start, "reasons": res.SuspectReasons, "activity_pct": res.ActivityPct,
				},
			})
		}
		accepted++
	}
	if err := tx.Commit(ctx); err != nil {
		return accepted, err
	}
	return accepted, nil
}

type CloseResult struct{ ActiveSeconds, IdleSeconds, AvgActivityPct int }

// StopSession closes the session, accrues the weekly timesheet, and emits worksession.closed.
// Only the session's own freelancer may stop it (rbac.ErrForbidden otherwise).
func (s *Service) StopSession(ctx context.Context, sessionID, caller, memo string, endedAt time.Time) (*CloseResult, error) {
	if err := s.requireSessionOwner(ctx, sessionID, caller); err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	active, idle, avg, contractID, week, err := s.store.CloseSession(ctx, tx, sessionID, endedAt, memo)
	if err != nil {
		return nil, err
	}
	terms, err := s.store.ContractTerms(ctx, contractID)
	if err != nil {
		return nil, err
	}
	if err := s.store.AccrueTimesheet(ctx, tx, contractID, week, active, avg, terms.HourlyRateCents); err != nil {
		return nil, err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "contract", AggregateID: contractID, EventType: "worksession.closed",
		Topic: "worksession.events", PartitionKey: contractID,
		Payload: map[string]any{
			"session_id": sessionID, "contract_id": contractID, "billing_week": week,
			"billable_seconds": active, "avg_activity_pct": avg,
		},
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &CloseResult{ActiveSeconds: active, IdleSeconds: idle, AvgActivityPct: avg}, nil
}

func (s *Service) GetTimesheet(ctx context.Context, contractID, week string) (store.TimesheetView, error) {
	if week == "" {
		week = isoWeek(time.Now())
	}
	return s.store.GetTimesheet(ctx, contractID, week)
}

func sum(samples []activity.Sample) (kb, mouse int) {
	for _, s := range samples {
		kb += s.KeyboardCount
		mouse += s.MouseCount
	}
	return
}

func isoWeek(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

func joinReasons(rs []string) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += ","
		}
		out += r
	}
	return out
}
