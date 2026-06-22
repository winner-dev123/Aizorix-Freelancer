// Package service holds the admin business logic. Every privileged operation writes BOTH
// an admin_actions row (what an admin did) AND an audit_logs row (the immutable account-
// ability trail) in ONE transaction with the cross-service state change it effects, and —
// for outward-facing transitions — enqueues an outbox event in that same transaction.
//
// Authorization is enforced one layer up, in the HTTP transport: each handler calls
// principal.Require("admin.<action>") before invoking these methods, so by the time a
// method here runs the caller is already known to be a permitted admin. The adminID passed
// in is principal.UserID.
package service

import (
	"context"
	"errors"

	"github.com/aizorix/platform/admin/internal/store"
	"github.com/aizorix/platform/pkg/outbox"
)

var (
	// ErrNotFound re-exports the store sentinel for transport mapping.
	ErrNotFound = store.ErrNotFound
	// ErrInvalidResolution is returned when a dispute resolution value is not allowed.
	ErrInvalidResolution = errors.New("admin: invalid dispute resolution")
)

// validResolutions are the terminal dispute_status values an admin may set.
var validResolutions = map[string]bool{
	"resolved_client":     true,
	"resolved_freelancer": true,
	"split":               true,
}

type Service struct{ store *store.Store }

func New(st *store.Store) *Service { return &Service{store: st} }

// AuditCtx carries the request-scoped accountability metadata captured by the transport
// (client IP, user agent) so it can be threaded into every audit_logs row.
type AuditCtx struct {
	IP        *string
	UserAgent *string
}

// ── user moderation ───────────────────────────────────────────────────────────

// SuspendUser sets a user's status to 'suspended', records the admin action and an audit
// row, and emits user.suspended on the admin.events topic — all in one transaction.
func (s *Service) SuspendUser(ctx context.Context, adminID, targetUserID, reason string, ac AuditCtx) error {
	return s.moderateUser(ctx, adminID, targetUserID, reason, ac,
		"suspended", "user.suspend", "user.suspended", true)
}

// ReinstateUser sets a user's status back to 'active' and records the action + audit row.
// No outbox event is emitted (reinstatement is internal accountability only).
func (s *Service) ReinstateUser(ctx context.Context, adminID, targetUserID, reason string, ac AuditCtx) error {
	return s.moderateUser(ctx, adminID, targetUserID, reason, ac,
		"active", "user.reinstate", "", false)
}

// moderateUser is the shared transaction for suspend/reinstate.
func (s *Service) moderateUser(ctx context.Context, adminID, targetUserID, reason string, ac AuditCtx, newStatus, action, eventType string, emit bool) error {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	exists, err := s.store.UserExists(ctx, tx, targetUserID)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := s.store.SetUserStatus(ctx, tx, targetUserID, newStatus); err != nil {
		return err
	}
	reasonPtr := nilIfEmpty(reason)
	if _, err := s.store.InsertAdminAction(ctx, tx, store.AdminAction{
		AdminID: adminID, Action: action, TargetType: "user", TargetID: targetUserID, Reason: reasonPtr,
	}); err != nil {
		return err
	}
	target := targetUserID
	if err := s.store.InsertAuditLog(ctx, tx, store.AuditLog{
		ActorID: &adminID, ActorType: "user", Action: action,
		ResourceType: "user", ResourceID: &target, IP: ac.IP, UserAgent: ac.UserAgent,
	}); err != nil {
		return err
	}
	if emit {
		if err := outbox.Enqueue(ctx, tx, outbox.Event{
			AggregateType: "user", AggregateID: targetUserID, EventType: eventType,
			Topic: "admin.events", PartitionKey: targetUserID,
			Payload: map[string]any{
				"user_id": targetUserID, "admin_id": adminID, "reason": reason,
			},
		}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ── dispute resolution ─────────────────────────────────────────────────────────

// ResolveDispute sets a dispute to a terminal resolution, records the action + audit row,
// and emits dispute.resolved on admin.events — all atomically.
func (s *Service) ResolveDispute(ctx context.Context, adminID, disputeID, resolution, note string, ac AuditCtx) error {
	if !validResolutions[resolution] {
		return ErrInvalidResolution
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	notePtr := nilIfEmpty(note)
	ok, err := s.store.ResolveDispute(ctx, tx, disputeID, resolution, notePtr, adminID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	if _, err := s.store.InsertAdminAction(ctx, tx, store.AdminAction{
		AdminID: adminID, Action: "dispute.resolve", TargetType: "dispute", TargetID: disputeID, Reason: notePtr,
	}); err != nil {
		return err
	}
	target := disputeID
	if err := s.store.InsertAuditLog(ctx, tx, store.AuditLog{
		ActorID: &adminID, ActorType: "user", Action: "dispute.resolve",
		ResourceType: "dispute", ResourceID: &target, IP: ac.IP, UserAgent: ac.UserAgent,
	}); err != nil {
		return err
	}
	if err := outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "dispute", AggregateID: disputeID, EventType: "dispute.resolved",
		Topic: "admin.events", PartitionKey: disputeID,
		Payload: map[string]any{
			"dispute_id": disputeID, "resolution": resolution,
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ── screenshot audit ───────────────────────────────────────────────────────────

// ScreenshotAuditFilter narrows ListScreenshotsForAudit.
type ScreenshotAuditFilter struct {
	UserID *string
	Status *string
	Limit  int
}

// ListScreenshotsForAudit returns screenshots matching the filter. Because reviewing a
// worker's screenshots is itself a sensitive action, each call also writes ONE audit_logs
// row recording the access (in the same transaction as nothing else, just the audit row).
func (s *Service) ListScreenshotsForAudit(ctx context.Context, adminID string, f ScreenshotAuditFilter, ac AuditCtx) ([]store.Screenshot, error) {
	limit := clampLimit(f.Limit)
	out, err := s.store.ListScreenshots(ctx, store.ScreenshotFilter{
		UserID: f.UserID, Status: f.Status, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err := s.store.InsertAuditLog(ctx, tx, store.AuditLog{
		ActorID: &adminID, ActorType: "user", Action: "screenshot.audit_list",
		ResourceType: "screenshot", IP: ac.IP, UserAgent: ac.UserAgent,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// ── audit reads ────────────────────────────────────────────────────────────────

// ListAdminActions returns recent admin_actions.
func (s *Service) ListAdminActions(ctx context.Context, limit int) ([]store.AdminAction, error) {
	return s.store.ListAdminActions(ctx, clampLimit(limit))
}

// AuditLogFilter narrows ListAuditLogs.
type AuditLogFilter struct {
	ActorID *string
	Action  *string
	Limit   int
}

// ListAuditLogs returns recent audit_logs matching the filter.
func (s *Service) ListAuditLogs(ctx context.Context, f AuditLogFilter) ([]store.AuditLog, error) {
	return s.store.ListAuditLogs(ctx, store.AuditLogFilter{
		ActorID: f.ActorID, Action: f.Action, Limit: clampLimit(f.Limit),
	})
}

// ── helpers ─────────────────────────────────────────────────────────────────────

// AuditMeta builds the request-scoped audit context for a method call.
func AuditMeta(ip, userAgent *string) AuditCtx { return AuditCtx{IP: ip, UserAgent: userAgent} }

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func clampLimit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 500 {
		return 500
	}
	return n
}
