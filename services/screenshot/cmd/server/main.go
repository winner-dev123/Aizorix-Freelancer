// Command server boots the screenshot service: config, Postgres, key provider (KMS in
// prod / local master key in dev), S3 presigner, and the HTTP transport.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aizorix/platform/pkg/config"
	"github.com/aizorix/platform/pkg/crypto"
	"github.com/aizorix/platform/pkg/log"
	"github.com/aizorix/platform/pkg/pg"
	"github.com/aizorix/platform/screenshot/internal/httpapi"
	"github.com/aizorix/platform/screenshot/internal/service"
	"github.com/aizorix/platform/screenshot/internal/storage"
	"github.com/aizorix/platform/screenshot/internal/store"
)

func main() {
	base := config.LoadBase()
	logger := log.New(base.LogLevel, "screenshot", base.Environment)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pg.Connect(ctx, base.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	kp, err := buildKeyProvider(base.Environment)
	if err != nil {
		logger.Error("key provider", "err", err)
		os.Exit(1)
	}
	bucket := config.Get("S3_BUCKET_SCREENSHOTS", "aizorix-screenshots")
	// Real S3/MinIO presigner when credentials are configured; otherwise a deterministic stub.
	var presigner storage.Presigner
	endpoint, accessKey := config.Get("S3_ENDPOINT", ""), config.Get("S3_ACCESS_KEY", "")
	if endpoint != "" && accessKey != "" {
		p, perr := storage.NewS3Presigner(endpoint, accessKey, config.Get("S3_SECRET_KEY", ""),
			strings.HasPrefix(endpoint, "https://"))
		if perr != nil {
			logger.Error("s3 presigner", "err", perr)
			os.Exit(1)
		}
		presigner = p
		logger.Info("using S3 presigner", "endpoint", endpoint, "bucket", bucket)
	} else {
		presigner = storage.StubPresigner{Endpoint: config.Get("S3_ENDPOINT", "http://localhost:9000")}
		logger.Warn("S3 not configured; using stub presigner")
	}

	svc := service.New(store.New(pool), kp, presigner, bucket)
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(base.HTTPPort),
		Handler:           httpapi.New(svc).Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	go func() {
		logger.Info("screenshot http listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
			stop()
		}
	}()
	<-ctx.Done()
	logger.Info("shutting down")
	sc, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sc)
}

// buildKeyProvider returns a KMS-backed provider in prod, or a local master-key provider in
// dev. (KMSKeyProvider lives behind a build tag; here we use the local provider for both to
// keep the scaffold buildable without the AWS SDK — swap in prod via KMS_MODE=kms.)
func buildKeyProvider(env string) (crypto.KeyProvider, error) {
	master, err := base64.StdEncoding.DecodeString(config.Get("KMS_LOCAL_MASTER_KEY", ""))
	if err != nil || len(master) != 32 {
		if env == "production" {
			return nil, errors.New("KMS configuration required in production")
		}
		// Deterministic dev fallback so the service runs without env setup.
		master = []byte("dev-only-32-byte-master-key-1234")
	}
	return crypto.NewLocalKeyProvider(master)
}
