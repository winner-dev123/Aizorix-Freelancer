// Package service implements payment business logic: creating Stripe PaymentIntents
// (idempotently), capturing funds into the double-entry ledger, and processing Stripe
// webhooks with signature verification and event de-duplication.
package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aizorix/platform/payment/internal/store"
	"github.com/aizorix/platform/pkg/outbox"
	"github.com/jackc/pgx/v5"
)

// ErrInvalidSignature is returned when a webhook fails HMAC verification.
var ErrInvalidSignature = errors.New("service: invalid stripe signature")

// StripeClient abstracts the Stripe payments API so it can be stubbed in dev/tests.
type StripeClient interface {
	// CreateIntent creates a PaymentIntent and returns its id and client secret.
	CreateIntent(amountCents int64, currency, idempotencyKey string) (id, clientSecret string, err error)
}

// stubStripe is the default in-process stand-in: it synthesizes plausible-looking ids
// instead of calling the real Stripe API. Swap for a real client in production wiring.
type stubStripe struct{ st *store.Store }

func (s stubStripe) CreateIntent(amountCents int64, currency, idempotencyKey string) (string, string, error) {
	return "pi_" + s.st.RandToken(12), "secret_" + s.st.RandToken(16), nil
}

type Service struct {
	store         *store.Store
	stripe        StripeClient
	webhookSecret string
}

// New builds the service with the default stub Stripe client. webhookSecret authenticates
// inbound webhooks; an empty secret disables verification (dev only).
func New(st *store.Store, webhookSecret string) *Service {
	return &Service{store: st, stripe: stubStripe{st: st}, webhookSecret: webhookSecret}
}

// NewWithStripe builds the service and selects the Stripe client at runtime: when secretKey is
// a real (non-empty, non-placeholder) key the REAL Stripe-backed client is used (and the SDK is
// configured via stripe.Key inside newLiveStripe); otherwise it falls back to the in-process
// stub. webhookSecret is handled identically to New. This keeps dev/tests on the stub while
// production wires the live client purely through config.
func NewWithStripe(st *store.Store, webhookSecret, secretKey string) *Service {
	svc := &Service{store: st, webhookSecret: webhookSecret}
	if RealStripeKey(secretKey) {
		svc.stripe = newLiveStripe(secretKey)
	} else {
		svc.stripe = stubStripe{st: st}
	}
	return svc
}

// RealStripeKey reports whether secretKey looks like a usable Stripe secret key rather than an
// unset value or an obvious placeholder. Stripe secret keys start with "sk_" (or restricted
// "rk_"); common placeholders are rejected so a misconfigured env doesn't accidentally hit the
// live API with junk. It is exported so the cmd/server wiring can make the same live-vs-stub
// decision NewWithStripe makes (e.g. to fail closed in production when only the stub would run).
func RealStripeKey(secretKey string) bool {
	if secretKey == "" {
		return false
	}
	switch secretKey {
	case "changeme", "placeholder", "sk_test_xxx", "sk_live_xxx", "xxx", "none":
		return false
	}
	return strings.HasPrefix(secretKey, "sk_") || strings.HasPrefix(secretKey, "rk_")
}

// CreateIntentResult is returned to the API/client to complete the payment on the frontend.
type CreateIntentResult struct {
	PaymentID             string `json:"payment_id"`
	StripePaymentIntentID string `json:"stripe_payment_intent_id"`
	ClientSecret          string `json:"client_secret"`
	Status                string `json:"status"`
}

