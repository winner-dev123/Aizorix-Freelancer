// Package proxy builds the gateway's request router: it maps public path prefixes
// to upstream reverse proxies, distinguishes public from protected routes, and wires
// the full middleware chain (recover -> request-id -> access-log -> strip-headers ->
// [public: rate-limit -> proxy] / [protected: auth -> rate-limit -> proxy]). It is the
// heart of the gateway. Protected routes authenticate before rate-limiting so the
// limiter can key on the authenticated user id, not just the client IP.
package proxy

import (
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/aizorix/platform/gateway/internal/auth"
	"github.com/aizorix/platform/gateway/internal/config"
	"github.com/aizorix/platform/gateway/internal/middleware"
	"github.com/aizorix/platform/gateway/internal/observe"
	"github.com/aizorix/platform/gateway/internal/ratelimit"
)

// publicRoutes are exact method+path pairs that bypass JWT authentication. These are
// the unauthenticated surface of the platform (registration, login, token refresh,
// JWKS, the Stripe webhook, and the health probe). Everything else requires a token.
type route struct {
	method string
	path   string
}

var publicRoutes = map[route]struct{}{
	{http.MethodPost, "/v1/auth/register"}:           {},
	{http.MethodPost, "/v1/auth/login"}:              {},
	{http.MethodPost, "/v1/auth/refresh"}:            {},
	{http.MethodPost, "/v1/auth/logout"}:             {},
	{http.MethodGet, "/.well-known/jwks.json"}:       {},
	{http.MethodPost, "/v1/payments/webhook/stripe"}: {},
	{http.MethodGet, "/healthz"}:                     {},
}

// isPublic reports whether a request may proceed without authentication.
func isPublic(r *http.Request) bool {
	_, ok := publicRoutes[route{r.Method, r.URL.Path}]
	return ok
}

// upstreamProxy is a compiled reverse proxy for one logical service.
type upstreamProxy struct {
	name   string
	prefix string
	proxy  *httputil.ReverseProxy
}

// Router resolves an inbound request to its upstream and serves it. It also answers
// the public JWKS path by proxying to the auth service.
type Router struct {
	upstreams []upstreamProxy
	logger    *slog.Logger
}

// jwksUpstream is the synthetic prefix used to route the public /.well-known/jwks.json
// path to the auth service (which actually serves the key set).
const jwksPath = "/.well-known/jwks.json"

// NewRouter compiles a reverse proxy per configured upstream.
func NewRouter(cfg config.Config, logger *slog.Logger) (*Router, error) {
	ups := make([]upstreamProxy, 0, len(cfg.Upstreams))
	var authBase string
	for _, u := range cfg.Upstreams {
		target, err := url.Parse(u.BaseURL)
		if err != nil {
			return nil, err
		}
		if u.Name == "auth" {
			authBase = u.BaseURL
		}
		ups = append(ups, upstreamProxy{
			name:   u.Name,
			prefix: u.Prefix,
			proxy:  newReverseProxy(target, logger),
		})
	}
	// Route the well-known JWKS path to auth. Place it first so it matches before the
	// generic /v1/* prefixes (it can't anyway — different prefix — but be explicit).
	if authBase != "" {
		if target, err := url.Parse(authBase); err == nil {
			ups = append([]upstreamProxy{{
				name:   "auth",
				prefix: jwksPath,
				proxy:  newReverseProxy(target, logger),
			}}, ups...)
		}
	}
	return &Router{upstreams: ups, logger: logger}, nil
}

// match returns the upstream serving the request path, or nil if none.
func (r *Router) match(path string) *upstreamProxy {
	for i := range r.upstreams {
		if matchPrefix(path, r.upstreams[i].prefix) {
			return &r.upstreams[i]
		}
	}
	return nil
}

// RouteName returns the logical upstream name for a request (used as the metrics
// route label), or "unmatched" / "internal" for non-proxied paths.
func (r *Router) RouteName(req *http.Request) string {
	switch req.URL.Path {
	case "/healthz", "/metrics":
		return "internal"
	}
	if u := r.match(req.URL.Path); u != nil {
		return u.name
	}
	return "unmatched"
}

