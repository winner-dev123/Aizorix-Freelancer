package hub

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gorilla/websocket"
)

// Conn is one authenticated WebSocket connection. It runs two pumps:
//   - readPump: reads client frames, enforces the pong keepalive, dispatches to the hub, and
//     refreshes presence on every pong.
//   - writePump: serializes all writes (gorilla forbids concurrent writers) and emits periodic
//     pings. Outbound frames are queued on a buffered channel (send buffering); if a client is
//     too slow and the buffer fills, the connection is dropped rather than blocking the hub.
type Conn struct {
	hub    *Hub
	ws     *websocket.Conn
	userID string

	send   chan []byte
	joined map[string]struct{} // conversations this connection has joined (read in pumps only)

	closed chan struct{}
}

// Serve adopts an upgraded WebSocket connection for an authenticated user and runs it until it
// closes. It is the hub's public entrypoint from the HTTP layer. Blocks for the connection's
// lifetime, so callers run it in the request goroutine (the socket is hijacked).
func (h *Hub) Serve(ctx context.Context, ws *websocket.Conn, userID string) {
	c := newConn(h, ws, userID)
	c.run(ctx)
}

func newConn(h *Hub, ws *websocket.Conn, userID string) *Conn {
	return &Conn{
		hub:    h,
		ws:     ws,
		userID: userID,
		send:   make(chan []byte, h.cfg.SendBuffer),
		joined: make(map[string]struct{}),
		closed: make(chan struct{}),
	}
}

// trySend enqueues a frame for delivery without blocking. If the per-connection buffer is full
// (a slow/stalled client), the connection is closed: backpressure protects the hub from one bad
// peer. Safe to call from any goroutine.
func (c *Conn) trySend(b []byte) {
	select {
	case c.send <- b:
	case <-c.closed:
	default:
		// Buffer full: drop this client. Closing `closed` makes both pumps exit; the write pump
		// closes the socket. Guard against a double close via the select on c.closed above.
		c.closeOnce()
	}
}

func (c *Conn) closeOnce() {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
}

// run starts the connection: registers it, seeds presence, and runs the pumps. It blocks until
// the connection ends, then unregisters and clears presence.
func (c *Conn) run(ctx context.Context) {
	c.hub.register(c)
	c.hub.bus.Heartbeat(ctx, c.userID)

	go c.writePump(ctx)
	c.readPump(ctx) // blocks until read error / close

	c.closeOnce()
	// Clear presence only when this was the user's last live connection on this replica;
	// otherwise a second connection closing would falsely mark a still-connected user offline.
	if lastForUser := c.hub.unregister(c); lastForUser {
		c.hub.bus.ClearPresence(context.Background(), c.userID)
	}
}

// readPump reads and dispatches client frames until the socket closes or a keepalive lapses.
func (c *Conn) readPump(ctx context.Context) {
	cfg := c.hub.cfg
	c.ws.SetReadLimit(cfg.MaxMessageSize)
	_ = c.ws.SetReadDeadline(time.Now().Add(cfg.PongWait))
	c.ws.SetPongHandler(func(string) error {
		// A live pong proves the client is healthy: extend the read deadline and refresh
		// presence so the user stays "online" without any explicit heartbeat frame.
		_ = c.ws.SetReadDeadline(time.Now().Add(cfg.PongWait))
		c.hub.bus.Heartbeat(ctx, c.userID)
		return nil
	})

	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var in Inbound
		if err := json.Unmarshal(data, &in); err != nil {
			c.trySend(errorFrame("BAD_FRAME", "could not parse frame"))
			continue
		}
		c.dispatch(ctx, in)
	}
}

// dispatch routes one inbound frame to the appropriate hub handler.
func (c *Conn) dispatch(ctx context.Context, in Inbound) {
	switch in.Type {
	case TypeJoin:
		c.hub.join(ctx, c, in.ConversationID)
	case TypeLeave:
		c.hub.leave(c, in.ConversationID)
	case TypeSend:
		c.hub.handleSend(ctx, c, in)
	case TypeTyping:
		c.hub.handleEphemeral(ctx, c, in, TypeTyping)
	case TypeRead:
		c.hub.handleEphemeral(ctx, c, in, TypeRead)
	default:
		c.trySend(errorFrame("UNKNOWN_TYPE", "unknown frame type: "+in.Type))
	}
}

// writePump owns all writes to the socket: queued frames plus periodic pings. It is the only
// goroutine that calls ws.Write*, satisfying gorilla's single-writer requirement.
func (c *Conn) writePump(ctx context.Context) {
	cfg := c.hub.cfg
	ticker := time.NewTicker(cfg.PingInterval)
	defer func() {
		ticker.Stop()
		_ = c.ws.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			c.writeClose(websocket.CloseGoingAway, "server shutting down")
			return
		case <-c.closed:
			c.writeClose(websocket.CloseNormalClosure, "")
			return
		case msg := <-c.send:
			_ = c.ws.SetWriteDeadline(time.Now().Add(cfg.WriteWait))
			if err := c.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(cfg.WriteWait))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// writeClose sends a best-effort close frame before the socket is torn down.
func (c *Conn) writeClose(code int, reason string) {
	_ = c.ws.SetWriteDeadline(time.Now().Add(c.hub.cfg.WriteWait))
	_ = c.ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason))
}
