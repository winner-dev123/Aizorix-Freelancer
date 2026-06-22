package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/aizorix/platform/wsgateway/internal/config"
)

// Hub is the per-replica fan-out core. It tracks which local connections are subscribed to
// which conversations and maintains exactly one Redis subscription per conversation that has
// local subscribers (reference-counted). When a Redis message arrives on a conversation channel
// it is decoded once and delivered to every local connection subscribed to it.
//
// Concurrency model: a single mutex guards the membership maps. Per-conversation Redis reader
// goroutines deliver into connections' buffered send channels (non-blocking), so one slow
// client can never stall the fan-out path.
type Hub struct {
	cfg       config.Config
	bus       *Bus
	persister *Persister
	logger    *slog.Logger

	mu    sync.Mutex
	conns map[*Conn]struct{}
	// convs maps conversationID -> the set of local conns subscribed, plus the bookkeeping to
	// run one Redis reader per conversation.
	convs map[string]*convState
	// userConns counts this replica's live connections per user id, so presence is cleared
	// only when a user's LAST connection on this replica goes away (presence ref-counting).
	userConns map[string]int
}

type convState struct {
	subscribers map[*Conn]struct{}
	sub         *Subscription
	cancel      context.CancelFunc
}

// New builds a Hub.
func New(cfg config.Config, bus *Bus, persister *Persister, logger *slog.Logger) *Hub {
	return &Hub{
		cfg:       cfg,
		bus:       bus,
		persister: persister,
		logger:    logger,
		conns:     make(map[*Conn]struct{}),
		convs:     make(map[string]*convState),
		userConns: make(map[string]int),
	}
}

// register adds a connection to the hub and bumps the user's local connection count.
func (h *Hub) register(c *Conn) {
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.userConns[c.userID]++
	h.mu.Unlock()
}

// unregister removes a connection and drops it from every conversation it had joined, tearing
// down any now-empty Redis subscriptions. It returns whether this was the user's LAST live
// connection on this replica — the caller clears presence only then (presence ref-counting),
// so a user with another open connection stays "online".
func (h *Hub) unregister(c *Conn) (lastForUser bool) {
	h.mu.Lock()
	delete(h.conns, c)
	for convID := range c.joined {
		h.leaveLocked(c, convID)
	}
	if n := h.userConns[c.userID] - 1; n <= 0 {
		delete(h.userConns, c.userID)
		lastForUser = true
	} else {
		h.userConns[c.userID] = n
	}
	h.mu.Unlock()
	return lastForUser
}

// join subscribes a connection to a conversation, opening the shared Redis subscription if this
// is the first local subscriber.
//
// Membership authorization: before subscribing we verify the connection's user is a
// participant in the conversation by asking the messaging service (the source of truth). This
// is what prevents any authenticated user from eavesdropping on an arbitrary conversation's
// live messages. The check fails CLOSED — if membership cannot be confirmed (non-member, or
// the messaging service is unreachable) we send an error frame and do NOT subscribe.
func (h *Hub) join(ctx context.Context, c *Conn, conversationID string) {
	if conversationID == "" {
		c.trySend(errorFrame("BAD_FRAME", "join requires conversation_id"))
		return
	}

	// Fast path: if already joined there is nothing to authorize or subscribe again.
	h.mu.Lock()
	_, already := c.joined[conversationID]
	h.mu.Unlock()
	if already {
		return
	}

	// Authorize the join via the messaging service. Done WITHOUT the hub lock held, since
	// it is a blocking network call and must never stall the fan-out path.
	authCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	member, err := h.persister.CheckMembership(authCtx, conversationID, c.userID)
	cancel()
	if err != nil {
		h.logger.Warn("membership check failed; rejecting join", "conversation_id", conversationID, "user_id", c.userID, "err", err)
		c.trySend(errorFrame("FORBIDDEN", "could not verify conversation membership"))
		return
	}
	if !member {
		c.trySend(errorFrame("FORBIDDEN", "not a participant in this conversation"))
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if _, ok := c.joined[conversationID]; ok {
		return // already joined (raced with a concurrent join)
	}

	cs := h.convs[conversationID]
	if cs == nil {
		// First local subscriber: open the shared Redis subscription and start its reader.
		subCtx, cancel := context.WithCancel(ctx)
		sub, err := h.bus.Subscribe(subCtx, conversationID)
		if err != nil {
			cancel()
			c.trySend(errorFrame("SUBSCRIBE_FAILED", "could not subscribe to conversation"))
			return
		}
		cs = &convState{
			subscribers: make(map[*Conn]struct{}),
			sub:         sub,
			cancel:      cancel,
		}
		h.convs[conversationID] = cs
		if sub != nil {
			go h.readConversation(subCtx, conversationID, sub)
		}
	}
	cs.subscribers[c] = struct{}{}
	c.joined[conversationID] = struct{}{}
	c.trySend(Outbound{Type: TypeAck, ConversationID: conversationID, Ref: "join"}.encode())
}

// leave removes a connection from a conversation.
func (h *Hub) leave(c *Conn, conversationID string) {
	h.mu.Lock()
	h.leaveLocked(c, conversationID)
	h.mu.Unlock()
}

// leaveLocked must be called with h.mu held.
func (h *Hub) leaveLocked(c *Conn, conversationID string) {
	cs := h.convs[conversationID]
	if cs == nil {
		return
	}
	delete(cs.subscribers, c)
	delete(c.joined, conversationID)
	if len(cs.subscribers) == 0 {
		// Last local subscriber gone: tear down the Redis subscription to stop fan-out churn.
		cs.cancel()
		cs.sub.Close()
		delete(h.convs, conversationID)
	}
}

// readConversation drains a conversation's Redis subscription and fans each decoded frame out
// to the local connections subscribed to it. One goroutine per active conversation per replica.
func (h *Hub) readConversation(ctx context.Context, conversationID string, sub *Subscription) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.Messages():
			if !ok {
				return
			}
			var frame Outbound
			if err := json.Unmarshal([]byte(msg.Payload), &frame); err != nil {
				h.logger.Warn("bad pub/sub payload", "conversation_id", conversationID, "err", err)
				continue
			}
			h.deliverLocal(conversationID, frame, msg.Payload)
		}
	}
}

