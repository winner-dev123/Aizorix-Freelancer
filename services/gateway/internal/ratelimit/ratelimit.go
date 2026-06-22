// Package ratelimit implements a distributed token-bucket rate limiter backed by
// Redis. The bucket arithmetic runs as a single atomic Lua script so concurrent
// gateway replicas share one accurate limit per key. The limiter is fail-open: if
// Redis is unreachable it logs a warning and allows the request, because a cache
// outage must not take down the whole platform's ingress.
package ratelimit

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aizorix/platform/gateway/internal/auth"
)

// tokenBucket is a classic refill-on-read token bucket. Each key stores the current
// token count and the last refill timestamp. KEYS[1] is the bucket key; ARGV are
// capacity, refill-rate (tokens/sec), now (unix seconds, float), and requested cost.
//
// Returns {allowed (1/0), remaining tokens, retry_after_seconds}.
const tokenBucket = `
local key      = KEYS[1]
local capacity = tonumber(ARGV[1])
local rate     = tonumber(ARGV[2])
local now      = tonumber(ARGV[3])
local cost     = tonumber(ARGV[4])

local data = redis.call("HMGET", key, "tokens", "ts")
local tokens = tonumber(data[1])
local ts = tonumber(data[2])
if tokens == nil then
  tokens = capacity
  ts = now
end

-- Refill based on elapsed time, capped at capacity.
local delta = math.max(0, now - ts)
tokens = math.min(capacity, tokens + delta * rate)

local allowed = 0
local retry = 0
if tokens >= cost then
  allowed = 1
  tokens = tokens - cost
else
  -- Seconds until enough tokens accumulate for one request.
  if rate > 0 then
    retry = math.ceil((cost - tokens) / rate)
  else
    retry = 1
  end
end

redis.call("HSET", key, "tokens", tokens, "ts", now)
-- Expire idle buckets a little after a full refill to reclaim memory.
local ttl = math.ceil(capacity / math.max(rate, 0.0001)) + 1
redis.call("EXPIRE", key, ttl)

return {allowed, math.floor(tokens), retry}
`

// Bucket describes a single rate-limit policy.
type Bucket struct {
	Capacity int           // burst capacity (also the per-window allowance)
	Window   time.Duration // time to refill `Capacity` tokens
}

func (b Bucket) ratePerSecond() float64 {
	if b.Window <= 0 {
		return float64(b.Capacity)
	}
	return float64(b.Capacity) / b.Window.Seconds()
}

// Limiter applies a stricter bucket to auth endpoints and a general bucket to
// everything else, keyed by authenticated user id when present and client IP
// otherwise.
type Limiter struct {
	rdb            *redis.Client
	script         *redis.Script
	general        Bucket
	auth           Bucket
	trustedProxies []*net.IPNet
	logger         *slog.Logger
}

// New builds a Limiter. A nil/empty addr disables Redis and the limiter fails open
// (useful for local runs without a cache). trustedProxies is a list of CIDR ranges for
// L7 load balancers we sit behind; only when the immediate peer (r.RemoteAddr) falls in
// one of these do we honor X-Forwarded-For. It defaults to empty (off), in which case
// the IP key is always derived from r.RemoteAddr and XFF is ignored (un-spoofable).
func New(addr string, general, authB Bucket, trustedProxies []string, logger *slog.Logger) *Limiter {
	var rdb *redis.Client
	if addr != "" {
		rdb = redis.NewClient(&redis.Options{Addr: addr})
	}
	return &Limiter{
		rdb:            rdb,
		script:         redis.NewScript(tokenBucket),
		general:        general,
		auth:           authB,
		trustedProxies: parseCIDRs(trustedProxies, logger),
		logger:         logger,
	}
}

// parseCIDRs turns CIDR strings into nets, skipping (and logging) any that don't parse.
func parseCIDRs(cidrs []string, logger *slog.Logger) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		} else if logger != nil {
			logger.Warn("ignoring invalid TRUSTED_PROXIES CIDR", "cidr", c, "err", err)
		}
	}
	return out
}

// Close releases the Redis connection pool.
func (l *Limiter) Close() error {
	if l.rdb == nil {
		return nil
	}
	return l.rdb.Close()
}

// Middleware enforces the rate limit. On limit it returns 429 with Retry-After.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l.rdb == nil {
			next.ServeHTTP(w, r)
			return
		}

		bucket, key := l.classify(r)
		allowed, retry, err := l.allow(r.Context(), key, bucket)
		if err != nil {
			// Fail open: a cache outage must not block ingress.
			l.logger.Warn("rate limiter unavailable; allowing request", "err", err, "key", key)
			next.ServeHTTP(w, r)
			return
		}
		if !allowed {
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"RATE_LIMITED","message":"too many requests"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// classify picks the bucket (auth vs general) and the limit key (user vs ip).
func (l *Limiter) classify(r *http.Request) (Bucket, string) {
	bucket := l.general
	if strings.HasPrefix(r.URL.Path, "/v1/auth") {
		bucket = l.auth
	}
	if uid, ok := auth.UserIDFromContext(r.Context()); ok {
		return bucket, "rl:user:" + uid
	}
	return bucket, "rl:ip:" + l.clientIP(r)
}

// allow runs the atomic token-bucket script and reports whether the request passes.
func (l *Limiter) allow(ctx context.Context, key string, b Bucket) (bool, int, error) {
	now := float64(time.Now().UnixNano()) / 1e9
	res, err := l.script.Run(ctx, l.rdb, []string{key},
		b.Capacity, b.ratePerSecond(), now, 1).Result()
	if err != nil {
		return false, 0, err
	}
	vals, ok := res.([]interface{})
	if !ok || len(vals) < 3 {
		return true, 0, nil // defensive: malformed reply, fail open
	}
	allowed, _ := vals[0].(int64)
	retry, _ := vals[2].(int64)
	return allowed == 1, int(retry), nil
}

// clientIP extracts the originating client address for the rate-limit key. By default it
// uses the host part of r.RemoteAddr (the immediate peer), which a client cannot forge.
// X-Forwarded-For is honored ONLY when that immediate peer is itself a configured trusted
// proxy — otherwise any client could spoof XFF to dodge its own limit or lock out a victim
// IP. With no trusted proxies configured, XFF is always ignored.
func (l *Limiter) clientIP(r *http.Request) string {
	peer := remoteHost(r.RemoteAddr)
	if l.peerTrusted(peer) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// The left-most token is the original client per the de-facto XFF convention.
			if i := strings.IndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
	}
	return peer
}

// peerTrusted reports whether the immediate peer IP falls within a configured trusted
// proxy CIDR. False (and thus XFF ignored) whenever no proxies are configured.
func (l *Limiter) peerTrusted(host string) bool {
	if len(l.trustedProxies) == 0 {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range l.trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// remoteHost strips the port from a RemoteAddr, tolerating a bare host.
func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
