// Package hub implements the real-time messaging fabric: per-connection read/write pumps,
// conversation subscriptions, and fan-out across gateway replicas via Redis pub/sub.
//
// Wire protocol (all frames are JSON objects with a "type" discriminator):
//
//	Inbound (client -> gateway):
//	  {"type":"join",   "conversation_id":"<id>"}                 subscribe to a conversation
//	  {"type":"leave",  "conversation_id":"<id>"}                 unsubscribe
//	  {"type":"send",   "conversation_id":"<id>", "body":"..."}   send a chat message (persisted)
//	  {"type":"typing", "conversation_id":"<id>"}                 ephemeral typing indicator
//	  {"type":"read",   "conversation_id":"<id>"}                 ephemeral read receipt
//
//	Outbound (gateway -> client):
//	  {"type":"message.sent", "conversation_id", "message_id", "sender_id", "body", "ts"}
//	  {"type":"typing",       "conversation_id", "sender_id", "ts"}
//	  {"type":"read",         "conversation_id", "sender_id", "ts"}
//	  {"type":"presence",     "user_id", "online"}
//	  {"type":"error",        "code", "message"}
//	  {"type":"ack",          "conversation_id", "ref"}  (optional join/send acknowledgement)
package hub

import "encoding/json"

// Frame types (the "type" field). Inbound and outbound share the wire namespace where it makes
// sense (e.g. typing/read echo the inbound shape with a sender_id added).
const (
	TypeJoin     = "join"
	TypeLeave    = "leave"
	TypeSend     = "send"
	TypeTyping   = "typing"
	TypeRead     = "read"
	TypeMessage  = "message.sent"
	TypePresence = "presence"
	TypeError    = "error"
	TypeAck      = "ack"
)

// Inbound is a frame received from a client. Only the fields relevant to its Type are set.
type Inbound struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
	Body           string `json:"body,omitempty"`
	Ref            string `json:"ref,omitempty"` // optional client correlation id, echoed in ack
}

// Outbound is a frame delivered to a client (and the shape published to Redis for fan-out).
type Outbound struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	SenderID       string `json:"sender_id,omitempty"`
	Body           string `json:"body,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	Online         bool   `json:"online,omitempty"`
	Code           string `json:"code,omitempty"`
	Message        string `json:"message,omitempty"`
	Ref            string `json:"ref,omitempty"`
	TS             int64  `json:"ts,omitempty"` // unix millis
}

func (o Outbound) encode() []byte {
	b, _ := json.Marshal(o)
	return b
}

func errorFrame(code, msg string) []byte {
	return Outbound{Type: TypeError, Code: code, Message: msg}.encode()
}
