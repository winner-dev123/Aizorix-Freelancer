// This file provides the REAL Stripe-backed implementation of StripeClient, used in
// production when STRIPE_SECRET_KEY is configured. It is additive: the stub in service.go
// remains the default for dev/tests, and the StripeClient interface is unchanged.
//
// Selection happens in NewWithStripe / the cmd/server wiring: when a non-placeholder secret
// key is present we set stripe.Key and hand the service a *liveStripe; otherwise the stub is
// kept. Webhook signature verification is intentionally NOT moved here — the service-level
// verifySignature remains the source of truth (HandleWebhook), though the Stripe SDK's
// webhook.ConstructEvent is available as an opt-in alternative (see VerifyWithSDK).
package service

import (
	stripe "github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/paymentintent"
	"github.com/stripe/stripe-go/v79/webhook"
)

// liveStripe is the production StripeClient. It is stateless: the secret key is set on the
// package-global stripe.Key (the SDK's documented configuration mechanism) by the constructor
// wiring, so this type only forwards calls to the SDK.
type liveStripe struct{}

// newLiveStripe configures the Stripe SDK with the given secret key and returns a live client.
// Callers are responsible for deciding whether to use it (see realStripeKey).
func newLiveStripe(secretKey string) liveStripe {
	stripe.Key = secretKey
	return liveStripe{}
}

// CreateIntent creates a real Stripe PaymentIntent and returns its id and client secret. It
// matches the StripeClient interface signature exactly. The idempotencyKey, when non-empty, is
// passed through to Stripe via Params.IdempotencyKey (the Idempotency-Key header) so retries do
// not create duplicate intents — the same guarantee the service relies on at the DB layer.
func (liveStripe) CreateIntent(amountCents int64, currency, idempotencyKey string) (string, string, error) {
	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(amountCents),
		Currency: stripe.String(stripeCurrency(currency)),
	}
	// Enable automatic payment methods so the intent is usable from the frontend without the
	// caller having to enumerate payment method types.
	params.AutomaticPaymentMethods = &stripe.PaymentIntentAutomaticPaymentMethodsParams{
		Enabled: stripe.Bool(true),
	}
	if idempotencyKey != "" {
		params.SetIdempotencyKey(idempotencyKey)
	}
	pi, err := paymentintent.New(params)
	if err != nil {
		return "", "", err
	}
	return pi.ID, pi.ClientSecret, nil
}

// stripeCurrency normalizes a currency code to the lowercase ISO form Stripe expects
// (e.g. "USD" -> "usd"). Empty defaults to usd.
func stripeCurrency(currency string) string {
	if currency == "" {
		return "usd"
	}
	out := make([]byte, len(currency))
	for i := 0; i < len(currency); i++ {
		c := currency[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

// VerifyWithSDK is an optional alternative to the service-level verifySignature: it validates a
// webhook using the Stripe SDK's webhook.ConstructEvent (which performs the same HMAC-SHA256
// scheme plus tolerance check). It is NOT wired into HandleWebhook by default — the in-house
// verifySignature stays the source of truth — but is provided so production can opt in via
// config if desired. It returns nil on a valid signature.
func VerifyWithSDK(payload []byte, signatureHeader, secret string) error {
	_, err := webhook.ConstructEvent(payload, signatureHeader, secret)
	return err
}

// compile-time assertion that liveStripe satisfies the StripeClient interface.
var _ StripeClient = liveStripe{}
