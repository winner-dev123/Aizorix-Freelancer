// Command consumer runs the analytics ingestion consumer — the analytics sink. It subscribes to
// every domain-event topic on the bus and folds each event into the pre-aggregated rollups via
// service.Service.IngestEvent (event_counts always; gmv_daily for payment.captured; the
// contracts counter for contract.activated; funnel_daily for the lifecycle events).
//
// Delivery is at-least-once; .WithDedupe(pool) records handled event ids in processed_events so
// a replayed event is skipped and does NOT re-increment a rollup. The rollup upserts are also
// idempotent-ish, but dedupe is what keeps counts exact across redelivery.
//
// IMPORTANT (no double counting): GMV is credited ONLY for payment.captured and the contract
// count ONLY for contract.activated. The event-type resolver below disambiguates the several
// event types that share a topic (e.g. contract.events also carries milestone.approved, which
// has an amount but must NOT move GMV), so the prior double-counting fix is preserved.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aizorix/platform/analytics/internal/service"
	"github.com/aizorix/platform/analytics/internal/store"
	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/kafka"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/pg"
)

// consumerGroup is overridable via CONSUMER_GROUP (handy for replaying from the earliest
// offset under a fresh group id).
var consumerGroup = func() string {
	if v := os.Getenv("CONSUMER_GROUP"); v != "" {
		return v
	}
	return "analytics-consumer"
}()

// topics is every domain-event stream — analytics is the platform-wide sink.
var topics = []string{
	"user.events",
	"project.events",
	"proposal.events",
	"contract.events",
	"worksession.events",
	"screenshot.ingested",
	"payment.events",
	"escrow.events",
	"review.events",
	"fraud.events",
}

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "analytics-consumer", base.Environment)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pg.Connect(ctx, base.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	svc := service.New(store.New(pool))

	brokers := strings.Split(base.KafkaBrokers, ",")
	consumer := kafka.NewConsumer(brokers, consumerGroup, topics).WithDedupe(pool)

	handler := func(ctx context.Context, m kafka.Message) error {
		var payload map[string]any
		if err := json.Unmarshal(m.Value, &payload); err != nil {
			return nil // poison payload; commit and move on
		}
		// Prefer the self-describing bus header (set by the relay); fall back to inference.
		eventType := m.EventType
		if eventType == "" {
			eventType = resolveEventType(m.Topic, payload)
		}
		amount := centsField(payload, "amount_cents", "bid_amount_cents")
		currency := stringField(payload, "currency")

		// The outbox payloads carry no event timestamp, so bucket by consume time (UTC). This
		// is a known approximation; a real pipeline would stamp occurred_at at emit time.
		occurredAt := time.Now().UTC()

		if err := svc.IngestEvent(ctx, eventType, occurredAt, amount, currency); err != nil {
			logger.Error("ingest event failed", "topic", m.Topic, "event_type", eventType, "event_id", m.EventID, "err", err)
			return err // redelivered; dedupe + idempotent upserts keep counts exact
		}
		return nil
	}

	logger.Info("analytics consumer starting", "brokers", base.KafkaBrokers, "group", consumerGroup, "topics", strings.Join(topics, ","))
	if err := consumer.Run(ctx, handler); err != nil {
		logger.Error("consumer stopped", "err", err)
		os.Exit(1)
	}
	logger.Info("analytics consumer shut down")
}

// resolveEventType recovers a concrete event type from the payload's "type"/"event_type" field
// if present, else infers it from the topic. Topics that multiplex several event types are
// disambiguated by the identifying keys their payloads carry — critically keeping
// milestone.approved / contract.disputed distinct from contract.activated, and ensuring only
// payment.captured maps to the GMV-bearing type. Unknown shapes fall back to the topic name,
// which still bumps event_counts but moves neither GMV nor funnel.
func resolveEventType(topic string, m map[string]any) string {
	if t, ok := m["type"].(string); ok && t != "" {
		return t
	}
	if t, ok := m["event_type"].(string); ok && t != "" {
		return t
	}
	switch topic {
	case "payment.events":
		return "payment.captured"
	case "proposal.events":
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
		return "worksession.closed"
	case "screenshot.ingested":
		return "screenshot.ingested"
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
	default:
		return topic
	}
}

// centsField returns the first integer "*_cents" payload value (JSON numbers decode to float64).
func centsField(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		if v, ok := m[k].(float64); ok {
			return int64(v)
		}
	}
	return 0
}

// stringField returns the first key whose value is a non-empty string.
func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
