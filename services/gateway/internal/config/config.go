// Package config builds the gateway's runtime configuration from environment
// variables, layered on top of the shared pkg/config loader. It owns the upstream
// routing table (path prefix -> service base URL) and the security/identity
// parameters used to verify access tokens.
package config

import (
	"sort"
	"strings"
	"time"

	"github.com/aizorix/platform/pkg/config"
)

// Upstream is a single proxy target: requests whose path begins with Prefix are
// forwarded to BaseURL. Name is the logical service name (also the default host).
type Upstream struct {
	Name    string
	Prefix  string
	BaseURL string
}

// Config is the fully-resolved gateway configuration.
type Config struct {
	Base config.Base

	// HTTP server timeouts (match the auth service defaults).
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration

	// JWT verification.
	JWKSURL     string
	JWKSRefresh time.Duration
	Issuer      string
	Audience    string

	// Rate limiting.
	RedisAddr        string
	RateGeneralLimit int           // tokens per window for general routes
	RateAuthLimit    int           // tokens per window for /v1/auth/* routes
	RateWindow       time.Duration // refill window
	// TrustedProxies are CIDR ranges of L7 load balancers we sit behind. Only requests
	// whose immediate peer is in one of these have their X-Forwarded-For honored for the
	// rate-limit IP key. Empty (the default) means XFF is never trusted.
	TrustedProxies []string

	// Upstreams, ordered longest-prefix-first for deterministic matching.
	Upstreams []Upstream
}

// routeDef maps a public path prefix to a logical upstream service. The upstream's
// base URL defaults to http://<service>:8080 and is overridable via UPSTREAM_<SVC>.
type routeDef struct {
	prefix  string
	service string
}

// routingTable is the canonical public->internal map. Adding a service is a one-line
// change here. Several public prefixes intentionally fan in to the same service
// (e.g. /v1/sessions and /v1/screenshots both go to screenshot).
var routingTable = []routeDef{
	{"/v1/auth", "auth"},
	{"/v1/users", "user"},
	{"/v1/projects", "project"},
	{"/v1/proposals", "proposal"},
	{"/v1/contracts", "contract"},
	{"/v1/tracking", "timetracking"},
	{"/v1/screenshots", "screenshot"},
	{"/v1/sessions", "screenshot"},
	{"/v1/payments", "payment"},
	{"/v1/escrow", "escrow"},
	{"/v1/reviews", "review"},
	{"/v1/messages", "messaging"},
	{"/v1/conversations", "messaging"},
	{"/v1/notifications", "notification"},
	{"/v1/search", "search"},
	{"/v1/admin", "admin"},
	{"/v1/fraud", "fraud"},
	{"/v1/analytics", "analytics"},
}

// Load resolves the gateway configuration from the environment.
func Load() Config {
	base := config.LoadBase()

	ups := make([]Upstream, 0, len(routingTable))
	for _, rd := range routingTable {
		// Default host is the service name (k8s/compose DNS); port 8080.
		def := "http://" + rd.service + ":8080"
		envKey := "UPSTREAM_" + strings.ToUpper(rd.service)
		ups = append(ups, Upstream{
			Name:    rd.service,
			Prefix:  rd.prefix,
			BaseURL: config.Get(envKey, def),
		})
	}
	// Longest prefix first so the most specific route wins on lookup.
	sort.SliceStable(ups, func(i, j int) bool {
		return len(ups[i].Prefix) > len(ups[j].Prefix)
	})

	return Config{
		Base:              base,
		ReadHeaderTimeout: config.GetDuration("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       config.GetDuration("HTTP_READ_TIMEOUT", 30*time.Second),
		WriteTimeout:      config.GetDuration("HTTP_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:       config.GetDuration("HTTP_IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout:   config.GetDuration("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),

		JWKSURL:     config.Get("GATEWAY_JWKS_URL", "http://auth:8080/.well-known/jwks.json"),
		JWKSRefresh: config.GetDuration("GATEWAY_JWKS_REFRESH", 15*time.Minute),
		Issuer:      config.Get("JWT_ISSUER", "https://auth.aizorix.com"),
		Audience:    config.Get("JWT_AUDIENCE", "aizorix"),

		RedisAddr:        config.Get("REDIS_ADDR", base.RedisAddr),
		RateGeneralLimit: config.GetInt("RATE_LIMIT_GENERAL", 120),
		RateAuthLimit:    config.GetInt("RATE_LIMIT_AUTH", 10),
		RateWindow:       config.GetDuration("RATE_LIMIT_WINDOW", time.Minute),
		TrustedProxies:   splitCSV(config.Get("TRUSTED_PROXIES", "")),

		Upstreams: ups,
	}
}

// splitCSV splits a comma-separated env value into trimmed, non-empty tokens.
func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
