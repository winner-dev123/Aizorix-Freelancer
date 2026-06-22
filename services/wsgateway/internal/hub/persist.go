package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Persister writes inbound chat messages to the messaging service's REST API. Persistence is
// best-effort from the WebSocket's point of view: we attempt the write, capture the resulting
// message id when available, and fan out over Redis regardless so live delivery is never
// blocked by a slow datastore. The messaging service remains the source of truth (history,
// participant authorization), and its own message.sent events are the durable record.
type Persister struct {
	baseURL string
	client  *http.Client
}

func NewPersister(baseURL string) *Persister {
	return &Persister{
		baseURL: baseURL,
		client:  &http.Client{Timeout: 5 * time.Second},
	}
}

type persistReq struct {
	Body string `json:"body"`
	Kind string `json:"kind"`
}

type persistResp struct {
	ID string `json:"id"`
}

type membershipResp struct {
	Member bool `json:"member"`
}

// CheckMembership asks the messaging service whether userID is a participant in
// conversationID. It is the authorization gate for a WebSocket "join": the wsgateway must
// not subscribe a connection to a conversation's live events unless the user is a member.
// Errors (messaging unreachable, non-2xx) are returned so the caller fails CLOSED — i.e.
// a join is rejected when membership cannot be confirmed.
func (p *Persister) CheckMembership(ctx context.Context, conversationID, userID string) (bool, error) {
	url := fmt.Sprintf("%s/v1/messaging/conversations/%s/membership", p.baseURL, conversationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, err
	}
	// Trusted identity header, same mesh-internal contract the persister uses for writes.
	req.Header.Set("X-User-Id", userID)

	resp, err := p.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Errorf("membership check returned %d: %s", resp.StatusCode, string(bodyBytes))
	}
	var out membershipResp
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return false, err
	}
	return out.Member, nil
}

// PostMessage persists a message to {MESSAGING_URL}/v1/messaging/conversations/{id}/messages on
// behalf of senderID (passed via the trusted X-User-Id header, exactly as the gateway injects
// it for REST callers). Returns the stored message id, or an error if the write failed. Callers
// treat the error as non-fatal and still fan out for live delivery.
func (p *Persister) PostMessage(ctx context.Context, conversationID, senderID, body string) (string, error) {
	payload, _ := json.Marshal(persistReq{Body: body, Kind: "text"})
	url := fmt.Sprintf("%s/v1/messaging/conversations/%s/messages", p.baseURL, conversationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	// The messaging service authorizes participation off this trusted identity header. In
	// production the wsgateway is inside the mesh and this header is trusted hop-to-hop.
	req.Header.Set("X-User-Id", senderID)

	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("messaging persist returned %d: %s", resp.StatusCode, string(bodyBytes))
	}
	var out persistResp
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}