// ServeHTTP forwards the request to the matched upstream or returns 404.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	u := r.match(req.URL.Path)
	if u == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"NOT_FOUND","message":"no route for path"}`))
		return
	}
	u.proxy.ServeHTTP(w, req)
}

// matchPrefix matches a path against a route prefix on path-segment boundaries, so
// "/v1/users" matches "/v1/users" and "/v1/users/123" but not "/v1/userspace".
func matchPrefix(path, prefix string) bool {
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+"/")
}

// newReverseProxy builds a ReverseProxy that rewrites the request to the upstream
// while preserving method, path, query, body, and most headers. It sets
// X-Forwarded-* and a clean Host, and centralises upstream-error handling.
func newReverseProxy(target *url.URL, logger *slog.Logger) *httputil.ReverseProxy {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)         // scheme+host+base path of the upstream
			pr.Out.Host = target.Host // upstreams expect their own Host
			pr.SetXForwarded()        // X-Forwarded-For/Proto/Host from the inbound req
		},
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   20,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
		ErrorHandler: func(w http.ResponseWriter, req *http.Request, err error) {
			logger.Error("upstream error",
				"target", target.String(),
				"path", req.URL.Path,
				"err", err,
			)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":"UPSTREAM_UNAVAILABLE","message":"upstream service unavailable"}`))
		},
	}
	return rp
}

// Handler assembles the complete gateway handler tree and middleware chain.
//
// Order (outermost first):
//
//	recover -> request-id -> access-log -> [strip spoofable headers] -> dispatch:
//	  public route:    rate-limit (by IP)        -> proxy
//	  protected route: auth -> rate-limit (by user) -> proxy
//
// /healthz and /metrics are served directly and skip auth/rate-limit/proxy.
func Handler(
	router *Router,
	verifier *auth.Verifier,
	limiter *ratelimit.Limiter,
	metrics *observe.Metrics,
	logger *slog.Logger,
) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/metrics", metrics.Handler())

	// Everything else flows through the proxy pipeline.
	mux.Handle("/", proxyPipeline(router, verifier, limiter))

	// Cross-cutting middleware wraps the whole mux.
	chain := middleware.Recover(logger)(
		middleware.RequestID(
			middleware.AccessLog(logger, metrics, router.RouteName)(mux),
		),
	)
	return chain
}

// proxyPipeline builds the per-request pipeline for proxied traffic: strip forged
// identity headers, then branch on public/protected.
//
//   - Public routes: an IP-keyed rate limit (the only key available pre-auth) then proxy.
//   - Protected routes: authenticate FIRST so the user id is on the context, THEN apply
//     the rate limit. This is what makes the per-user policy real — the limiter's
//     classify() only finds a user id once verifier.Middleware has populated it, so the
//     user-keyed limit must run inside the protected branch, after auth.
func proxyPipeline(router *Router, verifier *auth.Verifier, limiter *ratelimit.Limiter) http.Handler {
	// The terminal handler is the reverse-proxy router.
	proxied := http.Handler(router)

	// Protected branch: auth populates the user id on the context, then a per-user rate limit
	// keys on it. Order is verify -> user-keyed rate-limit -> proxy. (classify finds the user
	// id only after verifier.Middleware has set it, which is why this limit lives post-auth.)
	protected := verifier.Middleware(limiter.Middleware(proxied))

	// Public branch: no per-user identity exists; the up-front IP-keyed limit below is its gate.
	public := proxied

	// Branch per request because public/protected is decided by method+path, not mount point.
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublic(r) {
			public.ServeHTTP(w, r)
			return
		}
		protected.ServeHTTP(w, r)
	})

	// Up-front IP-keyed rate limit on EVERY request: pre-auth, classify() has no user id so it
	// keys by IP. This gates public routes AND protects the auth path itself from a token-flood
	// DoS (without it, an attacker could force a JWT verification per request, unmetered).
	// Protected routes additionally get the per-user limit after auth (above).
	ipLimited := limiter.Middleware(dispatch)

	// Strip any client-supplied trusted identity headers first of all — public and protected
	// alike, before anything else runs — so nothing downstream can be spoofed.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.StripTrustedHeaders(r)
		ipLimited.ServeHTTP(w, r)
	})
}
