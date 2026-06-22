// Package log configures structured JSON logging (slog) with trace correlation.
// Logs are shipped to Loki via the container stdout collector; the trace_id field
// links a log line to its distributed trace in Tempo/Jaeger.
package log

import (
	"context"
	"log/slog"
	"os"
)

type ctxKey struct{}

func New(level, service, env string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h).With("service", service, "env", env)
}

// WithTrace attaches the active trace id so every log line is correlatable.
func WithTrace(ctx context.Context, l *slog.Logger) *slog.Logger {
	if tid, ok := ctx.Value(ctxKey{}).(string); ok && tid != "" {
		return l.With("trace_id", tid)
	}
	return l
}

func ContextWithTrace(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, traceID)
}
