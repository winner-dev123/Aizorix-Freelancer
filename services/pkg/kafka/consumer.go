package kafka

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
)

// Message is the decoded event handed to a HandlerFunc.
type Message struct {
	Topic   string
	Key     string
	Value   []byte
	Headers map[string]string
	// EventID is the producer-assigned dedupe key (the "event-id" header set by the relay).
	EventID string
	// EventType is the domain event type (the "event-type" header), e.g. "contract.activated".
	EventType string
}

// HandlerFunc processes one message. Returning an error makes the consumer retry the message
// (offset is not committed), so handlers must be idempotent for at-least-once delivery.
type HandlerFunc func(ctx context.Context, m Message) error

// Consumer reads from a consumer group across one or more topics and commits offsets only
// after the handler succeeds (at-least-once). Multiple replicas in the same group share work.
// Deduplicator is the idempotency store. It is split into a read (AlreadyProcessed) and a
// write (MarkProcessed) on purpose, so an event is only ever marked AFTER its handler
// succeeds — marking on the check would let a transient handler failure permanently skip it.
type Deduplicator interface {
	AlreadyProcessed(ctx context.Context, consumer, eventID string) (bool, error)
	MarkProcessed(ctx context.Context, consumer, eventID string) error
}

type Consumer struct {
	r          *kafka.Reader
	group      string
	dedupe     Deduplicator
	dlq        *Producer
	maxRetries int
	backoff    time.Duration
}

func NewConsumer(brokers []string, group string, topics []string) *Consumer {
	return &Consumer{
		r: kafka.NewReader(kafka.ReaderConfig{
			Brokers:        brokers,
			GroupID:        group,
			GroupTopics:    topics,
			MinBytes:       1,
			MaxBytes:       10 << 20, // 10MB
			CommitInterval: 0,        // we commit explicitly after each handled message
			MaxWait:        500 * time.Millisecond,
		}),
		group:      group,
		maxRetries: 3,
		backoff:    time.Second,
	}
}

// WithDedupe enables exactly-once-effect processing by recording handled event ids in the
// `processed_events` table; replays of an already-processed event are skipped.
func (c *Consumer) WithDedupe(pool *pgxpool.Pool) *Consumer {
	c.dedupe = &Deduper{pool: pool}
	return c
}

// WithDLQ routes messages that still fail after maxRetries to `dlq.<topic>` (then commits so
// the partition is not wedged by a poison message). Without a DLQ, a permanently-failing
// message is left uncommitted and redelivered on the next consumer restart (at-least-once).
func (c *Consumer) WithDLQ(dlq *Producer, maxRetries int) *Consumer {
	c.dlq = dlq
	if maxRetries > 0 {
		c.maxRetries = maxRetries
	}
	return c
}

// Run blocks until ctx is cancelled, dispatching each message to handler.
func (c *Consumer) Run(ctx context.Context, handler HandlerFunc) error {
	defer c.r.Close()
	for {
		km, err := c.r.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		m := decode(km)
		if c.process(ctx, m, handler) {
			if err := c.r.CommitMessages(ctx, km); err != nil {
				return err
			}
		}
	}
}

// process applies dedupe → bounded retry → DLQ → mark, and returns whether the offset should
// be committed. It is decoupled from the Kafka reader so the dedupe/mark ORDERING (the part
// that had a real bug) is unit-testable with a fake Deduplicator and handler.
func (c *Consumer) process(ctx context.Context, m Message, handler HandlerFunc) (commit bool) {
	// Pure check (NOT a mark): skip events already successfully processed by this group.
	if c.dedupe != nil && m.EventID != "" {
		if done, derr := c.dedupe.AlreadyProcessed(ctx, c.group, m.EventID); derr == nil && done {
			return true // already handled; advance the offset
		}
	}

	// Bounded inline retry with backoff; on persistent failure, dead-letter (if configured).
	var herr error
	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		if herr = handler(ctx, m); herr == nil {
			break
		}
		if attempt < c.maxRetries {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(c.backoff * time.Duration(attempt)):
			}
		}
	}
	if herr != nil {
		if c.dlq == nil {
			return false // no DLQ: leave uncommitted so it is redelivered on restart
		}
		if derr := c.toDLQ(ctx, m, herr); derr != nil {
			return false // could not park it; retried on restart
		}
	}
	// Mark processed ONLY AFTER the handler succeeded (or the message was dead-lettered) — so a
	// handler failure never causes the event to be permanently skipped on redelivery.
	if c.dedupe != nil && m.EventID != "" {
		_ = c.dedupe.MarkProcessed(ctx, c.group, m.EventID)
	}
	return true
}

// toDLQ republishes a poison message to dlq.<topic> with diagnostic headers.
func (c *Consumer) toDLQ(ctx context.Context, m Message, cause error) error {
	headers := map[string]string{
		"event-id":       m.EventID,
		"event-type":     m.EventType,
		"original-topic": m.Topic,
		"consumer-group":  c.group,
		"error":          cause.Error(),
	}
	for k, v := range m.Headers {
		if _, exists := headers[k]; !exists {
			headers[k] = v
		}
	}
	return c.dlq.Publish(ctx, "dlq."+m.Topic, m.Key, m.Value, headers)
}

func decode(km kafka.Message) Message {
	headers := make(map[string]string, len(km.Headers))
	for _, h := range km.Headers {
		headers[h.Key] = string(h.Value)
	}
	return Message{
		Topic: km.Topic, Key: string(km.Key), Value: km.Value,
		Headers: headers, EventID: headers["event-id"], EventType: headers["event-type"],
	}
}

// Deduper records processed event ids for idempotent consumers. The check (AlreadyProcessed)
// and the record (MarkProcessed) are deliberately SEPARATE so an event is only ever marked
// AFTER its handler succeeds — marking on the check would let a transient handler failure
// permanently skip the event on redelivery.
type Deduper struct{ pool *pgxpool.Pool }

// AlreadyProcessed reports whether (consumer, eventID) was already handled. Read-only.
func (d *Deduper) AlreadyProcessed(ctx context.Context, consumer, eventID string) (bool, error) {
	var exists bool
	err := d.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM processed_events WHERE consumer=$1 AND event_id=$2)`,
		consumer, eventID).Scan(&exists)
	return exists, err
}

// MarkProcessed records (consumer, eventID) as handled. Idempotent.
func (d *Deduper) MarkProcessed(ctx context.Context, consumer, eventID string) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO processed_events (consumer, event_id) VALUES ($1,$2)
		ON CONFLICT (consumer, event_id) DO NOTHING`, consumer, eventID)
	return err
}