// CreatePaymentIntent is idempotent on idempotencyKey: if a payment already exists for the
// key it is returned unchanged. Otherwise a PaymentIntent is created at Stripe (stubbed)
// and a payments row is inserted in status 'processing'.
func (s *Service) CreatePaymentIntent(ctx context.Context, payerID string, contractID *string, amountCents int64, currency, idempotencyKey string) (*CreateIntentResult, error) {
	if amountCents <= 0 {
		return nil, fmt.Errorf("service: amount_cents must be > 0")
	}
	if currency == "" {
		currency = "USD"
	}
	// Idempotency fast-path: look up an existing payment for this key.
	if idempotencyKey != "" {
		if existing, err := s.store.GetPaymentByIdempotencyKey(ctx, idempotencyKey); err == nil {
			return resultFromPayment(existing), nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}

	intentID, clientSecret, err := s.stripe.CreateIntent(amountCents, currency, idempotencyKey)
	if err != nil {
		return nil, err
	}

	var keyPtr *string
	if idempotencyKey != "" {
		keyPtr = &idempotencyKey
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	p, err := s.store.InsertPayment(ctx, tx, payerID, contractID, amountCents, currency, intentID, keyPtr)
	if errors.Is(err, store.ErrDuplicateKey) {
		// Lost an idempotency race: another request inserted first. Read it back.
		_ = tx.Rollback(ctx)
		if idempotencyKey != "" {
			if existing, gErr := s.store.GetPaymentByIdempotencyKey(ctx, idempotencyKey); gErr == nil {
				return resultFromPayment(existing), nil
			}
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &CreateIntentResult{
		PaymentID:             p.ID,
		StripePaymentIntentID: deref(p.StripePaymentIntentID),
		ClientSecret:          clientSecret,
		Status:                p.Status,
	}, nil
}

// ConfirmPayment captures the funding: it transitions the payment processing -> succeeded
// and, in the SAME transaction, writes the balanced double-entry deposit legs and emits a
// payment.captured event via the outbox.
func (s *Service) ConfirmPayment(ctx context.Context, paymentID string) (store.Payment, error) {
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return store.Payment{}, err
	}
	defer tx.Rollback(ctx)

	chargeID := "ch_" + s.store.RandToken(12)
	p, err := s.store.MarkSucceeded(ctx, tx, paymentID, chargeID)
	if err != nil {
		return store.Payment{}, err
	}
	if err := s.writeDepositLedger(ctx, tx, p); err != nil {
		return store.Payment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.Payment{}, err
	}
	return p, nil
}

// writeDepositLedger records a balanced funding capture.
//
// LEDGER OWNERSHIP: payment capture is the SINGLE authoritative cash-in posting for a real
// deposit. It moves money the whole way in one balanced group:
//
//	stripe_clearing -> client_funding -> escrow
//
// i.e. debit stripe_clearing (cash arriving from Stripe), and credit 'escrow' exactly once.
// The escrow service's FundEscrow must NOT re-post client_funding -> escrow (it would
// double-credit 'escrow' on the shared ledger); it only adjusts held_cents and records a
// non-monetary escrow_hold marker. This keeps exactly one credit to the 'escrow' account
// per real deposit. All legs share one txn_group and sum to zero.
func (s *Service) writeDepositLedger(ctx context.Context, tx pgx.Tx, p store.Payment) error {
	group := s.store.NewUUID()
	pid := p.ID
	legs := []store.Leg{
		{Type: "deposit", AccountKind: "stripe_clearing", ContractID: p.ContractID, AmountCents: -p.AmountCents, Currency: p.Currency, PaymentID: &pid},
		{Type: "deposit", AccountKind: "client_funding", ContractID: p.ContractID, AmountCents: p.AmountCents, Currency: p.Currency, PaymentID: &pid},
		{Type: "deposit", AccountKind: "client_funding", ContractID: p.ContractID, AmountCents: -p.AmountCents, Currency: p.Currency, PaymentID: &pid},
		{Type: "deposit", AccountKind: "escrow", ContractID: p.ContractID, AmountCents: p.AmountCents, Currency: p.Currency, PaymentID: &pid},
	}
	if err := s.store.WriteLegs(ctx, tx, group, legs); err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, outbox.Event{
		AggregateType: "payment", AggregateID: p.ID, EventType: "payment.captured",
		Topic: "payment.events", PartitionKey: p.ID,
		Payload: map[string]any{
			"payment_id": p.ID, "contract_id": p.ContractID, "amount_cents": p.AmountCents,
			"currency": p.Currency, "payer_id": p.PayerID,
		},
	})
}

// stripeEvent is the minimal envelope we parse from a webhook body.
type stripeEvent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Data struct {
		Object struct {
			ID string `json:"id"`
		} `json:"object"`
	} `json:"data"`
}

// HandleWebhook verifies the Stripe signature, de-duplicates the event, and processes
// supported event types. It is safe to call repeatedly for the same event (idempotent).
func (s *Service) HandleWebhook(ctx context.Context, payload []byte, signatureHeader string) error {
	if err := verifySignature(payload, signatureHeader, s.webhookSecret); err != nil {
		return err
	}
	var ev stripeEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Errorf("service: parse webhook: %w", err)
	}
	if ev.ID == "" {
		return fmt.Errorf("service: webhook missing event id")
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// De-dup: first writer wins. If the row already existed, the event was processed.
	fresh, err := s.store.InsertStripeEvent(ctx, tx, ev.ID, ev.Type, payload)
	if err != nil {
		return err
	}
	if !fresh {
		_ = tx.Rollback(ctx)
		return nil // already processed
	}

	switch ev.Type {
	case "payment_intent.succeeded":
		p, err := s.store.GetPaymentByIntent(ctx, tx, ev.Data.Object.ID)
		if errors.Is(err, store.ErrNotFound) {
			// The payments row for this intent hasn't been created yet (race between Stripe's
			// webhook and our CreatePaymentIntent commit). Return an error so the tx rolls
			// back: the stripe_events row is NOT recorded as processed, and Stripe retries
			// later — otherwise the deposit would never be written.
			return fmt.Errorf("service: payment not found for intent %s: %w", ev.Data.Object.ID, err)
		}
		if err != nil {
			return err
		}
		if p.Status == "processing" {
			chargeID := "ch_" + s.store.RandToken(12)
			marked, err := s.store.MarkSucceeded(ctx, tx, p.ID, chargeID)
			if err != nil {
				return err
			}
			if err := s.writeDepositLedger(ctx, tx, marked); err != nil {
				return err
			}
		}
	default:
		// Unhandled event types are recorded (de-duped) and acknowledged.
	}

	if err := s.store.MarkStripeEventProcessed(ctx, tx, ev.ID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) GetPayment(ctx context.Context, id string) (store.Payment, error) {
	return s.store.GetPayment(ctx, id)
}

func (s *Service) ListPaymentsForContract(ctx context.Context, contractID string) ([]store.Payment, error) {
	return s.store.ListPaymentsForContract(ctx, contractID)
}

// webhookTolerance is the maximum accepted clock skew between the signature timestamp and
// now. It bounds replay of an old, validly-signed payload (Stripe's default is 5 minutes).
const webhookTolerance = 300 * time.Second

// verifySignature implements Stripe's webhook scheme: the header is `t=<ts>,v1=<sig>` and
// the signed payload is `<ts>.<rawBody>`, HMAC-SHA256'd with the endpoint secret. An empty
// secret skips verification (dev only — production wiring fails closed before reaching here,
// see main.go). The `t=` value is parsed as unix seconds and the request is rejected when it
// is outside webhookTolerance to defend against replay.
func verifySignature(payload []byte, header, secret string) error {
	if secret == "" {
		return nil // dev: verification disabled
	}
	var ts string
	// Collect ALL v1 signatures: during endpoint-secret rotation Stripe signs the payload with
	// each active secret and sends one v1=<sig> per secret in the header. Keeping only the last
	// would reject legitimate webhooks signed with the other secret.
	var v1s []string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			ts = kv[1]
		case "v1":
			v1s = append(v1s, kv[1])
		}
	}
	if ts == "" || len(v1s) == 0 {
		return ErrInvalidSignature
	}
	// Reject stale/future timestamps to prevent replay of an old signed body.
	tsUnix, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrInvalidSignature
	}
	skew := time.Since(time.Unix(tsUnix, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > webhookTolerance {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	// Accept if ANY provided v1 matches, using constant-time comparison against each.
	for _, v1 := range v1s {
		if hmac.Equal([]byte(expected), []byte(v1)) {
			return nil
		}
	}
	return ErrInvalidSignature
}

func resultFromPayment(p store.Payment) *CreateIntentResult {
	return &CreateIntentResult{
		PaymentID:             p.ID,
		StripePaymentIntentID: deref(p.StripePaymentIntentID),
		ClientSecret:          "", // client secret is not persisted; re-fetch from Stripe if needed
		Status:                p.Status,
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
