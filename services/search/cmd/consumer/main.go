// Command consumer runs the search service's indexing consumer. It subscribes to project.events
// and user.events and keeps the search engine's documents in sync: project.published indexes a
// project document, project.closed removes it, and a searchable freelancer profile.updated
// (re)indexes a freelancer document (an unsearchable one removes it). user.events is multiplexed
// (client profiles, user.registered, session.created), so non-freelancer events are ignored.
//
// The engine is the same SearchEngine the REST service uses: in dev/tests it is the OpenSearch
// stub (whose Index/Delete are no-op log lines and whose project search falls back to Postgres
// FTS); in prod it is a real OpenSearch client that this consumer's Index/Delete calls populate.
//
// Delivery is at-least-once; .WithDedupe(pool) makes processing effectively-once via the
// processed_events table. Index/Delete upserts are idempotent, and unknown event types are a
// no-op that still commits the offset.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/kafka"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/pg"
	"github.com/aizorix/platform/search/internal/service"
	"github.com/aizorix/platform/search/internal/store"
)

const consumerGroup = "search-index-consumer"

// topics are the streams whose documents the search index mirrors.
var topics = []string{"project.events", "user.events"}

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "search-consumer", base.Environment)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pg.Connect(ctx, base.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	st := store.New(pool)
	// Same runtime selection as cmd/server: a configured ELASTICSEARCH_URL (or OPENSEARCH_URL)
	// uses the real OpenSearch client this consumer's Index/Delete calls populate; otherwise the
	// Postgres-FTS stub (whose Index/Delete are no-op log lines).
	esURL := config.Get("ELASTICSEARCH_URL", config.Get("OPENSEARCH_URL", ""))
	es := store.NewSearchEngine(pool, esURL,
		config.Get("OPENSEARCH_USERNAME", ""), config.Get("OPENSEARCH_PASSWORD", ""), logger)
	svc := service.New(st, es)

	brokers := strings.Split(base.KafkaBrokers, ",")
	consumer := kafka.NewConsumer(brokers, consumerGroup, topics).WithDedupe(pool)

	handler := func(ctx context.Context, m kafka.Message) error {
		if err := indexEvent(ctx, svc, m); err != nil {
			logger.Error("index event failed", "topic", m.Topic, "event_id", m.EventID, "err", err)
			return err // redelivered; Index/Delete are idempotent
		}
		return nil
	}

	logger.Info("search consumer starting", "brokers", base.KafkaBrokers, "group", consumerGroup, "topics", strings.Join(topics, ","))
	if err := consumer.Run(ctx, handler); err != nil {
		logger.Error("consumer stopped", "err", err)
		os.Exit(1)
	}
	logger.Info("search consumer shut down")
}

// indexEvent decodes one event and applies it to the index: publish ⇒ Index, close ⇒ Delete.
// Unknown event types are a no-op so the offset still commits.
func indexEvent(ctx context.Context, svc *service.Service, m kafka.Message) error {
	var payload map[string]any
	if err := json.Unmarshal(m.Value, &payload); err != nil {
		return nil // poison payload; do not wedge the partition
	}
	// Prefer the self-describing bus header (set by the relay); fall back to inference.
	eventType := m.EventType
	if eventType == "" {
		eventType = resolveEventType(m.Topic, payload)
	}

	switch a := route(eventType, payload); a.op {
	case opIndex:
		return svc.Index(ctx, a.docType, a.id, payload)
	case opDelete:
		return svc.Delete(ctx, a.docType, a.id)
	default:
		return nil // user.registered, session.created, client profiles, unknown — all ignored
	}
}

const (
	opIndex  = "index"
	opDelete = "delete"
)

// indexAction is the routing decision for one event — what to do to which index document — kept
// separate from the engine call so the rules below are unit-testable.
type indexAction struct {
	op      string // opIndex, opDelete, or "" (ignore)
	docType string
	id      string
}

// route maps an event type + payload to an index mutation, independent of the search engine. The
// security-relevant invariants live here: user.events is multiplexed, so the user service emits
// profile.updated for BOTH freelancer (kind=freelancer) and client (kind=client) profiles and the
// auth service emits user.registered/session.created on the same topic. Only a freelancer-kind
// profile may enter the freelancers index, and only while searchable (an unsearchable one is
// removed); client profiles and non-profile user events must NEVER be indexed as freelancers.
func route(eventType string, payload map[string]any) indexAction {
	switch eventType {
	case "project.published":
		return indexAction{opIndex, "project", stringField(payload, "project_id", "id")}
	case "project.closed":
		return indexAction{opDelete, "project", stringField(payload, "project_id", "id")}
	case "profile.updated":
		if !strings.EqualFold(stringField(payload, "kind"), "freelancer") {
			return indexAction{} // client (or any non-freelancer) profile: ignore
		}
		userID := stringField(payload, "user_id", "id")
		if boolField(payload, "is_searchable") {
			return indexAction{opIndex, "freelancer", userID}
		}
		return indexAction{opDelete, "freelancer", userID}
	default:
		return indexAction{}
	}
}

// resolveEventType recovers the event type from the payload (if a "type" field is present) or
// infers it from the topic and payload shape. The outbox does not propagate event_type onto the
// wire, so consumers reconstruct it here.
func resolveEventType(topic string, m map[string]any) string {
	if t, ok := m["type"].(string); ok && t != "" {
		return t
	}
	if t, ok := m["event_type"].(string); ok && t != "" {
		return t
	}
	switch topic {
	case "project.events":
		if _, ok := m["closed_at"]; ok {
			return "project.closed"
		}
		return "project.published"
	case "user.events":
		if _, ok := m["kyc_status"]; ok {
			return "user.kyc_updated"
		}
		return "profile.updated"
	default:
		return topic
	}
}

// boolField reports whether the given key holds a truthy value. JSON decodes booleans into
// bool, but the field may also arrive as a string ("true") or a number, so handle those too.
func boolField(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1"
	case float64:
		return v != 0
	default:
		return false
	}
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
