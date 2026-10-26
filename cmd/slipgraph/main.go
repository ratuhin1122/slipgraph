package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/slipgraph/slipgraph/internal/audit"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/platform/config"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"os"
	"time"
)

func run() error {
	if len(os.Args) != 3 || os.Args[1] != "audit" || os.Args[2] != "verify" {
		return fmt.Errorf("usage: slipgraph audit verify (SLIPGRAPH_SESSION is required)")
	}
	cfg, err := config.Load(":8080")
	if err != nil {
		return fmt.Errorf("invalid configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	key, err := config.LoadAuthKey()
	if err != nil {
		return err
	}
	identity, err := auth.New(pool, key)
	if err != nil {
		return err
	}
	session, err := identity.Resolve(ctx, os.Getenv("SLIPGRAPH_SESSION"))
	if err != nil {
		return fmt.Errorf("valid admin session required")
	}
	var result audit.Result
	err = identity.InTenant(ctx, session, policy.AuditVerify, "", func(tx database.TenantTx, actor policy.Actor) error {
		var err error
		result, err = audit.Verify(ctx, tx, actor)
		return err
	})
	if err != nil {
		return fmt.Errorf("audit verification denied or failed")
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return err
	}
	if !result.Valid {
		return fmt.Errorf("audit chain is invalid")
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
