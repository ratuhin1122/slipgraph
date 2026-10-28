//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/audit"
	"github.com/slipgraph/slipgraph/internal/integrations"
	"github.com/slipgraph/slipgraph/internal/llm"
	"github.com/slipgraph/slipgraph/internal/meetings"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/proposals"
	"github.com/slipgraph/slipgraph/internal/realtime"
	"github.com/slipgraph/slipgraph/internal/risk"
	"github.com/slipgraph/slipgraph/internal/search"
)

func securitySignature(key []byte, workspace uuid.UUID, delivery, ts string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(workspace.String() + ".fixture." + delivery + "." + ts + "."))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
func TestWebhookReplayAndTenantScope(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	key := bytes.Repeat([]byte{7}, 32)
	now := time.Now().UTC()
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte("untrusted fixture")
	sig := securitySignature(key, f.sessions[0].WorkspaceID, "delivery-1", ts, body)
	claim := func(actor policy.Actor, signature string) error {
		return f.identity.InConsumer(ctx, actor, func(tx database.TenantTx) error {
			return integrations.VerifyAndClaim(ctx, tx, actor, key, "fixture", "delivery-1", ts, signature, body, now)
		})
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if claim(f.actors[0], sig) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatal("concurrent replay accepted", accepted.Load())
	}
	if claim(f.actors[0], sig) == nil {
		t.Fatal("replayed signed delivery accepted")
	}
	if claim(f.actors[1], sig) == nil {
		t.Fatal("signature transferred to another workspace")
	}
	ownB := securitySignature(key, f.sessions[1].WorkspaceID, "delivery-1", ts, body)
	if e := claim(f.actors[1], ownB); e != nil {
		t.Fatal(e)
	}
	e := f.identity.InConsumer(ctx, f.actors[1], func(tx database.TenantTx) error {
		if e := integrations.VerifyAndClaim(ctx, tx, f.actors[0], key, "fixture", "other", ts, sig, body, now); e == nil {
			t.Fatal("foreign actor accepted")
		}
		rows, e := tx.Query(ctx, `SELECT workspace_id FROM app.webhook_receipts`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var workspace uuid.UUID
			if e = rows.Scan(&workspace); e != nil {
				return e
			}
			if workspace != f.sessions[1].WorkspaceID {
				t.Fatal("unfiltered replay table leaked")
			}
		}
		return rows.Err()
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestEveryWorkerJobRejectsForeignScope(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	o := f.objects[0]
	s := f.sessions[0]
	a, b := f.actors[0], f.actors[1]
	limits := proposals.DefaultLimits()
	limits.DailyTokens = 10000000
	limits.DailyCostMicroUSD = 1000000000
	provider := &fixtureProvider{}
	runner := proposals.Runner{Store: f.ps, Provider: provider, Limits: limits, Logger: f.logger}
	seen := map[string]bool{}
	reject := func(name string, run func() error) {
		seen[name] = true
		t.Helper()
		t.Run(name, func(t *testing.T) {
			if e := run(); !errors.Is(e, auth.ErrNotFound) && !errors.Is(e, pgx.ErrNoRows) && !errors.Is(e, audit.ErrSourceUnavailable) {
				t.Fatalf("foreign resource was not hidden: %v", e)
			}
		})
	}
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	payloads := map[string]interface{}{
		"add_dependency":  proposals.AddDependency{TaskID: o.Task, Version: 1, BlockerID: o.Blocker},
		"create_task":     proposals.CreateTask{ProjectID: o.Project, Title: "Proposed", Start: start, Due: start.Add(time.Hour), EstimateMinutes: 60},
		"change_assignee": proposals.ChangeAssignee{TaskID: o.Task, Version: 1, OwnerID: nil},
	}
	runIDs := map[string]uuid.UUID{"reschedule_task": o.Run, "create_plan": o.Plan, "meeting_extraction": o.Extraction}
	for kind, payload := range payloads {
		id, e := f.ps.Enqueue(ctx, s, kind, []uuid.UUID{o.Task, o.Message})
		if e != nil {
			t.Fatal(e)
		}
		runIDs[kind] = id
		data, e := json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
		provider.fixture = data
		reject("generation/"+kind, func() error { return runner.Process(ctx, b, id) })
		if provider.calls != 0 {
			t.Fatal("foreign generation called provider")
		}
		if e = runner.Process(ctx, a, id); e != nil {
			t.Fatal(e)
		}
		provider.calls = 0
	}
	// Create-plan and meeting-extraction produce the remaining executable kinds.
	runner.Provider = llm.Demo{}
	for _, kind := range []string{"reschedule_task", "create_plan", "meeting_extraction"} {
		id := runIDs[kind]
		reject("generation/"+kind, func() error { return runner.Process(ctx, b, id) })
		if kind != "reschedule_task" {
			if e := runner.Process(ctx, a, id); e != nil {
				t.Fatal(e)
			}
		}
	}
	var eventID uuid.UUID
	if e := f.identity.InConsumer(ctx, a, func(tx database.TenantTx) error {
		return tx.QueryRow(ctx, `SELECT id FROM app.events WHERE workspace_id=$1 AND object_id=$2 AND event_type='message.created'`, tx.WorkspaceID(), o.Message).Scan(&eventID)
	}); e != nil {
		t.Fatal(e)
	}
	consumers := map[string]func(context.Context, database.TenantTx, policy.Actor, uuid.UUID) error{
		"audit-v1": audit.Consume, "risk-v1": risk.Consume, "chat-fanout-v1": (&realtime.Broker{}).Consume,
		"proposal-executor-v1": f.ps.Consume, "delay-detection-v1": f.ps.ConsumeMessage, "search-index-v1": f.ss.Consume,
	}
	for name, consume := range consumers {
		reject("consumer/"+name, func() error {
			return f.identity.InConsumer(ctx, b, func(tx database.TenantTx) error { return consume(ctx, tx, b, eventID) })
		})
	}
	if e := f.identity.InConsumer(ctx, a, func(tx database.TenantTx) error { return f.ps.ConsumeMessage(ctx, tx, a, eventID) }); e != nil {
		t.Fatal(e)
	}
	var delay uuid.UUID
	if e := f.identity.InConsumer(ctx, a, func(tx database.TenantTx) error {
		return tx.QueryRow(ctx, `SELECT id FROM app.agent_runs WHERE workspace_id=$1 AND source_event_id=$2 AND kind='delay_detection'`, tx.WorkspaceID(), eventID).Scan(&delay)
	}); e != nil {
		t.Fatal(e)
	}
	reject("generation/delay_detection", func() error { return runner.Process(ctx, b, delay) })
	if e := runner.Process(ctx, a, delay); e != nil {
		t.Fatal(e)
	}
	expected := map[string]bool{"reschedule_task": false, "add_dependency": false, "create_task": false, "change_assignee": false, "create_plan": false, "meeting_task": false, "record_decision": false}
	page, e := f.ps.List(ctx, s, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range page.Items {
		expected[p.Kind] = true
		if _, e = f.ps.Decide(ctx, s, p.ID, p.Version, p.PayloadHash, "", true); e != nil {
			t.Fatal(e)
		}

		reject("executor/"+p.Kind, func() error { return f.ps.Execute(ctx, b, p.ID) })
	}
	for kind, found := range expected {
		if !found {
			t.Fatal("missing executable proposal fixture", kind)
		}
	}
	transcriber := &transcriptFixture{}
	mr := meetings.Runner{Store: f.ms, Transcriber: transcriber, Probe: durationProbe{duration: 2000}, Proposals: f.ps, Logger: f.logger}
	reject("runner/meetings", func() error { return mr.Process(ctx, b, o.Meeting) })
	if transcriber.calls != 0 {
		t.Fatal("foreign transcription called provider")
	}
	embedder := &countEmbedder{}
	sr := search.Runner{Store: f.ss, Embedder: embedder, Provider: llm.Demo{}, Logger: f.logger, DailyTokens: 10000000, DailyCostMicroUSD: 1000000000, CostPerToken: 1}
	if e = f.identity.InConsumer(ctx, a, func(tx database.TenantTx) error {
		_, e := tx.Exec(ctx, `UPDATE app.search_index_jobs SET generation=processed_generation+1,retry_at=now(),lease_until=NULL WHERE workspace_id=$1 AND object_id=$2`, tx.WorkspaceID(), o.Task)
		return e
	}); e != nil {
		t.Fatal(e)
	}
	reject("runner/search-index", func() error { return sr.Index(ctx, b, o.Task) })
	reject("runner/search", func() error { return sr.Process(ctx, b, o.Search) })
	reject("runner/ask", func() error { return sr.Process(ctx, b, o.Ask) })
	if embedder.calls != 0 {
		t.Fatal("foreign search called provider")
	}
	raw, e := os.ReadFile("../../api/job-security-coverage.json")
	if e != nil {
		t.Fatal(e)
	}
	var required []string
	if e = json.Unmarshal(raw, &required); e != nil {
		t.Fatal(e)
	}
	for _, name := range required {
		if !seen[name] {
			t.Errorf("job security case did not execute: %s", name)
		}
	}
	// Source cannot be rescued by a forged service actor with A's ID and B's scope.
	forged := a
	forged.WorkspaceID = b.WorkspaceID
	if e := f.identity.InConsumer(ctx, forged, func(database.TenantTx) error { return nil }); e == nil {
		t.Fatal("forged service identity accepted")
	}
	if _, e = f.ps.Get(ctx, s, o.Proposal.ID); e != nil {
		t.Fatal(e)
	}
}
