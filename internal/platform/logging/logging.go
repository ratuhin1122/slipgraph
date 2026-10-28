package logging

import (
	"context"
	"github.com/google/uuid"
	"io"
	"log/slog"
	"strings"
)

// Scrub protects structured attributes, nested groups, and message text. A strict
// allow-list prevents accidental logging of URLs, content, passwords, or tokens.
func Scrub(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindGroup {
		return a
	}
	switch a.Key {

	case "workspace_id", "run_id", "request_id":
		if a.Value.Kind() == slog.KindString {
			if id, e := uuid.Parse(a.Value.String()); e == nil && id != uuid.Nil {
				return a
			}
		}
	case "input_tokens", "output_tokens", "cost_microusd", "duration_ms":
		if a.Value.Kind() == slog.KindInt64 && a.Value.Int64() >= 0 {
			return a
		}
	case "status":
		if a.Value.Kind() == slog.KindInt64 && a.Value.Int64() >= 100 && a.Value.Int64() <= 599 {
			return a
		}
	case slog.TimeKey:
		if a.Value.Kind() == slog.KindTime {
			return a
		}
	case slog.LevelKey:
		if _, ok := a.Value.Any().(slog.Level); ok {
			return a
		}
	case "method":
		if a.Value.Kind() == slog.KindString {
			switch a.Value.String() {
			case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE":
				return a
			}
		}
	case "service":
		if a.Value.Kind() == slog.KindString {
			switch a.Value.String() {
			case "api", "worker", "migrate", "slipgraph":
				return a
			}
		}

	case slog.MessageKey:
		switch a.Value.String() {
		case "search indexing failed", "search request failed", "search worker failed", "model usage", "request", "starting", "stopped", "startup failed", "server failed", "shutdown failed", "panic recovered", "consumer failed":
			return a
		}
	}
	return slog.String(a.Key, "[REDACTED]")
}
func New(out io.Writer, level string) *slog.Logger {
	var l slog.Level
	switch strings.ToLower(level) {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: l, ReplaceAttr: Scrub}))
}

// Context is reserved for request-scoped logging without storing secrets.
func Context(ctx context.Context, logger *slog.Logger) *slog.Logger {
	return logger.With("request_id", RequestID(ctx))
}

type requestIDKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}
func RequestID(ctx context.Context) string { id, _ := ctx.Value(requestIDKey{}).(string); return id }
