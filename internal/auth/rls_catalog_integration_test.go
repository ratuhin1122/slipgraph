//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/slipgraph/slipgraph/internal/integrations"
	"github.com/slipgraph/slipgraph/internal/llm"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/proposals"
	"github.com/slipgraph/slipgraph/internal/search"
)

func TestEveryTenantTableRLSWithoutApplicationFilters(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	limits := proposals.DefaultLimits()
	limits.DailyTokens = 10000000
	limits.DailyCostMicroUSD = 1000000000
	for i, s := range f.sessions {
		r := proposals.Runner{Store: f.ps, Provider: llm.Demo{}, Limits: limits, Logger: f.logger}
		if e := r.Process(ctx, f.actors[i], f.objects[i].Extraction); e != nil {
			t.Fatal(e)
		}
		page, e := f.ps.List(ctx, s, nil)
		if e != nil {
			t.Fatal(e)
		}
		for _, p := range page.Items {
			if p.Kind == "record_decision" {
				if _, e = f.ps.Decide(ctx, s, p.ID, p.Version, p.PayloadHash, "", true); e != nil {
					t.Fatal(e)
				}
				if e = f.ps.Execute(ctx, f.actors[i], p.ID); e != nil {
					t.Fatal(e)
				}
			}
		}
		key := bytes.Repeat([]byte{7}, 32)
		now := time.Now()
		stamp := strconv.FormatInt(now.Unix(), 10)
		body := []byte("fixture")
		sig := securitySignature(key, s.WorkspaceID, "catalog", stamp, body)
		if e = f.identity.InConsumer(ctx, f.actors[i], func(tx database.TenantTx) error {
			return integrations.VerifyAndClaim(ctx, tx, f.actors[i], key, "fixture", "catalog", stamp, sig, body, now)
		}); e != nil {
			t.Fatal(e)
		}
	}
	indexer := search.Runner{Store: f.ss, Embedder: llm.MockEmbedder{}, Provider: llm.Demo{}, Logger: f.logger, DailyTokens: 10000000, DailyCostMicroUSD: 1000000000, CostPerToken: 1}
	if e := indexer.Index(ctx, f.actors[1], f.objects[1].Task); e != nil {
		t.Fatal(e)
	}
	rows, e := f.db.Admin.Query(ctx, `SELECT c.relname,c.relrowsecurity,c.relforcerowsecurity,has_table_privilege('slipgraph_app',c.oid,'SELECT') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='app' AND c.relkind IN ('r','p') ORDER BY c.relname`)
	if e != nil {
		t.Fatal(e)
	}
	type table struct {
		name             string
		rls, force, read bool
	}
	tables := []table{}
	for rows.Next() {
		var x table
		if e = rows.Scan(&x.name, &x.rls, &x.force, &x.read); e != nil {
			rows.Close()
			t.Fatal(e)
		}
		tables = append(tables, x)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		t.Fatal(e)
	}
	identityTables, e := f.db.Admin.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname='identity'`)
	if e != nil {
		t.Fatal(e)
	}
	approved := map[string]bool{"users": true, "credentials": true, "sessions": true, "memberships": true, "invites": true, "password_resets": true}
	for identityTables.Next() {
		var name string
		if e = identityTables.Scan(&name); e != nil {
			identityTables.Close()
			t.Fatal(e)
		}
		if !approved[name] {
			t.Error("unapproved global identity table", name)
		}
		delete(approved, name)
	}
	e = identityTables.Err()
	identityTables.Close()
	if e != nil || len(approved) != 0 {
		t.Fatal("identity exception changed", e, approved)
	}

	for _, tab := range tables {
		t.Run(tab.name, func(t *testing.T) {
			if !tab.rls || !tab.force {
				t.Fatal("ENABLE/FORCE RLS missing")
			}
			name := pgx.Identifier{"app", tab.name}.Sanitize()
			var foreign int64
			if e := f.db.Admin.QueryRow(ctx, "SELECT count(*) FROM "+name+" WHERE workspace_id=$1", f.sessions[0].WorkspaceID).Scan(&foreign); e != nil {
				t.Fatal(e)
			}
			if foreign == 0 {
				t.Fatal("tenant table lacks a real A fixture; add it before enabling this table")
			}
			e := database.WithTenant(ctx, f.db.Pool, f.sessions[1].WorkspaceID, func(tx database.TenantTx) error {
				result, e := tx.Query(ctx, "SELECT workspace_id FROM "+name)
				if e != nil {
					return e
				}
				defer result.Close()
				for result.Next() {
					var workspace uuid.UUID
					if e = result.Scan(&workspace); e != nil {
						return e
					}
					if workspace != f.sessions[1].WorkspaceID {
						t.Fatal("unfiltered SELECT leaked foreign rows")
					}
				}
				return result.Err()
			})
			if tab.read {
				if e != nil {
					t.Fatal(e)
				}
			} else {
				var pgerr *pgconn.PgError
				if !errors.As(e, &pgerr) || pgerr.Code != "42501" {
					t.Fatalf("restricted table readable: %v", e)
				}
			}
		})
	}
	// An application forgetting every WHERE workspace predicate still cannot read,
	// update, delete or insert a foreign generic object through the runtime role.
	if e := database.WithTenant(ctx, f.db.Pool, f.sessions[1].WorkspaceID, func(tx database.TenantTx) error {
		for _, query := range []string{`UPDATE app.objects SET updated_at=now() WHERE id=$1`, `DELETE FROM app.objects WHERE id=$1`} {
			tag, e := tx.Exec(ctx, query, f.objects[0].Task)
			if e != nil {
				return e
			}
			if tag.RowsAffected() != 0 {
				t.Fatal("unfiltered foreign mutation succeeded")
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	e = database.WithTenant(ctx, f.db.Pool, f.sessions[1].WorkspaceID, func(tx database.TenantTx) error {
		_, e := tx.Exec(ctx, `INSERT INTO app.objects(workspace_id,id,type,created_by) VALUES($1,$2,'task',$3)`, f.sessions[0].WorkspaceID, uuid.New(), f.sessions[1].User.ID)
		return e
	})
	var pgerr *pgconn.PgError
	if !errors.As(e, &pgerr) || pgerr.Code != "42501" {
		t.Fatalf("foreign INSERT not blocked by RLS: %v", e)
	}
	var invoker bool
	if e = f.db.Admin.QueryRow(ctx, `SELECT 'security_invoker=true'=ANY(reloptions) FROM pg_class WHERE oid='app.search_documents'::regclass`).Scan(&invoker); e != nil || !invoker {
		t.Fatal("search view can bypass caller RLS")
	}
}
