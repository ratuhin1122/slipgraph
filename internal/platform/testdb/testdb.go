//go:build integration

// Package testdb creates an isolated real Postgres database for integration tests.
package testdb

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type Fixture struct {
	Pool     *pgxpool.Pool
	Admin    *pgx.Conn
	DSN      string
	AdminDSN string
}

func New(t *testing.T) *Fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("repository root not found")
		}
		root = parent
	}
	passwords := map[string]string{"postgres": uuid.NewString(), "owner": uuid.NewString(), "app": uuid.NewString()}
	files := []testcontainers.ContainerFile{{HostFilePath: filepath.Join(root, "deploy/postgres-init.sh"), ContainerFilePath: "/docker-entrypoint-initdb.d/10-roles.sh", FileMode: 0o644}}
	for _, name := range []string{"owner", "app"} {
		files = append(files, testcontainers.ContainerFile{Reader: strings.NewReader(passwords[name]), ContainerFilePath: "/run/" + name + "/" + name + "_password", FileMode: 0o444})
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "pgvector/pgvector:pg16", ExposedPorts: []string{"5432/tcp"}, Env: map[string]string{"POSTGRES_PASSWORD": passwords["postgres"], "POSTGRES_DB": "slipgraph", "POSTGRES_USER": "postgres"}, Files: files, WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute)}, Started: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := container.Terminate(cleanup); err != nil {
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
		return fmt.Sprintf("postgres://%s:%s@%s:%s/slipgraph?sslmode=disable", role, passwords[strings.TrimPrefix(role, "slipgraph_")], host, port.Port())
	}
	owner, err := sql.Open("pgx", dsn("slipgraph_owner"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, owner, os.DirFS(filepath.Join(root, "db/migrations")), goose.WithTableName("migrations.goose_db_version"), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Open(ctx, dsn("slipgraph_app"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	admin, err := pgx.Connect(ctx, dsn("postgres"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	return &Fixture{Pool: pool, Admin: admin, DSN: dsn("slipgraph_app"), AdminDSN: dsn("postgres")}
}
