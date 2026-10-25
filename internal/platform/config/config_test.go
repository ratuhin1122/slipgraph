package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func base(t *testing.T) {
	t.Helper()
	for _, key := range []string{"APP_ENV", "HTTP_ADDR", "LOG_LEVEL", "DB_PASSWORD_FILE", "REDIS_PASSWORD_FILE", "SHUTDOWN_TIMEOUT_SECONDS"} {
		t.Setenv(key, "")
	}
	t.Setenv("DATABASE_URL", "postgres://slipgraph_app:test@localhost/slipgraph?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://:test@localhost:6379/0")
}
func TestValidation(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"DATABASE_URL", "secret-invalid"}, {"REDIS_URL", "https://localhost"}, {"APP_ENV", "unknown"}, {"HTTP_ADDR", "invalid"}, {"LOG_LEVEL", "trace"}, {"SHUTDOWN_TIMEOUT_SECONDS", "0"}, {"APP_ENV", "production"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			base(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load(":8080")
			if err == nil {
				t.Fatal("invalid configuration accepted")
			}
			if strings.Contains(err.Error(), "secret-invalid") {
				t.Fatal("secret leaked")
			}
		})
	}
}
func TestValidConfig(t *testing.T) {
	base(t)
	c, err := Load(":8080")
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" {
		t.Fatal(c.HTTPAddr)
	}
}
func TestProductionTLS(t *testing.T) {
	base(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DATABASE_URL", "postgres://slipgraph_app:test@localhost/slipgraph?sslmode=verify-full")
	t.Setenv("REDIS_URL", "rediss://:test@localhost:6379/0")
	if _, err := Load(":8080"); err != nil {
		t.Fatal(err)
	}
}

func TestSecretFile(t *testing.T) {
	base(t)
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("file-password?%&\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_PASSWORD_FILE", path)
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_SSLMODE", "disable")
	c, err := Load(":8080")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(c.DatabaseURL)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	if password != "file-password?%&" {
		t.Fatal("secret was not trimmed and URL-encoded correctly")
	}
	t.Setenv("DB_PASSWORD_FILE", filepath.Join(t.TempDir(), "missing"))
	if _, err := Load(":8080"); err == nil {
		t.Fatal("missing secret file accepted")
	}
}
