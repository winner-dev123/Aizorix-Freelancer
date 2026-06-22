// Command devpg runs a REAL PostgreSQL server as a subprocess WITHOUT Docker (via
// embedded-postgres, which downloads + runs the official PG binary). It exists so the stack
// can be exercised end-to-end in environments where the Docker daemon is unavailable.
//
//	go run ./cmd/devpg            # starts PG on :5432 (user=aizorix db=aizorix), blocks
//
// Connect with: postgres://aizorix:aizorix_dev@localhost:5432/aizorix?sslmode=disable
package main

import (
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

func main() {
	port := uint32(envInt("PG_PORT", 5432))
	cfg := embeddedpostgres.DefaultConfig().
		Username("aizorix").
		Password("aizorix_dev").
		Database("aizorix").
		Port(port)

	pg := embeddedpostgres.NewDatabase(cfg)
	log.Printf("starting embedded postgres on :%d (first run downloads the PG binary)...", port)
	if err := pg.Start(); err != nil {
		log.Fatalf("start: %v", err)
	}
	log.Printf("embedded postgres READY on :%d  (user=aizorix db=aizorix)", port)
	// Signal readiness to scripts watching for it.
	_ = os.WriteFile("devpg.ready", []byte("ok\n"), 0o644)

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	<-ch
	log.Println("stopping embedded postgres...")
	_ = pg.Stop()
	_ = os.Remove("devpg.ready")
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
