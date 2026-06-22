// Command server boots the payment service.
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/aizorix/platform/payment/internal/httpapi"
	"github.com/aizorix/platform/payment/internal/service"
	"github.com/aizorix/platform/payment/internal/store"
	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/pg"
)

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "payment", base.Environment)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pg.Connect(ctx, base.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	// The Stripe webhook signing secret authenticates inbound Stripe callbacks. Empty in
	// local/dev disables verification (see service.verifySignature). Fail CLOSED in
	// production: an empty secret there would silently accept forged webhooks.
	webhookSecret := config.Get("STRIPE_WEBHOOK_SECRET", "")
	if webhookSecret == "" && base.Environment == "production" {
		logger.Error("STRIPE_WEBHOOK_SECRET must be set in production")
		os.Exit(1)
	}
	// Select the Stripe client at runtime: a real (non-placeholder) STRIPE_SECRET_KEY wires the
	// live Stripe SDK; otherwise the in-process stub is used (dev/local). Decide ONCE here using
	// the same predicate NewWithStripe uses (service.RealStripeKey) so the log reflects what is
	// actually wired and we can fail closed below. Note a non-empty key is NOT enough: placeholders
	// like "changeme"/"sk_test_xxx" resolve to the stub.
	stripeSecret := config.Get("STRIPE_SECRET_KEY", "")
	liveStripe := service.RealStripeKey(stripeSecret)
	// Fail CLOSED in production: the stub mints fake pi_/ch_ ids while the flow still posts a real
	// 'escrow' credit to the ledger — escrow would be funded with no money collected. Empty or
	// placeholder keys must not silently run the stub in prod.
	if !liveStripe && base.Environment == "production" {
		logger.Error("STRIPE_SECRET_KEY must be a real key in production (stub client refuses to run)")
		os.Exit(1)
	}
	if liveStripe {
		logger.Info("stripe: using live client")
	} else {
		logger.Info("stripe: using stub client (STRIPE_SECRET_KEY unset or placeholder)")
	}
	svc := service.NewWithStripe(store.New(pool), webhookSecret, stripeSecret)
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(base.HTTPPort),
		Handler:           httpapi.New(svc, logger).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		logger.Info("payment http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}
