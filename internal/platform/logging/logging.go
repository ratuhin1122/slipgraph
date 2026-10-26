package logging

import (
	"context"
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
	case slog.TimeKey, slog.LevelKey, "request_id", "method", "status", "duration_ms", "service", "event":
		return a
	case slog.MessageKey:
		switch a.Value.String() {
		case "request", "starting", "stopped", "startup failed", "server failed", "shutdown failed", "panic recovered", "consumer failed":
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
