package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/slipgraph/slipgraph/internal/audit"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/chat"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/config"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/platform/httpserver"
	"github.com/slipgraph/slipgraph/internal/platform/logging"
	"github.com/slipgraph/slipgraph/internal/realtime"
	"github.com/slipgraph/slipgraph/internal/risk"
)

// Run starts the process and drains HTTP requests before closing dependency pools.
// The worker dispatches authorized transactional audit and risk consumers.
func Run(name, defaultAddr string) int {
	logger := logging.New(os.Stdout, "info")
	cfg, err := config.Load(defaultAddr)
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	logger = logging.New(os.Stdout, cfg.LogLevel).With("service", name)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	pool, err := database.Open(startup, cfg.DatabaseURL)
	if err != nil {
		cancel()
		logger.Error("startup failed", "error", err)
		return 1
	}
	defer pool.Close()
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		cancel()
		logger.Error("startup failed", "error", err)
		return 1
	}
	opts.DialTimeout = 3 * time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = 2 * time.Second
	cache := redis.NewClient(opts)
	defer func() { _ = cache.Close() }()
	err = cache.Ping(startup).Err()
	cancel()
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	key, err := config.LoadAuthKey()
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	identity, err := auth.New(pool, key)
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	probes := []httpserver.Probe{pool.Ping, func(ctx context.Context) error { return cache.Ping(ctx).Err() }}
	broker := &realtime.Broker{Redis: cache}
	var handler http.Handler
	if name == "api" {
		endpoints, err := auth.NewHTTP(identity, config.PublicOrigin(), cfg.Environment == "production")
		if err != nil {
			logger.Error("startup failed", "error", err)
			return 1
		}
		endpoints.TrustedProxyHost = os.Getenv("TRUSTED_PROXY_HOST")
		gateway := realtime.New(ctx, endpoints, &chat.Store{Auth: identity}, broker)
		defer gateway.Shutdown()
		handler = httpserver.NewRealtimeAPI(logger, endpoints, gateway, probes...)
	} else {
		dispatcher := &events.Dispatcher{Pool: pool, Sessions: identity, Logger: logger, Consumers: []events.Consumer{{Name: "audit-v1", Handle: audit.Consume}, {Name: "risk-v1", Handle: risk.Consume}, {Name: "chat-fanout-v1", Handle: broker.Consume}}}
		done := make(chan struct{})
		go func() { defer close(done); dispatcher.Run(ctx) }()
		defer func() { stop(); <-done }()
		handler = httpserver.New(logger, probes...)
	}
	server := &http.Server{Addr: cfg.HTTPAddr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelError)}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	logger.Info("starting")
	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			return 1
		}
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			logger.Error("shutdown failed", "error", err)
			return 1
		}
		if err := <-result; !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "error", err)
			return 1
		}
	}
	logger.Info("stopped")
	return 0
}
