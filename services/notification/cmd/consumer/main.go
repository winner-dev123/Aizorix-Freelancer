// Command consumer runs the notification service's Kafka event consumer. It subscribes to the
// domain-event topics the notification service cares about and, for each event, fans out a
// notification (a notifications row + one delivery_attempts row per enabled channel, honoring
// notification_preferences) via service.Service.HandleRawEvent.
//
// Delivery is at-least-once; the consumer is made effectively-once with .WithDedupe(pool),
// which records handled event ids in the processed_events table and skips replays. Unknown
// event types are a no-op that still commits the offset, so the stream never wedges.
package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aizorix/platform/notification/internal/service"
	"github.com/aizorix/platform/notification/internal/store"
	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/kafka"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/pg"
)

// consumerGroup is the Kafka consumer-group id; all replicas share it so partitions are
// balanced across them and offsets/dedupe are tracked per group.
const consumerGroup = "notification-consumer"

// topics are the domain-event streams the notification service subscribes to.
var topics = []string{
	"contract.events",
	"payment.events",
	"escrow.events",
	"proposal.events",
	"worksession.events",
	"review.events",
	"user.events",
	"fraud.events",
}

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "notification-consumer", base.Environment)
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
		// Prefer the self-describing bus header (set by the relay); fall back to inference.
		eventType := m.EventType
		if eventType == "" {
			eventType = service.ResolveEventType(m.Topic, m.Value)
		}
		if err := svc.HandleRawEvent(ctx, eventType, m.Value); err != nil {
			logger.Error("handle event failed", "topic", m.Topic, "event_type", eventType, "event_id", m.EventID, "err", err)
			return err // not committed; redelivered (handler is idempotent + deduped)
		}
		logger.Info("event handled", "topic", m.Topic, "event_type", eventType, "event_id", m.EventID)
		return nil
	}

	logger.Info("notification consumer starting", "brokers", base.KafkaBrokers, "group", consumerGroup, "topics", strings.Join(topics, ","))
	if err := consumer.Run(ctx, handler); err != nil {
		logger.Error("consumer stopped", "err", err)
		os.Exit(1)
	}
	logger.Info("notification consumer shut down")
}
