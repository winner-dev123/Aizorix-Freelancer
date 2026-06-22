// Package contractparties is a small server-to-server client for the contract service's
// internal parties endpoint. The time-tracking service uses it to authorize that the caller
// opening a work session is the contract's freelancer (and that the contract is active)
// before a billable session is started.
//
// It mirrors the proven wsgateway hub.CheckMembership pattern: an HTTP GET to another
// service with a context timeout that FAILS CLOSED — any error (contract service
// unreachable, non-2xx, contract missing) is returned so the caller denies the action.
package contractparties

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrNotFound is returned when the contract service reports the contract does not exist
// (HTTP 404). Callers treat it the same as any other lookup failure: deny.
var ErrNotFound = errors.New("contractparties: contract not found")

// Parties is the resolved contract identity returned by the internal endpoint.
type Parties struct {
	ContractID   string `json:"contract_id"`
	ClientID     string `json:"client_id"`
	FreelancerID string `json:"freelancer_id"`
	Status       string `json:"status"`
}

// Client calls GET {baseURL}/v1/internal/contracts/{id}/parties.
type Client struct {
	baseURL string
	client  *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, client: &http.Client{Timeout: 5 * time.Second}}
}

// Get resolves a contract's parties. It applies its own ~5s timeout and fails closed: a
// non-2xx response or transport error is an error, so the caller denies. A 404 maps to
// ErrNotFound.
func (c *Client) Get(ctx context.Context, contractID string) (Parties, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/v1/internal/contracts/%s/parties", c.baseURL, contractID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Parties{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return Parties{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusNotFound {
		return Parties{}, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Parties{}, fmt.Errorf("contractparties: lookup returned %d: %s", resp.StatusCode, string(body))
	}
	var p Parties
	if err := json.Unmarshal(body, &p); err != nil {
		return Parties{}, err
	}
	return p, nil
}
