// Command relay is the transactional-outbox publisher. One replica set runs per service
// database: it polls that DB's `outbox` table (SELECT ... FOR UPDATE SKIP LOCKED) and
// publishes pending events to Kafka, marking them sent. Multiple replicas share the work
// safely. This is what turns committed state changes into bus events with no dual-write.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/kafka"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/outbox"
	"github.com/aizorix/platform/pkg/pg"
)

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "relay", base.Environment)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pg.Connect(ctx, base.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	producer := kafka.NewProducer(strings.Split(base.KafkaBrokers, ","))
	defer producer.Close()

	batch := config.GetInt("RELAY_BATCH", 100)
	interval := config.GetDuration("RELAY_INTERVAL", 500*time.Millisecond)
	// RELAY_SOURCE identifies the service DB this relay drains. In a one-DB-per-service
	// deployment, set it per service so dedupe event-ids never collide across databases.
	source := config.Get("RELAY_SOURCE", "")
	if source == "" {
		logger.Warn("RELAY_SOURCE not set; event-ids unprefixed — fine for a single shared DB, but set it per service in multi-DB deployments to avoid cross-DB dedupe collisions")
	}
	relay := outbox.NewRelay(pool, producer, source, batch, interval, logger)

	logger.Info("outbox relay started", "brokers", base.KafkaBrokers, "source", source, "batch", batch, "interval", interval.String())
	if err := relay.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("relay stopped", "err", err)
		os.Exit(1)
	}
	logger.Info("relay shut down cleanly")
}
