// Package middleware contains the cross-cutting HTTP middleware shared by every
// gateway route: panic recovery, request-id propagation, and structured access
// logging with metrics. These run outermost (before auth/rate-limit/proxy) so that
// even rejected requests are logged and counted.
package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/aizorix/platform/gateway/internal/observe"
	"github.com/google/uuid"
)

type ctxKey int

const ctxRequestID ctxKey = iota

const requestIDHeader = "X-Request-Id"

// statusRecorder captures the response status code and byte count for logging and
// metrics without buffering the body.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush proxies http.Flusher so streaming/SSE responses through the reverse proxy
// are not buffered by the recorder wrapper.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Recover converts a panic in any downstream handler into a 500 response and logs
// it, so a single bad request can never crash the gateway process.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("panic recovered",
						"err", rec,
						"path", r.URL.Path,
						"request_id", RequestIDFromContext(r.Context()),
					)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"code":"INTERNAL","message":"internal error"}`))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestID ensures every request carries a stable id: it reuses an inbound
// X-Request-Id when present (so traces span the whole call chain) or generates one,
// stores it on the context, and echoes it on both the upstream request and the
// client response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if id == "" {
			id = uuid.NewString()
		}
		r.Header.Set(requestIDHeader, id)
		w.Header().Set(requestIDHeader, id)
		ctx := context.WithValue(r.Context(), ctxRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the request id set by RequestID, if any.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// AccessLog emits one structured log line per request and records Prometheus
// metrics. The `route` label is resolved by routeOf (logical upstream name) to keep
// label cardinality bounded — never the raw path.
func AccessLog(logger *slog.Logger, m *observe.Metrics, routeOf func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}

			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}
			route := routeOf(r)
			dur := time.Since(start)
			status := statusText(rec.status)

			m.Requests.WithLabelValues(route, r.Method, status).Inc()
			m.Latency.WithLabelValues(route, r.Method, status).Observe(dur.Seconds())

			logger.Info("request",
				"request_id", RequestIDFromContext(r.Context()),
				"method", r.Method,
				"path", r.URL.Path,
				"route", route,
				"status", rec.status,
				"bytes", rec.bytes,
				"duration_ms", dur.Milliseconds(),
				"remote", r.RemoteAddr,
			)
		})
	}
}

func statusText(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}
