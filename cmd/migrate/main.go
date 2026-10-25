package main

import (
	"context"
	"database/sql"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/slipgraph/slipgraph/internal/platform/config"
	"github.com/slipgraph/slipgraph/internal/platform/logging"
)

func run() int {
	logger := logging.New(os.Stdout, "info")
	cfg, err := config.Load(":8080")
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Migration bookkeeping is infrastructure metadata, isolated from tenant tables.
	provider, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS("db/migrations"), goose.WithTableName("migrations.goose_db_version"), goose.WithDisableGlobalRegistry(true))
	if err == nil {
		_, err = provider.Up(ctx)
	}
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	logger.Info("stopped")
	return 0
}
func main() { os.Exit(run()) }
