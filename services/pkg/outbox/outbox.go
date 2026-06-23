// Package outbox implements the transactional outbox pattern. A state change and the
// event it emits are written in ONE database transaction; a separate relay polls the
// outbox table and publishes to Kafka, then marks rows published. This guarantees
// at-least-once delivery with no dual-write inconsistency.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// Event is the payload to enqueue. Topic + PartitionKey decide Kafka placement.
type Event struct {
	AggregateType string
	AggregateID   string
	EventType     string
	Topic         string
	PartitionKey  string
	Payload       any
	Headers       map[string]string
}

// Enqueue writes the event to the outbox within the caller's transaction `tx`.
// The caller must commit tx; on rollback the event is discarded with the state change.
func Enqueue(ctx context.Context, tx pgx.Tx, e Event) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("outbox: marshal payload: %w", err)
	}
	headers, err := json.Marshal(e.Headers)
	if err != nil {
		return fmt.Errorf("outbox: marshal headers: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox (aggregate_type, aggregate_id, event_type, topic, partition_key, payload, headers)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		e.AggregateType, e.AggregateID, e.EventType, e.Topic, e.PartitionKey, payload, headers)
	if err != nil {
		return fmt.Errorf("outbox: insert: %w", err)
	}
	return nil
}

// Publisher is implemented by the Kafka producer used by the relay.
type Publisher interface {
	Publish(ctx context.Context, topic, key string, payload []byte, headers map[string]string) error
}

type Row struct {
	ID           int64
	EventType    string
	Topic        string
	PartitionKey string
	Payload      []byte
	Headers      map[string]string
}

// Relay polls unpublished rows and publishes them. SELECT ... FOR UPDATE SKIP LOCKED lets
// multiple replicas share the work for at-least-once DELIVERY. ORDERING CAVEAT: SKIP LOCKED hands
// rows to whichever replica grabs them, so two replicas can publish same-partition-key events out
// of outbox-id order. Run EXACTLY ONE relay replica per source database when consumers rely on
// per-aggregate ordering (the default deployment is one relay per service DB); to scale out,
// shard by hash(partition_key) so all events for an aggregate drain through one replica.
type Relay struct {
	pool interface {
		Begin(ctx context.Context) (pgx.Tx, error)
	}
	pub Publisher
	// source identifies the service/database this relay drains. It prefixes the dedupe
	// event-id so ids never collide ACROSS databases: each service's outbox has its own
	// IDENTITY sequence (both starting at 1), so a bare row id would make e.g. payment#42 and
	// contract#42 dedupe-collide in a consumer that reads from both — silently dropping events.
	source   string
	batch    int
	interval time.Duration
	logger   *slog.Logger
}

func NewRelay(pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}, pub Publisher, source string, batch int, interval time.Duration, logger *slog.Logger) *Relay {
	return &Relay{pool: pool, pub: pub, source: source, batch: batch, interval: interval, logger: logger}
}

// eventID builds the globally-unique dedupe key for a published event. It prefixes the outbox
// row id with the relay's source (the service/database), because each service's outbox has its
// own IDENTITY sequence (all starting at 1) — a bare id collides across databases and causes
// false-positive dedupe drops in consumers that read from multiple service DBs.
func eventID(source string, id int64) string {
	if source != "" {
		return fmt.Sprintf("%s:%d", source, id)
	}
	return fmt.Sprintf("%d", id)
}

// Run blocks, polling until ctx is cancelled.
func (r *Relay) Run(ctx context.Context) error {
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := r.drain(ctx); err != nil {
				// Surface the failure: a persistently failing publish/DB error makes the
				// outbox silently stop draining (rows pile up, events never delivered).
				if r.logger != nil {
					r.logger.Warn("outbox relay drain failed; retrying next tick", "err", err)
				}
				continue
			}
		}
	}
}

func (r *Relay) drain(ctx context.Context) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		SELECT id, event_type, topic, partition_key, payload, headers
		FROM outbox
		WHERE published_at IS NULL
		ORDER BY id
		FOR UPDATE SKIP LOCKED
		LIMIT $1`, r.batch)
	if err != nil {
		return err
	}
	var batch []Row
	for rows.Next() {
		var row Row
		var headers []byte
		if err := rows.Scan(&row.ID, &row.EventType, &row.Topic, &row.PartitionKey, &row.Payload, &headers); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal(headers, &row.Headers)
		batch = append(batch, row)
	}
	rows.Close()

	for _, row := range batch {
		// Make the bus self-describing: a stable dedupe key + the event type, so consumers
		// neither replay nor have to infer the type from the topic/payload shape.
		if row.Headers == nil {
			row.Headers = map[string]string{}
		}
		row.Headers["event-id"] = eventID(r.source, row.ID)
		row.Headers["event-type"] = row.EventType
		if err := r.pub.Publish(ctx, row.Topic, row.PartitionKey, row.Payload, row.Headers); err != nil {
			return err // leave unpublished; retried next tick
		}
		if _, err := tx.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = $1`, row.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
