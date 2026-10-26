//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/audit"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/platform/httpserver"
	"github.com/slipgraph/slipgraph/internal/platform/testdb"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/risk"
	"github.com/slipgraph/slipgraph/internal/tasks"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestProjectsTasksIsolationAndRisk(t *testing.T) {
	fixture := testdb.New(t)
	ctx := context.Background()
	identity, err := auth.New(fixture.Pool, bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	api, err := auth.NewHTTP(identity, "http://localhost:5173", false)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpserver.NewAPI(slog.New(slog.NewJSONHandler(io.Discard, nil)), api, fixture.Pool.Ping)
	newUser := func(email, workspace string) (*browser, auth.Session) {
		b := &browser{handler: handler, cookies: map[string]*http.Cookie{}, t: t}
		b.token()
		session := decode[auth.Session](t, b.request("POST", "/auth/signup", map[string]string{"email": email, "password": "correct horse battery staple", "display_name": workspace, "workspace_name": workspace}, true), 200)
		b.token()
		return b, session
	}
	a, sessionA := newUser("project-a@example.com", "A")
	b, sessionB := newUser("project-b@example.com", "B")
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	date := func(days int) string { return start.Add(time.Duration(days) * 24 * time.Hour).Format(time.RFC3339) }
	projectBody := map[string]interface{}{"name": "Launch", "description": "Scoped project", "start": date(0), "launch_due": date(6)}
	p := decode[tasks.Project](t, a.request("POST", "/projects", projectBody, true), 200)
	other := decode[tasks.Project](t, b.request("POST", "/projects", projectBody, true), 200)
	makeBody := func(title string, duration, due int) map[string]interface{} {
		return map[string]interface{}{"title": title, "description": "Plain text", "status": "todo", "owner_id": nil, "start": date(0), "due": date(due), "estimate_minutes": duration * 1440, "milestone": title == "Launch"}
	}
	bodies := []map[string]interface{}{makeBody("A", 2, 2), makeBody("B", 2, 5), makeBody("C", 3, 5), makeBody("Launch", 1, 6)}
	created := make([]tasks.Task, 4)
	for i, body := range bodies {
		created[i] = decode[tasks.Task](t, a.request("POST", "/projects/"+p.ID.String()+"/tasks", body, true), 200)
	}
	foreignTask := decode[tasks.Task](t, b.request("POST", "/projects/"+other.ID.String()+"/tasks", makeBody("Foreign", 1, 5), true), 200)
	current := func(i int) tasks.Task {
		return decode[tasks.Task](t, a.request("GET", "/tasks/"+created[i].ID.String(), nil, false), 200)
	}
	add := func(dependent, blocker int) {
		task := current(dependent)
		created[dependent] = decode[tasks.Task](t, a.request("POST", "/tasks/"+task.ID.String()+"/dependencies", map[string]interface{}{"version": task.Version, "blocker_id": created[blocker].ID.String()}, true), 200)
	}
	add(1, 0)
	add(2, 0)
	add(3, 1)
	add(3, 2)
	if r := a.request("POST", "/tasks/"+created[0].ID.String()+"/dependencies", map[string]interface{}{"version": current(0).Version, "blocker_id": created[3].ID.String()}, true); r.Code != 409 {
		t.Fatalf("cycle accepted: %d %s", r.Code, r.Body)
	}
	before := current(0)
	updated := decode[tasks.Task](t, a.request("POST", "/tasks/"+before.ID.String()+"/delay", map[string]interface{}{"version": before.Version, "days": 2}, true), 200)
	if updated.Version != before.Version+1 {
		t.Fatal("version not incremented")
	}
	if r := a.request("POST", "/tasks/"+before.ID.String()+"/delay", map[string]interface{}{"version": before.Version, "days": 3}, true); r.Code != 409 {
		t.Fatal("stale version accepted")
	}
	// Every endpoint is exercised with a valid foreign tenant session.
	taskUpdate := makeBody("Attack", 1, 5)
	taskUpdate["version"] = 1
	projectUpdate := map[string]interface{}{"name": "Attack", "description": "Denied", "start": date(0), "launch_due": date(6), "version": 1}
	cases := []struct {
		method, path string
		body         interface{}
	}{
		{"GET", "/projects/" + p.ID.String(), nil}, {"PUT", "/projects/" + p.ID.String(), projectUpdate}, {"DELETE", "/projects/" + p.ID.String(), map[string]int{"version": 1}},
		{"POST", "/projects/" + p.ID.String() + "/tasks", makeBody("Attack", 1, 5)}, {"GET", "/projects/" + p.ID.String() + "/risk", nil},
		{"GET", "/tasks/" + created[0].ID.String(), nil}, {"PUT", "/tasks/" + created[0].ID.String(), taskUpdate}, {"DELETE", "/tasks/" + created[0].ID.String(), map[string]int{"version": 1}},
		{"POST", "/tasks/" + created[0].ID.String() + "/delay", map[string]int{"version": 1, "days": 2}},
		{"POST", "/tasks/" + created[0].ID.String() + "/dependencies", map[string]interface{}{"version": 1, "blocker_id": foreignTask.ID.String()}},
		{"DELETE", "/tasks/" + created[0].ID.String() + "/dependencies/" + created[1].ID.String(), map[string]int{"version": 1}},
	}
	for _, test := range cases {
		r := b.request(test.method, test.path, test.body, true)
		if r.Code != 404 {
			t.Fatalf("%s %s leaked scope: %d %s", test.method, test.path, r.Code, r.Body)
		}
	}
	ownProjects := decode[[]tasks.Project](t, b.request("GET", "/projects", nil, false), 200)
	if len(ownProjects) != 1 || ownProjects[0].ID != other.ID {
		t.Fatal("list leaked projects")
	}
	forgedProject := map[string]interface{}{"name": "Forged", "description": "", "start": date(0), "launch_due": nil, "workspace_id": sessionA.WorkspaceID.String()}
	if r := b.request("POST", "/projects", forgedProject, true); r.Code != 400 {
		t.Fatal("caller-selected project scope accepted")
	}
	// Assignment is a scoped membership object, not a global identity lookup.
	memberB := decode[[]auth.Member](t, b.request("GET", "/members", nil, false), 200)[0]
	badOwner := makeBody("Bad owner", 1, 5)
	badOwner["owner_id"] = memberB.ID.String()
	if r := a.request("POST", "/projects/"+p.ID.String()+"/tasks", badOwner, true); r.Code != 404 {
		t.Fatal("cross-tenant owner accepted")
	}
	if r := a.request("POST", "/tasks/"+current(0).ID.String()+"/dependencies", map[string]interface{}{"version": current(0).Version, "blocker_id": foreignTask.ID.String()}, true); r.Code != 404 {
		t.Fatal("cross-tenant dependency accepted")
	}
	dispatcher := events.Dispatcher{Pool: fixture.Pool, Sessions: identity, Logger: slog.Default(), Consumers: []events.Consumer{{Name: "audit-v1", Handle: audit.Consume}, {Name: "risk-v1", Handle: risk.Consume}}}
	if err := dispatcher.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	assessment := decode[tasks.RiskEnvelope](t, a.request("GET", "/projects/"+p.ID.String()+"/risk", nil, false), 200)
	if assessment.Assessment == nil || assessment.Stale || assessment.Assessment.ProjectSlipMinutes != 2880 || assessment.Assessment.LaunchSlipMinutes != 2880 {
		t.Fatalf("wrong stored risk: %+v", assessment)
	}
	if err := dispatcher.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	again := decode[tasks.RiskEnvelope](t, a.request("GET", "/projects/"+p.ID.String()+"/risk", nil, false), 200)
	if again.Version != assessment.Version || !again.AssessedAt.Equal(*assessment.AssessedAt) {
		t.Fatal("consumer not idempotent")
	}
	// Loader closure is a recursive CTE and is tenant-scoped even with foreign IDs.
	if err := identity.InTenant(ctx, sessionA, policy.TaskRead, "", func(tx database.TenantTx, actor policy.Actor) error {
		loaded, err := risk.Load(ctx, tx, actor, p.ID, created[0].ID.String(), true)
		if err != nil {
			return err
		}
		if len(loaded.Downstream) != 4 {
			t.Fatalf("wrong recursive closure: %v", loaded.Downstream)
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.tasks`).Scan(&count); err != nil {
			return err
		}
		if count != 4 {
			t.Fatal("RLS leaked unfiltered tasks")
		}
		_, err = risk.Load(ctx, tx, actor, other.ID, "", true)
		if err == nil {
			t.Fatal("loader crossed tenant")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A hostile queued reference cannot cause the service session to load another workspace.
	fakeEvent := uuid.Must(uuid.NewV7())
	if err := identity.InTenant(ctx, sessionA, policy.TaskWrite, "", func(tx database.TenantTx, _ policy.Actor) error {
		_, err := tx.Exec(ctx, `INSERT INTO app.events(id,workspace_id,object_id,actor_id,event_type,payload) VALUES($1,$2,$3,$4,'task.updated',$5)`, fakeEvent, sessionA.WorkspaceID, p.ID, sessionA.User.ID, []byte(`{"project_id":"`+other.ID.String()+`","graph_version":2}`))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if err := identity.InTenant(ctx, sessionB, policy.ProjectRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.risk_assessments WHERE source_event_id=$1`, fakeEvent).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("job wrote foreign assessment")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Soft deletion retains object history while all read paths return not found.
	target := current(1)
	decode[map[string]string](t, a.request("DELETE", "/tasks/"+target.ID.String(), map[string]int64{"version": target.Version}, true), 204)
	if r := a.request("GET", "/tasks/"+target.ID.String(), nil, false); r.Code != 404 {
		t.Fatal("soft-deleted task visible")
	}
	board := decode[tasks.Board](t, a.request("GET", "/projects/"+p.ID.String(), nil, false), 200)
	if len(board.Tasks) != 3 {
		t.Fatal("soft delete not applied to board")
	}
	decode[map[string]string](t, a.request("DELETE", "/projects/"+p.ID.String(), map[string]int64{"version": board.Project.Version}, true), 204)
	if r := a.request("GET", "/tasks/"+created[0].ID.String(), nil, false); r.Code != 404 {
		t.Fatal("task in deleted project visible")
	}
	raw, _ := json.Marshal(assessment)
	if strings.Contains(string(raw), "Plain text") {
		t.Fatal("free text leaked to risk snapshot")
	}
}
