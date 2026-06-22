// Command consumer runs the search service's indexing consumer. It subscribes to project.events
// and user.events and keeps the search engine's documents in sync: project.published indexes a
// project document, project.closed removes it, and user profile.updated (re)indexes a freelancer
// document.
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

	"github.com/aizorix/platform/search/internal/service"
	"github.com/aizorix/platform/search/internal/store"
	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/kafka"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/pg"
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
	eventType := resolveEventType(m.Topic, payload)

	switch eventType {
	case "project.published":
		return svc.Index(ctx, "project", stringField(payload, "project_id", "id"), payload)
	case "project.closed":
		return svc.Delete(ctx, "project", stringField(payload, "project_id", "id"))
	case "profile.updated":
		// (Re)index the freelancer/client profile document. The user service emits one
		// profile.updated for both profile kinds keyed on user_id.
		return svc.Index(ctx, "freelancer", stringField(payload, "user_id", "id"), payload)
	default:
		return nil
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

// stringField returns the first key whose value is a non-empty string.
func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
