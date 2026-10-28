package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedaction(t *testing.T) {
	var out bytes.Buffer
	logger := New(&out, "info")
	logger.Info("password=message-secret", "password", "password-secret", "database_url", "postgres://url-secret", "error", errors.New("token=error-secret"), "nested", slog.GroupValue(slog.String("token", "group-secret")))
	for _, secret := range []string{"message-secret", "password-secret", "url-secret", "error-secret", "group-secret"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	if !strings.Contains(out.String(), "[REDACTED]") {
		t.Fatal("missing redaction")
	}
}

func TestAllowedKeysCannotSmuggleSecretValues(t *testing.T) {
	var out bytes.Buffer
	l := New(&out, "info")
	for _, key := range []string{"workspace_id", "request_id", "run_id", "method", "status", "input_tokens", "output_tokens", "cost_microusd", "duration_ms", "service", "event", "time", "level"} {
		l.Info("request", key, "CANARY-SECRET", "nested", slog.GroupValue(slog.String(key, "CANARY-SECRET")))
	}
	if strings.Contains(out.String(), "CANARY-SECRET") {
		t.Fatal("allow-listed key leaked untyped secret")
	}
}
