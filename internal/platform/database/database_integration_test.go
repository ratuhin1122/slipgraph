//go:build integration

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/slipgraph/slipgraph/internal/platform/dbgen"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestTenantIsolationAndRoleSafety(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	passwords := map[string]string{"postgres": uuid.NewString(), "owner": uuid.NewString(), "app": uuid.NewString()}
	files := []testcontainers.ContainerFile{{HostFilePath: "../../../deploy/postgres-init.sh", ContainerFilePath: "/docker-entrypoint-initdb.d/10-roles.sh", FileMode: 0o644}}
	for _, name := range []string{"owner", "app"} {
		files = append(files, testcontainers.ContainerFile{Reader: strings.NewReader(passwords[name]), ContainerFilePath: "/run/" + name + "/" + name + "_password", FileMode: 0o444})
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image: "pgvector/pgvector:pg16", ExposedPorts: []string{"5432/tcp"}, Env: map[string]string{"POSTGRES_PASSWORD": passwords["postgres"], "POSTGRES_DB": "slipgraph", "POSTGRES_USER": "postgres"}, Files: files,
		WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
	}, Started: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Error(err)
		}
	})
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	dsn := func(role string) string {
		return fmt.Sprintf("postgres://slipgraph_%s:%s@%s:%s/slipgraph?sslmode=disable", role, passwords[role], host, port.Port())
	}
	adminDSN := fmt.Sprintf("postgres://postgres:%s@%s:%s/slipgraph?sslmode=disable", passwords["postgres"], host, port.Port())
	ownerDB, err := sql.Open("pgx", dsn("owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ownerDB.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, ownerDB, os.DirFS("../../../db/migrations"), goose.WithTableName("migrations.goose_db_version"), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	// Verify reversibility, then exercise the exact final migrated schema.
	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	for _, unsafeDSN := range []string{adminDSN, dsn("owner")} {
		if pool, err := Open(ctx, unsafeDSN); err == nil {
			pool.Close()
			t.Fatal("privileged runtime accepted")
		}
	}
	cfg, err := pgxpool.ParseConfig(dsn("app"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	verify := func(wantSafe bool) {
		t.Helper()
		c, err := pgx.Connect(ctx, dsn("app"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close(ctx) }()
		err = VerifyRuntimeRole(ctx, c)
		if (err == nil) != wantSafe {
			t.Fatalf("role safety: safe=%v err=%v", wantSafe, err)
		}
	}
	verify(true)
	if _, err := pool.Exec(ctx, `SELECT count(*) FROM migrations.goose_db_version`); err == nil {
		t.Fatal("runtime can read migration metadata")
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE app.forbidden(id uuid)`); err == nil {
		t.Fatal("runtime can create tables")
	}
	workspaces := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	ids := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	actor := uuid.Must(uuid.NewV7())
	for i, workspace := range workspaces {
		err = WithTenant(ctx, pool, workspace, func(tx TenantTx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'task',$3)`, ids[i], workspace, actor); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO app.links(id,workspace_id,from_object,to_object,link_type) VALUES($1,$2,$3,$3,'mentions')`, uuid.Must(uuid.NewV7()), workspace, ids[i]); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO app.events(id,workspace_id,object_id,actor_id,event_type,payload) VALUES($1,$2,$3,$4,'object.created','{}')`, uuid.Must(uuid.NewV7()), workspace, ids[i], actor)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, workspace := range workspaces {
		t.Run(fmt.Sprintf("workspace-%d", i), func(t *testing.T) {
			if err := WithTenant(ctx, pool, workspace, func(tx TenantTx) error {
				for _, table := range []string{"objects", "links", "events"} {
					var count int
					// Identifiers are an internal fixed allow-list, never request data.
					query := `SELECT count(*) FROM app.` + pgx.Identifier{table}.Sanitize()
					if err := tx.QueryRow(ctx, query).Scan(&count); err != nil {
						return err
					}
					if count != 1 {
						t.Fatalf("%s leaked rows: %d", table, count)
					}
				}
				queries := dbgen.New(tx)
				if _, err := queries.GetObject(ctx, dbgen.GetObjectParams{ID: pgtype.UUID{Bytes: ids[i], Valid: true}, WorkspaceID: pgtype.UUID{Bytes: workspace, Valid: true}}); err != nil {
					return err
				}
				if _, err := queries.GetObject(ctx, dbgen.GetObjectParams{ID: pgtype.UUID{Bytes: ids[1-i], Valid: true}, WorkspaceID: pgtype.UUID{Bytes: workspaces[1-i], Valid: true}}); !errors.Is(err, pgx.ErrNoRows) {
					t.Fatalf("generated query crossed tenant boundary: %v", err)
				}
				var count int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.objects WHERE id=$1 AND workspace_id=$2`, ids[1-i], workspaces[1-i]).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					t.Fatal("cross-tenant read")
				}
				result, err := tx.Exec(ctx, `UPDATE app.objects SET type='doc' WHERE id=$1 AND workspace_id=$2`, ids[1-i], workspaces[1-i])
				if err != nil {
					return err
				}
				if result.RowsAffected() != 0 {
					t.Fatal("cross-tenant update")
				}
				result, err = tx.Exec(ctx, `DELETE FROM app.links WHERE workspace_id=$1`, workspaces[1-i])
				if err != nil {
					return err
				}
				if result.RowsAffected() != 0 {
					t.Fatal("cross-tenant delete")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			err := WithTenant(ctx, pool, workspace, func(tx TenantTx) error {
				_, err := tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'task',$3)`, uuid.Must(uuid.NewV7()), workspaces[1-i], actor)
				return err
			})
			if err == nil {
				t.Fatal("cross-tenant write accepted")
			}
			err = WithTenant(ctx, pool, workspace, func(tx TenantTx) error {
				_, err := tx.Exec(ctx, `INSERT INTO app.links(id,workspace_id,from_object,to_object,link_type) VALUES($1,$2,$3,$4,'blocked_by')`, uuid.Must(uuid.NewV7()), workspace, ids[i], ids[1-i])
				return err
			})
			if err == nil {
				t.Fatal("cross-tenant link accepted")
			}
		})
	}
	// A reused pooled connection loses the tenant after both commit and rollback.
	for _, rollback := range []bool{false, true} {
		_ = WithTenant(ctx, pool, workspaces[0], func(TenantTx) error {
			if rollback {
				return fmt.Errorf("rollback requested")
			}
			return nil
		})
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM app.objects`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("tenant setting leaked from transaction")
		}
	}
	if err := WithTenant(ctx, pool, uuid.Nil, func(TenantTx) error { return nil }); err == nil {
		t.Fatal("missing workspace accepted")
	}
	var escaped TenantTx
	if err := WithTenant(ctx, pool, workspaces[0], func(tx TenantTx) error { escaped = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.Exec(ctx, "SELECT 1"); err == nil {
		t.Fatal("tenant capability survived its callback")
	}
	// Catalog checks detect future migration regressions and privileged memberships.
	for _, statement := range []string{
		`ALTER ROLE slipgraph_app BYPASSRLS`,
		`GRANT slipgraph_owner TO slipgraph_app`,
		`GRANT pg_read_server_files TO slipgraph_app`,
		`ALTER TABLE app.objects OWNER TO slipgraph_app`,
		`ALTER TABLE app.objects NO FORCE ROW LEVEL SECURITY`,
		`ALTER TABLE app.objects DISABLE ROW LEVEL SECURITY`,
	} {
		if _, err := admin.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
		verify(false)
		for _, reset := range []string{`ALTER ROLE slipgraph_app NOBYPASSRLS`, `REVOKE slipgraph_owner FROM slipgraph_app`, `REVOKE pg_read_server_files FROM slipgraph_app`, `ALTER TABLE app.objects OWNER TO slipgraph_owner`, `ALTER TABLE app.objects ENABLE ROW LEVEL SECURITY`, `ALTER TABLE app.objects FORCE ROW LEVEL SECURITY`} {
			if _, err := admin.Exec(ctx, reset); err != nil {
				t.Fatal(err)
			}
		}
		verify(true)
	}
	if runtime, err := Open(ctx, dsn("app")); err != nil {
		t.Fatal(err)
	} else {
		runtime.Close()
	}
}
