// Package kafka provides the platform's event-bus producer/consumer on top of
// segmentio/kafka-go (works against MSK in prod and Redpanda in dev). The producer
// implements outbox.Publisher so the transactional-outbox relay can publish through it.
package kafka

import (
	"context"
	"time"

	"github.com/segmentio/kafka-go"
)

// Producer is a single writer shared across topics (the topic is set per-message). It hashes
// on the message key so all events for one aggregate land on the same partition (ordering).
type Producer struct {
	w *kafka.Writer
}

func NewProducer(brokers []string) *Producer {
	return &Producer{
		w: &kafka.Writer{
			Addr:                   kafka.TCP(brokers...),
			Balancer:               &kafka.Hash{},
			RequiredAcks:           kafka.RequireAll, // durability: wait for all in-sync replicas
			AllowAutoTopicCreation: true,             // dev convenience; prod pre-creates topics
			BatchTimeout:           50 * time.Millisecond,
			Async:                  false, // the outbox relay needs a synchronous ack to mark sent
		},
	}
}

// Publish satisfies outbox.Publisher: send one keyed, headered message to a topic.
func (p *Producer) Publish(ctx context.Context, topic, key string, payload []byte, headers map[string]string) error {
	hs := make([]kafka.Header, 0, len(headers))
	for k, v := range headers {
		hs = append(hs, kafka.Header{Key: k, Value: []byte(v)})
	}
	return p.w.WriteMessages(ctx, kafka.Message{
		Topic:   topic,
		Key:     []byte(key),
		Value:   payload,
		Headers: hs,
		Time:    time.Now(),
	})
}

func (p *Producer) Close() error { return p.w.Close() }
