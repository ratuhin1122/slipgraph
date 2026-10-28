//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/slipgraph/slipgraph/internal/platform/httpserver"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/llm"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/platform/testdb"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/search"
)

// This opt-in benchmark uses an isolated testcontainer, never a user's database.
func TestSearchBenchmark(t *testing.T) {
	if os.Getenv("SEARCH_BENCH") != "1" {
		t.Skip("run scripts/search_benchmark.py for the 100k benchmark")
	}
	f := testdb.New(t)
	ctx := context.Background()
	identity, e := auth.New(f.Pool, bytes.Repeat([]byte{2}, 32))
	if e != nil {
		t.Fatal(e)
	}
	// Auth's public workflow supplies the verified workspace used by tenant seeding.
	endpoints, e := auth.NewHTTP(identity, "http://localhost:5173", false)
	if e != nil {
		t.Fatal(e)
	}
	b, session := searchBenchmarkSignup(t, endpoints)
	_ = b
	if _, e = f.Admin.Exec(ctx, `DROP INDEX app.embeddings_hnsw`); e != nil {
		t.Fatal(e)
	}
	workspace := session.WorkspaceID
	project := uuid.New()
	model := (llm.MockEmbedder{}).Model()
	seed := func(tx database.TenantTx, a policy.Actor) error {
		// Seeding/indexing intentionally has a larger local statement limit, never runtime queries.
		if _, e := tx.Exec(ctx, `SET LOCAL statement_timeout='5min'`); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO app.objects(workspace_id,id,type,created_by) VALUES($1,$2,'project',$3)`, workspace, project, a.ID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO app.projects(workspace_id,id,name,start_at) VALUES($1,$2,'Synthetic benchmark',now())`, workspace, project); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO app.objects(workspace_id,id,type,created_by) SELECT $1,public.gen_random_uuid(),'task',$2 FROM generate_series(1,100000)`, workspace, a.ID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO app.tasks(workspace_id,id,title,description,status,start_at,due_at,estimate_minutes) SELECT workspace_id,id,'Benchmark task '||row_number() OVER(ORDER BY id),'Synthetic release API roadmap category '||(abs(hashtext(id::text)::bigint)%100)::text,'todo',now(),now()+interval '1 day',60 FROM app.objects WHERE workspace_id=$1 AND type='task'`, workspace); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO app.links(workspace_id,id,from_object,to_object,link_type) SELECT workspace_id,public.gen_random_uuid(),id,$2,'belongs_to' FROM app.tasks WHERE workspace_id=$1`, workspace, project); e != nil {
			return e
		}
		// 100 deterministic embedding buckets. No provider calls or quota bypass on real data.
		literals := []string{}
		for i := 0; i < 100; i++ {
			vectors, e := (llm.MockEmbedder{}).Embed(ctx, []string{fmt.Sprintf("Synthetic release API roadmap category %d", i)})
			if e != nil {
				return e
			}
			literal, e := json.Marshal(vectors[0])
			if e != nil {
				return e
			}
			literals = append(literals, string(literal))
		}
		if _, e := tx.Exec(ctx, `INSERT INTO app.embeddings(workspace_id,object_id,chunk_index,chunk,vector,model,content_hash) SELECT d.workspace_id,d.id,0,d.content,v.vector::public.vector,$3,d.content_hash FROM app.search_documents d JOIN unnest($2::text[]) WITH ORDINALITY AS v(vector,category) ON abs(hashtext(d.id::text)::bigint)%100=v.category-1 WHERE d.workspace_id=$1 AND d.type='task'`, workspace, literals, model); e != nil {
			return e
		}

		return nil
	}
	if e = identity.InTenant(ctx, session, policy.TaskWrite, "", seed); e != nil {
		t.Fatal(e)
	}
	// Statistics collected outside measured queries; fixture admin does not serve retrieval.
	if _, e = f.Admin.Exec(ctx, `SET statement_timeout='5min'; SET maintenance_work_mem='512MB'; SET max_parallel_maintenance_workers=0; CREATE INDEX embeddings_hnsw ON app.embeddings USING hnsw(vector public.vector_cosine_ops); ANALYZE app.embeddings; ANALYZE app.objects; ANALYZE app.tasks; ANALYZE app.links`); e != nil {
		t.Fatal(e)
	}
	samples := []float64{}
	for i := 0; i < 110; i++ {
		query := fmt.Sprintf("API roadmap category %d", i%100)
		vectors, e := (llm.MockEmbedder{}).Embed(ctx, []string{query})
		if e != nil {
			t.Fatal(e)
		}
		start := time.Now()
		e = identity.InTenant(ctx, session, policy.SearchRead, "", func(tx database.TenantTx, a policy.Actor) error {
			hits, e := search.Retrieve(ctx, tx, a, query, model, vectors[0])
			if e != nil {
				return e
			}
			if len(hits) == 0 {
				return fmt.Errorf("empty benchmark result")
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		if i >= 10 {
			samples = append(samples, float64(time.Since(start).Microseconds())/1000)
		}
	}
	sort.Float64s(samples)
	result := map[string]interface{}{"go_version": runtime.Version(), "os": runtime.GOOS, "cpus": runtime.NumCPU(), "objects": 100000, "samples": len(samples), "warmups": 10, "p50_ms": samples[49], "p95_ms": samples[94], "dimensions": 384, "embedding": "mock-hash-v1-384", "scope": "warm hybrid SQL + tenant transaction + live session/policy checks; excludes queue/provider time", "distribution": "one workspace, synthetic tasks, 100 embedding buckets, serial requests"}
	data, e := json.Marshal(result)
	if e != nil {
		t.Fatal(e)
	}
	fmt.Printf("SEARCH_BENCH_RESULT=%s\n", data)
}

func searchBenchmarkSignup(t *testing.T, endpoints *auth.HTTP) (*browser, auth.Session) {
	handler := httpserver.NewAPI(slog.New(slog.NewJSONHandler(io.Discard, nil)), endpoints)
	b := &browser{handler: handler, cookies: map[string]*http.Cookie{}, t: t}
	b.token()
	session := decode[auth.Session](t, b.request("POST", "/auth/signup", map[string]string{"email": "bench@example.com", "password": "correct horse battery staple", "display_name": "Benchmark", "workspace_name": "Benchmark"}, true), 200)
	b.token()
	return b, session
}