// deliverLocal pushes a frame (already JSON in `raw`) to every local connection subscribed to
// the conversation. Delivery is non-blocking per connection.
func (h *Hub) deliverLocal(conversationID string, frame Outbound, raw string) {
	h.mu.Lock()
	cs := h.convs[conversationID]
	if cs == nil {
		h.mu.Unlock()
		return
	}
	targets := make([]*Conn, 0, len(cs.subscribers))
	for c := range cs.subscribers {
		// Don't echo a sender's own typing/read indicators back to itself.
		if frame.SenderID != "" && frame.SenderID == c.userID &&
			(frame.Type == TypeTyping || frame.Type == TypeRead) {
			continue
		}
		targets = append(targets, c)
	}
	h.mu.Unlock()

	payload := []byte(raw)
	for _, c := range targets {
		c.trySend(payload)
	}
}

// handleSend processes an inbound "send" frame: persist to the messaging service (best-effort)
// then publish a message.sent event to Redis for cross-replica fan-out.
func (h *Hub) handleSend(ctx context.Context, c *Conn, in Inbound) {
	if in.ConversationID == "" || in.Body == "" {
		c.trySend(errorFrame("BAD_FRAME", "send requires conversation_id and body"))
		return
	}
	now := time.Now().UnixMilli()

	// Best-effort persistence — the messaging service is the source of truth, but a transient
	// failure must not drop live delivery. We still fan out; the durable record reconciles via
	// the messaging service's own events.
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	messageID, err := h.persister.PostMessage(pctx, in.ConversationID, c.userID, in.Body)
	cancel()
	if err != nil {
		h.logger.Warn("message persist failed; fanning out anyway", "conversation_id", in.ConversationID, "err", err)
	}

	frame := Outbound{
		Type:           TypeMessage,
		ConversationID: in.ConversationID,
		MessageID:      messageID,
		SenderID:       c.userID,
		Body:           in.Body,
		Ref:            in.Ref,
		TS:             now,
	}
	if h.bus.Enabled() {
		// Redis fans out to every replica (including this one, via the subscription reader).
		h.bus.Publish(ctx, in.ConversationID, frame)
		return
	}
	// No Redis: deliver locally so same-replica subscribers still receive the message.
	h.deliverLocal(in.ConversationID, frame, string(frame.encode()))
}

// handleEphemeral publishes typing/read receipts — pure pub/sub, never persisted.
func (h *Hub) handleEphemeral(ctx context.Context, c *Conn, in Inbound, typ string) {
	if in.ConversationID == "" {
		c.trySend(errorFrame("BAD_FRAME", typ+" requires conversation_id"))
		return
	}
	frame := Outbound{
		Type:           typ,
		ConversationID: in.ConversationID,
		SenderID:       c.userID,
		TS:             time.Now().UnixMilli(),
	}
	if h.bus.Enabled() {
		h.bus.Publish(ctx, in.ConversationID, frame)
		return
	}
	// No Redis: deliver locally so same-replica subscribers still receive the receipt.
	h.deliverLocal(in.ConversationID, frame, string(frame.encode()))
}

// Online proxies a presence query to the bus.
func (h *Hub) Online(ctx context.Context, userIDs []string) (map[string]bool, error) {
	return h.bus.Online(ctx, userIDs)
}
