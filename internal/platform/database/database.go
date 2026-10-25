package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	cfg.MaxConns = 10
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error { return VerifyRuntimeRole(ctx, conn) }
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("database connection failed")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, errors.New("database unavailable or unsafe runtime role")
	}
	return pool, nil
}

// VerifyRuntimeRole rejects privileged login/current roles, memberships that permit
// SET ROLE to other roles, database/schema/table ownership, and missing RLS.
func VerifyRuntimeRole(ctx context.Context, conn *pgx.Conn) error {
	var unsafe bool
	err := conn.QueryRow(ctx, `
 SELECT current_user <> 'slipgraph_app' OR session_user <> 'slipgraph_app'
 OR EXISTS (SELECT 1 FROM pg_roles r WHERE pg_has_role(session_user, r.oid, 'MEMBER')
   AND (r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication))
 OR EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member WHERE r.rolname=session_user)
 OR EXISTS (SELECT 1 FROM pg_database d WHERE d.datname = current_database() AND pg_has_role(session_user, d.datdba, 'MEMBER'))
 OR EXISTS (SELECT 1 FROM pg_namespace n WHERE n.nspname IN ('app', 'migrations') AND pg_has_role(session_user, n.nspowner, 'MEMBER'))
 OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg_toast%'
   AND c.relkind IN ('r','p') AND pg_has_role(session_user, c.relowner, 'MEMBER'))
 OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='app' AND c.relkind IN ('r','p') AND (NOT c.relrowsecurity OR NOT c.relforcerowsecurity))
 OR to_regclass('app.objects') IS NULL`).Scan(&unsafe)
	if err != nil {
		return errors.New("runtime role verification failed")
	}
	if unsafe {
		return errors.New("unsafe database role, ownership, or RLS configuration")
	}
	return nil
}

// WithTenant scopes the setting to the transaction. workspaceID MUST come from a
// verified session (or re-authorized job), never from a request DTO or URL.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, workspaceID uuid.UUID, fn func(pgx.Tx) error) error {
	if workspaceID == uuid.Nil {
		return errors.New("workspace is required")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return errors.New("begin tenant transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('app.workspace_id', $1, true)", workspaceID.String()); err != nil {
		return errors.New("set transaction tenant")
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tenant transaction: %w", err)
	}
	return nil
}
