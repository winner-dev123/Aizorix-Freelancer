// Package proposallookup is a small server-to-server client for the proposal service's internal
// endpoint. The contract service uses it to form a contract from the AUTHORITATIVE proposal
// (freelancer, bid amount, owning client, status) rather than trusting the request body — so a
// caller cannot mint a contract naming an arbitrary freelancer at an arbitrary rate.
//
// It mirrors the proven contractparties/wsgateway.CheckMembership pattern: an HTTP GET with a
// context timeout that FAILS CLOSED — any error (proposal service unreachable, non-2xx, proposal
// missing) is returned so the caller rejects the contract.
package proposallookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrNotFound is returned when the proposal service reports the proposal does not exist (404).
var ErrNotFound = errors.New("proposallookup: proposal not found")

// Proposal is the authoritative subset returned by the proposal service's internal endpoint.
type Proposal struct {
	ProposalID      string `json:"proposal_id"`
	ProjectID       string `json:"project_id"`
	ProjectClientID string `json:"project_client_id"`
	FreelancerID    string `json:"freelancer_id"`
	Status          string `json:"status"`
	BidAmountCents  int64  `json:"bid_amount_cents"`
	Currency        string `json:"currency"`
}

// Client calls GET {baseURL}/v1/internal/proposals/{id}.
type Client struct {
	baseURL string
	client  *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: baseURL, client: &http.Client{Timeout: 5 * time.Second}}
}

// Get resolves a proposal. It applies its own ~5s timeout and fails closed: a non-2xx response
// or transport error is an error, so the caller rejects the contract. A 404 maps to ErrNotFound.
func (c *Client) Get(ctx context.Context, proposalID string) (Proposal, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := fmt.Sprintf("%s/v1/internal/proposals/%s", c.baseURL, proposalID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Proposal{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return Proposal{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode == http.StatusNotFound {
		return Proposal{}, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Proposal{}, fmt.Errorf("proposallookup: lookup returned %d: %s", resp.StatusCode, string(body))
	}
	var p Proposal
	if err := json.Unmarshal(body, &p); err != nil {
		return Proposal{}, err
	}
	return p, nil
}
