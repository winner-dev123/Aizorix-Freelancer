// Package config builds the wsgateway's runtime configuration on top of the shared
// pkg/config loader. It owns the WebSocket/keepalive tunables, the JWT verification
// parameters, the Redis pub/sub address, and the messaging-service URL used to persist
// inbound chat messages.
package config

import (
	"time"

	"github.com/aizorix/platform/pkg/config"
)

// Config is the fully-resolved wsgateway configuration.
type Config struct {
	Base config.Base

	// HTTP server timeouts. Note ReadTimeout/WriteTimeout are deliberately NOT applied to
	// the hijacked WebSocket connection (which lives far longer than any request); they only
	// bound the upgrade handshake and the plain HTTP endpoints (/healthz, /metrics, /presence).
	ReadHeaderTimeout time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration

	// JWT verification (same JWKS the gateway and other services use).
	JWKSURL     string
	JWKSRefresh time.Duration
	Issuer      string
	Audience    string

	// Redis pub/sub + presence.
	RedisAddr string

	// Messaging service base URL for best-effort message persistence.
	MessagingURL string

	// WebSocket keepalive + buffering.
	PingInterval   time.Duration // how often we send a ping to the client
	PongWait       time.Duration // max time to wait for a pong before closing
	WriteWait      time.Duration // deadline for a single write
	SendBuffer     int           // per-connection outbound queue depth (slow-client backpressure)
	MaxMessageSize int64         // max inbound frame size (bytes)

	// Presence key TTL; refreshed on every heartbeat. A client is "online" while its
	// presence key exists, so TTL should comfortably exceed PingInterval.
	PresenceTTL time.Duration

	// AllowedOrigins restricts the upgrade Origin header (CSWSH defense). The check is
	// default-DENY: an empty list rejects every cross-origin upgrade. Set an explicit "*"
	// entry to allow all origins (dev/local), or list the permitted origins for production.
	AllowedOrigins []string
}

// Load resolves the wsgateway configuration from the environment.
func Load() Config {
	base := config.LoadBase()
	return Config{
		Base:              base,
		ReadHeaderTimeout: config.GetDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		IdleTimeout:       config.GetDuration("HTTP_IDLE_TIMEOUT", 120*time.Second),
		ShutdownTimeout:   config.GetDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),

		JWKSURL:     config.Get("GATEWAY_JWKS_URL", "http://auth:8080/.well-known/jwks.json"),
		JWKSRefresh: config.GetDuration("GATEWAY_JWKS_REFRESH", 15*time.Minute),
		Issuer:      config.Get("JWT_ISSUER", "https://auth.aizorix.com"),
		Audience:    config.Get("JWT_AUDIENCE", "aizorix"),

		RedisAddr:    config.Get("REDIS_ADDR", base.RedisAddr),
		MessagingURL: config.Get("MESSAGING_URL", "http://messaging:8080"),

		PingInterval:   config.GetDuration("WS_PING_INTERVAL", 25*time.Second),
		PongWait:       config.GetDuration("WS_PONG_WAIT", 60*time.Second),
		WriteWait:      config.GetDuration("WS_WRITE_WAIT", 10*time.Second),
		SendBuffer:     config.GetInt("WS_SEND_BUFFER", 64),
		MaxMessageSize: int64(config.GetInt("WS_MAX_MESSAGE_SIZE", 1<<16)),

		PresenceTTL: config.GetDuration("WS_PRESENCE_TTL", 90*time.Second),

		AllowedOrigins: splitNonEmpty(config.Get("WS_ALLOWED_ORIGINS", "")),
	}
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := s[start:i]
			// trim surrounding spaces
			for len(part) > 0 && part[0] == ' ' {
				part = part[1:]
			}
			for len(part) > 0 && part[len(part)-1] == ' ' {
				part = part[:len(part)-1]
			}
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}
