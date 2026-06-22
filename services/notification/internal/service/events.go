package service

import (
	"context"
	"encoding/json"
	"fmt"
)

// ResolveEventType derives a domain event type from a Kafka message's topic and JSON payload.
//
// The transactional outbox stores event_type in its own table column but does NOT propagate it
// into the published message (only the JSON payload and an event-id header travel on the wire),
// so consumers must recover the type here. If a payload ever carries an explicit "type" (or
// "event_type") field we trust it; otherwise we infer the type from the topic, disambiguating
// the multiple event types that share a topic by the distinctive keys their payloads carry.
func ResolveEventType(topic string, payload []byte) string {
	var m map[string]any
	_ = json.Unmarshal(payload, &m)
	if t := stringField(m, "type", "event_type"); t != "" {
		return t
	}
	return inferEventType(topic, m)
}

// inferEventType maps (topic, payload-shape) to a concrete event type. Topics that carry a
// single event type map directly; topics that multiplex several types are disambiguated by the
// presence of identifying payload keys (e.g. contract.events: dispute_id ⇒ contract.disputed,
// milestone_id ⇒ milestone.approved, else contract.activated).
func inferEventType(topic string, m map[string]any) string {
	switch topic {
	case "payment.events":
		return "payment.captured"
	case "proposal.events":
		// proposal.events carries submitted/withdrawn/shortlisted; only submitted ships a
		// bid_amount_cents, the others key off the proposal lifecycle. Default to submitted.
		if _, ok := m["bid_amount_cents"]; ok {
			return "proposal.submitted"
		}
		return "proposal.submitted"
	case "contract.events":
		if _, ok := m["dispute_id"]; ok {
			return "contract.disputed"
		}
		if _, ok := m["milestone_id"]; ok {
			return "milestone.approved"
		}
		return "contract.activated"
	case "worksession.events":
		// activity.suspicious carries no session id; worksession.closed identifies a session.
		if _, ok := m["session_id"]; ok {
			return "worksession.closed"
		}
		if _, ok := m["worksession_id"]; ok {
			return "worksession.closed"
		}
		return "worksession.closed"
	case "review.events":
		return "review.published"
	case "user.events":
		if _, ok := m["kyc_status"]; ok {
			return "user.kyc_updated"
		}
		return "profile.updated"
	case "fraud.events":
		if _, ok := m["resolution"]; ok {
			return "fraud.case_resolved"
		}
		return "fraud.case_opened"
	case "project.events":
		if _, ok := m["closed_at"]; ok {
			return "project.closed"
		}
		return "project.published"
	case "screenshot.ingested":
		return "screenshot.ingested"
	default:
		return topic
	}
}

// recipient resolves the user that a notification for this event should target, by probing the
// payload keys producers populate (client/freelancer/payer/subject ids and generic user_id).
func recipient(m map[string]any) string {
	for _, k := range []string{"user_id", "recipient_id", "freelancer_id", "client_id", "payer_id", "subject_id"} {
		if v := stringField(m, k); v != "" {
			return v
		}
	}
	return ""
}

// notificationCopy maps an event type to the human-facing title and body of the notification.
// Unknown types fall back to a generic title so the fan-out still records something useful.
func notificationCopy(eventType string, m map[string]any) (title string, body *string) {
	switch eventType {
	case "proposal.submitted":
		title = "New proposal received"
		body = ptr("A freelancer submitted a proposal on your project.")
	case "proposal.shortlisted":
		title = "You were shortlisted"
		body = ptr("Your proposal was shortlisted by the client.")
	case "proposal.withdrawn":
		title = "Proposal withdrawn"
		body = ptr("A proposal on your project was withdrawn.")
	case "contract.activated":
		title = "Contract activated"
		body = ptr("Your contract is now active. Work can begin.")
	case "milestone.approved":
		title = "Milestone approved"
		body = ptr("A milestone was approved and is ready for release.")
	case "contract.disputed":
		title = "Contract disputed"
		body = ptr("A dispute was raised on your contract.")
	case "payment.captured":
		title = "Payment received"
		if amt := centsField(m, "amount_cents"); amt != "" {
			body = ptr(fmt.Sprintf("A payment of %s was captured for your contract.", amt))
		} else {
			body = ptr("A payment was captured for your contract.")
		}
	case "worksession.closed":
		title = "Work session logged"
		body = ptr("A tracked work session was closed on your contract.")
	case "activity.suspicious":
		title = "Suspicious activity detected"
		body = ptr("Unusual activity was detected on a tracked work session.")
	case "review.published":
		title = "New review"
		body = ptr("You received a new review.")
	case "profile.updated":
		title = "Profile updated"
		body = ptr("Your profile was updated.")
	case "user.kyc_updated":
		title = "Identity verification update"
		body = ptr("Your identity verification status changed.")
	case "fraud.case_opened":
		title = "Account under review"
		body = ptr("A trust & safety review was opened on your account.")
	case "fraud.case_resolved":
		title = "Account review resolved"
		body = ptr("A trust & safety review on your account was resolved.")
	default:
		title = eventType
		body = nil
	}
	return title, body
}

// HandleRawEvent is the Kafka-facing fan-out entry point: it parses the raw event payload,
// resolves the recipient and notification copy for eventType, and delegates to HandleEvent
// (which writes a notifications row + a delivery_attempts row per enabled channel in one tx).
//
// It is idempotent at the consumer boundary via processed_events dedupe, and tolerant of
// unknown/recipient-less events: those are a no-op that still lets the offset commit, so the
// stream never blocks on an event this service has no notification for.
func (s *Service) HandleRawEvent(ctx context.Context, eventType string, payload []byte) error {
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		// Malformed payload: nothing actionable. Don't block the partition on poison data.
		return nil
	}
	userID := recipient(m)
	if userID == "" {
		// No addressable recipient on this event (e.g. a system-wide signal) — no-op + commit.
		return nil
	}
	title, body := notificationCopy(eventType, m)
	_, err := s.HandleEvent(ctx, userID, eventType, title, body, m)
	return err
}

// ── small helpers ─────────────────────────────────────────────────────────────

func ptr(s string) *string { return &s }

// stringField returns the first key in keys whose value is a non-empty string.
func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// centsField renders an integer "*_cents" payload value as a dollar string for display.
func centsField(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	f, ok := v.(float64) // JSON numbers decode to float64
	if !ok {
		return ""
	}
	return fmt.Sprintf("$%.2f", f/100)
}
