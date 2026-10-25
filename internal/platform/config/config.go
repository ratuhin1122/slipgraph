package config

import (
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment     string
	HTTPAddr        string
	DatabaseURL     string
	RedisURL        string
	LogLevel        string
	ShutdownTimeout time.Duration
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func secret(key, fileKey string) (string, error) {
	if p := os.Getenv(fileKey); p != "" {
		// Rooted access prevents a secret-file symlink escaping its configured directory.
		root, err := os.OpenRoot(filepath.Dir(p))
		if err != nil {
			return "", errors.New(fileKey + " directory cannot be read")
		}
		defer func() { _ = root.Close() }()
		b, err := root.ReadFile(filepath.Base(p))
		if err != nil {
			return "", errors.New(fileKey + " cannot be read")
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", errors.New(fileKey + " is empty")
		}
		return v, nil
	}
	return os.Getenv(key), nil
}
func Load(defaultAddr string) (Config, error) {
	c := Config{Environment: env("APP_ENV", "development"), HTTPAddr: env("HTTP_ADDR", defaultAddr), LogLevel: env("LOG_LEVEL", "info"), ShutdownTimeout: 10 * time.Second}
	if c.Environment != "development" && c.Environment != "production" && c.Environment != "test" {
		return c, errors.New("APP_ENV must be development, test, or production")
	}
	if _, port, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return c, errors.New("invalid HTTP_ADDR")
	} else if number, err := strconv.Atoi(port); err != nil || number < 1 || number > 65535 {
		return c, errors.New("invalid HTTP_ADDR port")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return c, errors.New("invalid LOG_LEVEL")
	}
	c.DatabaseURL = os.Getenv("DATABASE_URL")
	if c.DatabaseURL == "" {
		password, err := secret("DB_PASSWORD", "DB_PASSWORD_FILE")
		if err != nil {
			return c, err
		}
		if password == "" {
			return c, errors.New("DATABASE_URL or DB_PASSWORD_FILE is required")
		}
		u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(env("DB_HOST", "postgres"), env("DB_PORT", "5432")), Path: "/" + env("DB_NAME", "slipgraph"), User: url.UserPassword(env("DB_USER", "slipgraph_app"), password)}
		q := u.Query()
		q.Set("sslmode", env("DB_SSLMODE", "require"))
		u.RawQuery = q.Encode()
		c.DatabaseURL = u.String()
	}
	db, err := url.Parse(c.DatabaseURL)
	if err != nil || (db.Scheme != "postgres" && db.Scheme != "postgresql") || db.Hostname() == "" || db.User == nil || db.User.Username() == "" || db.Path == "" {
		return c, errors.New("invalid DATABASE_URL")
	}
	if c.Environment == "production" && db.Query().Get("sslmode") != "verify-full" {
		return c, errors.New("production database requires sslmode=verify-full")
	}
	c.RedisURL = os.Getenv("REDIS_URL")
	if c.RedisURL == "" {
		password, err := secret("REDIS_PASSWORD", "REDIS_PASSWORD_FILE")
		if err != nil {
			return c, err
		}
		if password == "" {
			return c, errors.New("REDIS_URL or REDIS_PASSWORD_FILE is required")
		}
		u := url.URL{Scheme: "redis", Host: net.JoinHostPort(env("REDIS_HOST", "redis"), env("REDIS_PORT", "6379")), Path: "/0", User: url.UserPassword("", password)}
		c.RedisURL = u.String()
	}
	r, err := url.Parse(c.RedisURL)
	if err != nil || (r.Scheme != "redis" && r.Scheme != "rediss") || r.Hostname() == "" {
		return c, errors.New("invalid REDIS_URL")
	}
	if c.Environment == "production" && r.Scheme != "rediss" {
		return c, errors.New("production Redis requires TLS")
	}
	if v := os.Getenv("SHUTDOWN_TIMEOUT_SECONDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 60 {
			return c, errors.New("invalid SHUTDOWN_TIMEOUT_SECONDS")
		}
		c.ShutdownTimeout = time.Duration(n) * time.Second
	}
	return c, nil
}
