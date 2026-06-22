package hub

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Bus is the cross-replica transport. Every gateway replica publishes conversation events to
// the Redis channel conv:{conversationId} and subscribes to the channels for conversations it
// currently has local subscribers in. This is what makes WebSocket fan-out horizontally
// scalable: a sticky-WS load balancer (NLB) pins a client to one replica, and Redis pub/sub
// stitches the replicas together so a message sent on replica A reaches subscribers on B and C.
//
// Bus also owns presence: presence:{userId} keys with a TTL refreshed on every client
// heartbeat. A user is "online" exactly while at least one of their keys exists.
type Bus struct {
	rdb         *redis.Client
	presenceTTL time.Duration
	logger      *slog.Logger
}

// NewBus builds a Bus. A nil client (empty addr) is tolerated by callers that only need local
// delivery, but in production Redis is required for multi-replica fan-out.
func NewBus(rdb *redis.Client, presenceTTL time.Duration, logger *slog.Logger) *Bus {
	return &Bus{rdb: rdb, presenceTTL: presenceTTL, logger: logger}
}

// Enabled reports whether Redis-backed cross-replica fan-out is active. When false the hub
// must deliver frames to local connections directly, since nothing arrives over pub/sub.
func (b *Bus) Enabled() bool { return b.rdb != nil }

// convChannel is the Redis pub/sub channel for a conversation.
func convChannel(conversationID string) string { return "conv:" + conversationID }

// presenceKey is the Redis key holding a user's presence (with TTL).
func presenceKey(userID string) string { return "presence:" + userID }

// Publish fans an outbound frame out to every replica subscribed to the conversation. It is
// best-effort: a Redis hiccup logs and drops, it must not block the sender's write pump.
func (b *Bus) Publish(ctx context.Context, conversationID string, frame Outbound) {
	if b.rdb == nil {
		return
	}
	if err := b.rdb.Publish(ctx, convChannel(conversationID), frame.encode()).Err(); err != nil {
		b.logger.Warn("redis publish failed", "conversation_id", conversationID, "err", err)
	}
}

// Subscription is a live subscription to one conversation channel. Messages() yields the raw
// JSON payloads published by any replica; the hub decodes and fans them to local connections.
type Subscription struct {
	ps *redis.PubSub
	ch <-chan *redis.Message
}

// Subscribe opens a Redis subscription for a conversation channel. Returns nil if Redis is
// disabled (single-replica/local mode), in which case the hub delivers purely locally.
func (b *Bus) Subscribe(ctx context.Context, conversationID string) (*Subscription, error) {
	if b.rdb == nil {
		return nil, nil
	}
	ps := b.rdb.Subscribe(ctx, convChannel(conversationID))
	// Wait for the subscription to be confirmed so we don't miss the first message.
	if _, err := ps.Receive(ctx); err != nil {
		_ = ps.Close()
		return nil, fmt.Errorf("subscribe %s: %w", conversationID, err)
	}
	return &Subscription{ps: ps, ch: ps.Channel()}, nil
}

// Messages returns the channel of inbound pub/sub payloads.
func (s *Subscription) Messages() <-chan *redis.Message {
	if s == nil {
		return nil
	}
	return s.ch
}

// Close tears down the subscription.
func (s *Subscription) Close() {
	if s != nil && s.ps != nil {
		_ = s.ps.Close()
	}
}

// Heartbeat (re)sets a user's presence key with the configured TTL. Called on connect and on
// every keepalive pong, so a crashed client's presence expires naturally.
func (b *Bus) Heartbeat(ctx context.Context, userID string) {
	if b.rdb == nil {
		return
	}
	if err := b.rdb.Set(ctx, presenceKey(userID), "1", b.presenceTTL).Err(); err != nil {
		b.logger.Warn("presence heartbeat failed", "user_id", userID, "err", err)
	}
}

// ClearPresence removes a user's presence key on a clean disconnect (best-effort; the TTL is
// the real backstop).
func (b *Bus) ClearPresence(ctx context.Context, userID string) {
	if b.rdb == nil {
		return
	}
	_ = b.rdb.Del(ctx, presenceKey(userID)).Err()
}

// Online reports, for each requested user id, whether a presence key currently exists.
func (b *Bus) Online(ctx context.Context, userIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(userIDs))
	if b.rdb == nil {
		// Without Redis we cannot know cross-replica presence; report everyone offline.
		for _, id := range userIDs {
			out[id] = false
		}
		return out, nil
	}
	keys := make([]string, len(userIDs))
	for i, id := range userIDs {
		keys[i] = presenceKey(id)
	}
	vals, err := b.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	for i, id := range userIDs {
		out[id] = i < len(vals) && vals[i] != nil
	}
	return out, nil
}
