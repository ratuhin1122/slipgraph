//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/chat"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/filestore"
	"github.com/slipgraph/slipgraph/internal/llm"
	"github.com/slipgraph/slipgraph/internal/meetings"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/platform/httpserver"
	"github.com/slipgraph/slipgraph/internal/platform/securityspec"
	"github.com/slipgraph/slipgraph/internal/platform/testdb"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/proposals"
	"github.com/slipgraph/slipgraph/internal/realtime"
	"github.com/slipgraph/slipgraph/internal/risk"
	"github.com/slipgraph/slipgraph/internal/search"
	"github.com/slipgraph/slipgraph/internal/tasks"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type securityObjects struct {
	Project, Task, Blocker, Channel, Message, Member, Run, Brief, Plan, Meeting, Segment, Extraction, Search, Ask uuid.UUID
	GitHubConnection, GitHubEvent, GitHubItem                                                                     uuid.UUID
	Proposal                                                                                                      proposals.Proposal
	Token                                                                                                         string
}
type securityFixture struct {
	identity *auth.Service
	db       *testdb.Fixture
	users    [2]*browser
	sessions [2]auth.Session
	actors   [2]policy.Actor
	objects  [2]securityObjects
	ps       *proposals.Store
	ms       *meetings.Store
	ss       *search.Store
	logger   *slog.Logger
	handler  http.Handler
}

const securityPassword = "correct horse battery staple"

func newSecurityFixture(t *testing.T) *securityFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("MEDIA_ROOT", root)
	t.Setenv("LLM_PROVIDER", "mock")
	t.Setenv("TRANSCRIPTION_PROVIDER", "mock")
	t.Setenv("EMBEDDING_PROVIDER", "mock")
	f := &securityFixture{db: testdb.New(t), logger: slog.New(slog.NewJSONHandler(io.Discard, nil))}
	ctx := context.Background()
	var e error
	f.identity, e = auth.New(f.db.Pool, bytes.Repeat([]byte{17}, 32))
	if e != nil {
		t.Fatal(e)
	}
	httpAuth, e := auth.NewHTTP(f.identity, "http://localhost:5173", false)
	if e != nil {
		t.Fatal(e)
	}
	// Resource presence authorization fails before touching Redis. Real socket
	// subscriptions/flood/revocation use TestChatRealtimeIsolation with real Redis.

	redisContainer, e := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "redis:7-alpine", ExposedPorts: []string{"6379/tcp"}, WaitingFor: wait.ForListeningPort("6379/tcp")}, Started: true})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = redisContainer.Terminate(context.Background()) })
	host, e := redisContainer.Host(ctx)
	if e != nil {
		t.Fatal(e)
	}
	port, e := redisContainer.MappedPort(ctx, "6379/tcp")
	if e != nil {
		t.Fatal(e)
	}
	cache := redis.NewClient(&redis.Options{Addr: host + ":" + port.Port()})
	t.Cleanup(func() { _ = cache.Close() })
	gateway := realtime.New(ctx, httpAuth, &chat.Store{Auth: f.identity}, &realtime.Broker{Redis: cache})
	t.Cleanup(gateway.Shutdown)
	f.handler = httpserver.NewRealtimeAPI(f.logger, httpAuth, gateway)
	for i, email := range []string{"matrix-a@example.com", "matrix-b@example.com"} {
		b := &browser{handler: f.handler, cookies: map[string]*http.Cookie{}, t: t}
		b.token()
		f.users[i] = b
		f.sessions[i] = decode[auth.Session](t, b.request("POST", "/auth/signup", map[string]string{"email": email, "password": securityPassword, "display_name": email, "workspace_name": email}, true), 200)
		b.token()
	}
	actors, e := f.identity.ConsumerSessions(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for i, s := range f.sessions {
		for _, a := range actors {
			if a.WorkspaceID == s.WorkspaceID.String() {
				f.actors[i] = a
			}
		}
	}
	f.ps = &proposals.Store{Auth: f.identity}
	f.ms = &meetings.Store{Auth: f.identity, Files: filestore.Local{Root: root}, DailyMinutes: 600}
	f.ss = &search.Store{Auth: f.identity}
	ts := &tasks.Store{Auth: f.identity}
	cs := &chat.Store{Auth: f.identity}
	start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	limits := proposals.DefaultLimits()
	limits.DailyTokens = 10000000
	limits.DailyCostMicroUSD = 1000000000
	for i, s := range f.sessions {
		o := &f.objects[i]
		b := f.users[i]
		project, e := ts.CreateProject(ctx, s, tasks.ProjectInput{Name: s.User.Email, Description: "PRIVATE-" + s.User.Email, Start: start})
		if e != nil {
			t.Fatal(e)
		}
		o.Project = project.ID
		for n := 0; n < 2; n++ {
			task, e := ts.CreateTask(ctx, s, o.Project, tasks.TaskInput{Title: []string{"Backend API", "Blocker"}[n], Description: "PRIVATE-" + s.User.Email, Status: "todo", Start: start, Due: start.Add(24 * time.Hour), EstimateMinutes: 60})
			if e != nil {
				t.Fatal(e)
			}
			if n == 0 {
				o.Task = task.ID
			} else {
				o.Blocker = task.ID
			}
		}
		c, e := cs.CreateChannel(ctx, s, s.User.Email)
		if e != nil {
			t.Fatal(e)
		}
		o.Channel = c.ID
		m, e := cs.Post(ctx, s, c.ID, chat.Input{Body: "maybe backend API will slip 3 days. PRIVATE-" + s.User.Email, ClientID: uuid.New()})
		if e != nil {
			t.Fatal(e)
		}
		o.Message = m.ID
		members := decode[[]auth.Member](t, b.request("GET", "/members", nil, false), 200)
		o.Member = members[0].ID
		o.Run, e = f.ps.Enqueue(ctx, s, "reschedule_task", []uuid.UUID{o.Task, o.Message})
		if e != nil {
			t.Fatal(e)
		}
		data, e := json.Marshal(proposals.RescheduleTask{TaskID: o.Task, Version: 1, Start: start, Due: start.Add(48 * time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		runner := proposals.Runner{Store: f.ps, Provider: &fixtureProvider{fixture: data}, Limits: limits, Logger: f.logger}
		if e = runner.Process(ctx, f.actors[i], o.Run); e != nil {
			t.Fatal(e)
		}
		page, e := f.ps.List(ctx, s, nil)
		if e != nil || len(page.Items) != 1 {
			t.Fatalf("proposal fixture: %v", e)
		}
		o.Proposal = page.Items[0]
		o.Plan, e = f.ps.RequestPlan(ctx, s, "Start 2030-01-01. A private backend/frontend launch brief for "+s.User.Email)
		if e != nil {
			t.Fatal(e)
		}
		e = f.identity.InTenant(ctx, s, policy.ProposalRead, "", func(tx database.TenantTx, _ policy.Actor) error {
			return tx.QueryRow(ctx, `SELECT l.to_object FROM app.links l JOIN app.objects o ON o.workspace_id=l.workspace_id AND o.id=l.to_object WHERE l.workspace_id=$1 AND l.from_object=$2 AND l.link_type='source' AND o.type='brief'`, tx.WorkspaceID(), o.Plan).Scan(&o.Brief)
		})
		if e != nil {
			t.Fatal(e)
		}
		id := uuid.New()
		key, size, e := f.ms.WriteUpload(ctx, s, id, bytes.NewReader(recordingBytes()))
		if e != nil {
			t.Fatal(e)
		}
		meeting, e := f.ms.Accept(ctx, s, id, o.Project, s.User.Email, "audio/wav", key, size)
		if e != nil {
			t.Fatal(e)
		}
		o.Meeting = meeting.ID
		mr := meetings.Runner{Store: f.ms, Transcriber: meetings.MockTranscriber{}, Probe: durationProbe{duration: 2000}, Proposals: f.ps, Logger: f.logger}
		if e = mr.Process(ctx, f.actors[i], id); e != nil {
			t.Fatal(e)
		}
		seg, e := f.ms.Transcript(ctx, s, id, "", -1)
		if e != nil || len(seg.Items) < 1 {
			t.Fatalf("transcript fixture: %v", e)
		}
		o.Segment = seg.Items[0].ID
		e = f.identity.InTenant(ctx, s, policy.ProposalRead, "", func(tx database.TenantTx, _ policy.Actor) error {
			return tx.QueryRow(ctx, `SELECT id FROM app.agent_runs WHERE workspace_id=$1 AND kind='meeting_extraction' ORDER BY id LIMIT 1`, tx.WorkspaceID()).Scan(&o.Extraction)
		})
		if e != nil {
			t.Fatal(e)
		}
		req, e := f.ss.Request(ctx, s, "PRIVATE-"+s.User.Email, "search")
		if e != nil {
			t.Fatal(e)
		}
		o.Search = req.ID
		req, e = f.ss.Request(ctx, s, "PRIVATE-"+s.User.Email, "ask")
		if e != nil {
			t.Fatal(e)
		}
		o.Ask = req.ID
		o.Token = decode[map[string]string](t, b.request("POST", "/invites", map[string]string{"email": "invited-" + s.User.Email, "role": "member"}, true), 200)["token"]
	}
	dispatcher := events.Dispatcher{Pool: f.db.Pool, Sessions: f.identity, Logger: f.logger, Consumers: []events.Consumer{{Name: "security-index", Handle: f.ss.Consume}, {Name: "security-risk", Handle: risk.Consume}}}
	if e = dispatcher.Dispatch(ctx); e != nil {
		t.Fatal(e)
	}
	indexer := search.Runner{Store: f.ss, Embedder: llm.MockEmbedder{}, Provider: llm.Demo{}, Logger: f.logger, DailyTokens: 10000000, DailyCostMicroUSD: 1000000000, CostPerToken: 1}
	for _, id := range []uuid.UUID{f.objects[0].Task, f.objects[0].Message, f.objects[0].Segment} {
		if e = indexer.Index(ctx, f.actors[0], id); e != nil {
			t.Fatal(e)
		}
	}
	seedGitHubSecurity(t, f)
	return f
}
func (f *securityFixture) path(op securityspec.Operation, index int) string {
	o := f.objects[index]
	r := strings.NewReplacer("{projectId}", o.Project.String(), "{taskId}", o.Task.String(), "{blockerId}", o.Blocker.String(), "{channelId}", o.Channel.String(), "{messageId}", o.Message.String(), "{memberId}", o.Member.String(), "{proposalId}", o.Proposal.ID.String(), "{runId}", o.Run.String(), "{briefId}", o.Brief.String(), "{meetingId}", o.Meeting.String(), "{segmentId}", o.Segment.String(), "{requestId}", o.Search.String())
	if op.ID == "completeGitHub" {
		return op.Path + "?code=invalid&state=" + strings.Repeat("x", 43)
	}
	if op.ID == "receiveGitHub" {
		return "/hooks/github/invalid"
	}
	return r.Replace(op.Path)
}
func (f *securityFixture) body(id string, index int) interface{} {
	o := f.objects[index]
	version := map[string]interface{}{"version": 1}
	task := map[string]interface{}{"title": "Updated", "description": "", "status": "todo", "owner_id": nil, "start": "2030-01-01T00:00:00Z", "due": "2030-01-02T00:00:00Z", "estimate_minutes": 60, "milestone": false}
	switch id {
	case "configureGitHub":
		return map[string]interface{}{"repository": "octo/fixture", "channel_id": o.Channel, "stale_days": 7, "delay_days": 1, "workspace_id": f.sessions[index].WorkspaceID}
	case "mapGitHubIssue":
		return map[string]interface{}{"issue_number": 12, "task_id": o.Task, "workspace_id": f.sessions[index].WorkspaceID}
	case "updateTask":
		task["version"] = 1
		return task
	case "createTask":
		return task
	case "deleteTask", "deleteProject", "deleteMessage", "removeDependency":
		return version
	case "updateProject":
		return map[string]interface{}{"version": 1, "name": "Updated", "description": "", "start": "2030-01-01T00:00:00Z", "launch_due": nil}
	case "reportDelay":
		return map[string]interface{}{"version": 1, "days": 3}
	case "addDependency":
		return map[string]interface{}{"version": 1, "blocker_id": o.Blocker}
	case "postMessage":
		return map[string]interface{}{"body": "foreign attempt", "thread_id": nil, "client_id": uuid.New(), "mention_ids": []string{}}
	case "editMessage":
		return map[string]interface{}{"body": "foreign edit", "version": 1, "mention_ids": []string{}}
	case "changeRole":
		return map[string]interface{}{"version": 1, "role": "member"}
	case "approveProposal":
		return map[string]interface{}{"version": o.Proposal.Version, "payload_hash": o.Proposal.PayloadHash}
	case "rejectProposal":
		return map[string]interface{}{"version": o.Proposal.Version, "payload_hash": o.Proposal.PayloadHash, "reason": "foreign rejection"}
	case "confirmDelay":
		return map[string]interface{}{"task_id": o.Task, "days": 3}
	case "requestSearch", "requestAnswer":
		return map[string]interface{}{"query": "PRIVATE-" + f.sessions[index].User.Email}
	case "requestPlan":
		return map[string]interface{}{"brief": "A private plan", "workspace_id": f.sessions[index].WorkspaceID}
	case "createProject":
		return map[string]interface{}{"name": "forged", "description": "", "start": "2030-01-01T00:00:00Z", "launch_due": nil, "workspace_id": f.sessions[index].WorkspaceID}
	case "createChannel":
		return map[string]interface{}{"name": "forged", "workspace_id": f.sessions[index].WorkspaceID}
	case "createInvite":
		return map[string]interface{}{"email": "foreign@example.com", "role": "member", "workspace_id": f.sessions[index].WorkspaceID}
	case "acceptInvite":
		return map[string]interface{}{"token": o.Token}
	case "signup":
		return map[string]interface{}{"email": "forged@example.com", "password": securityPassword, "display_name": "forged", "workspace_name": "forged", "workspace_id": f.sessions[index].WorkspaceID}
	case "login":
		return map[string]interface{}{"email": f.sessions[index].User.Email, "password": "an incorrect foreign password"}
	case "createWorkspace":
		return map[string]interface{}{"name": "forged", "workspace_id": f.sessions[index].WorkspaceID}
	case "switchWorkspace":
		return map[string]interface{}{"ticket": "invalid foreign ticket"}
	}
	return nil
}
func matrixRaw(b *browser, method, path string, body interface{}, origin, csrf string, headers map[string]string) *httptest.ResponseRecorder {
	b.t.Helper()
	data, e := json.Marshal(body)
	if e != nil {
		b.t.Fatal(e)
	}
	if body == nil {
		data = nil
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.RemoteAddr = "192.0.2.2:1"
	r.Header.Set("Origin", origin)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	for _, c := range b.cookies {
		r.AddCookie(c)
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	b.handler.ServeHTTP(w, r)
	return w
}
func TestOpenAPITenantMatrix(t *testing.T) {
	f := newSecurityFixture(t)
	ctx := context.Background()
	ops, e := securityspec.Load("../..")
	if e != nil {
		t.Fatal(e)
	}
	a, b := f.users[0], f.users[1]
	sa, sb := f.sessions[0], f.sessions[1]
	foreignIDs := []string{f.objects[0].GitHubConnection.String(), f.objects[0].GitHubEvent.String(), f.objects[0].GitHubItem.String(), sa.User.Email, f.objects[0].Channel.String(), f.objects[0].Member.String(), f.objects[0].Run.String(), f.objects[0].Plan.String(), f.objects[0].Extraction.String(), f.objects[0].Segment.String(), f.objects[0].Search.String(), f.objects[0].Ask.String(), sa.WorkspaceID.String(), sa.User.ID.String(), f.objects[0].Project.String(), f.objects[0].Task.String(), f.objects[0].Message.String(), f.objects[0].Proposal.ID.String(), f.objects[0].Meeting.String(), f.objects[0].Brief.String(), "PRIVATE-" + sa.User.Email}
	noForeign := func(w *httptest.ResponseRecorder) {
		t.Helper()
		for _, id := range foreignIDs {
			if strings.Contains(w.Body.String(), id) {
				t.Fatalf("foreign data in response (%d): %s", w.Code, id)
			}
		}
	}
	for _, op := range ops {
		t.Run(op.ID, func(t *testing.T) {
			path := f.path(op, 0)
			body := f.body(op.ID, 0)
			if op.Scope == "webhook" {
				w := matrixRaw(b, op.Method, path, body, "https://evil.example", "", nil)
				if w.Code != 404 {
					t.Fatalf("unauthenticated hook route: %d", w.Code)
				}
				return
			}
			unsafe := op.Method != "GET"
			// Every unsafe operation is covered, including public authentication and uploads.
			if unsafe {
				w := matrixRaw(b, op.Method, path, body, "http://localhost:5173", "", nil)
				if w.Code != 403 {
					t.Fatalf("missing CSRF: %d %s", w.Code, w.Body)
				}
				w = matrixRaw(b, op.Method, path, body, "https://evil.example", b.csrf, nil)
				if w.Code != 403 {
					t.Fatalf("cross-site mutation: %d", w.Code)
				}
				w = matrixRaw(b, op.Method, path, body, "http://localhost:5173", a.csrf, nil)
				if w.Code != 403 {
					t.Fatalf("foreign CSRF token: %d", w.Code)
				}
			}
			if op.Scope != "public" {
				anon := &browser{handler: f.handler, cookies: map[string]*http.Cookie{}, t: t}
				w := matrixRaw(anon, op.Method, path, body, "http://localhost:5173", "", nil)
				if w.Code != 401 {
					t.Fatalf("anonymous operation: %d %s", w.Code, w.Body)
				}
			}
			switch op.Scope {
			case "resource":
				w := b.request(op.Method, path, body, unsafe)
				if w.Code != 404 {
					t.Fatalf("foreign resource: %d %s", w.Code, w.Body)
				}
				noForeign(w)
				if op.Method == "GET" {
					own := a.request(op.Method, path, nil, false)
					if own.Code != 200 {
						t.Fatalf("fixture does not exist: %d %s", own.Code, own.Body)
					}
				}
			case "websocket":
				w := matrixRaw(b, "GET", path, nil, "https://evil.example", "", nil)
				if w.Code != 403 {
					t.Fatal("cross-site websocket accepted")
				}
			case "collection":
				if op.ID == "uploadMeeting" {
					w := uploadRecording(b, f.objects[0].Project, "audio/wav", recordingBytes(), true, nil)
					if w.Code != 404 {
						t.Fatalf("foreign upload parent: %d %s", w.Code, w.Body)
					}
					break
				}
				if op.ID == "requestSearch" || op.ID == "requestAnswer" {
					queued := decode[search.Result](t, b.request(op.Method, path, body, true), 202)
					r := search.Runner{Store: f.ss, Embedder: llm.MockEmbedder{}, Provider: llm.Demo{}, Logger: f.logger, DailyTokens: 10000000, DailyCostMicroUSD: 1000000000, CostPerToken: 1}
					if e := r.Process(ctx, f.actors[1], queued.ID); e != nil {
						t.Fatal(e)
					}
					result := decode[search.Result](t, b.request("GET", "/search/"+queued.ID.String(), nil, false), 200)
					if len(result.Items) != 0 || len(result.Claims) != 0 {
						t.Fatal("foreign query returned results")
					}
					break
				}
				if unsafe {
					w := b.request(op.Method, path, body, true)
					if w.Code != 400 {
						t.Fatalf("foreign scope injection: %d %s", w.Code, w.Body)
					}
				} else {
					w := b.request("GET", path, nil, false)
					if w.Code != 200 {
						t.Fatalf("own collection: %d %s", w.Code, w.Body)
					}
					noForeign(w)
					w = matrixRaw(b, "GET", path, nil, "http://localhost:5173", "", map[string]string{"X-Workspace-ID": sa.WorkspaceID.String()})
					if w.Code != 200 {
						t.Fatalf("header attempt: %d", w.Code)
					}
					noForeign(w)
				}
			case "oauth":
				w := b.request("GET", path, nil, false)
				if w.Code != 400 {
					t.Fatalf("foreign OAuth state: %d", w.Code)
				}
			case "identity", "public":
				switch op.ID {
				case "switchWorkspace":
					own := decode[[]auth.Workspace](t, a.request("GET", "/auth/workspaces", nil, false), 200)
					body = map[string]string{"ticket": own[0].SwitchToken}
				}
				w := b.request(op.Method, path, body, unsafe)
				switch op.ID {
				case "signup", "createWorkspace", "createInvite":
					if w.Code != 400 {
						t.Fatalf("scope accepted: %d", w.Code)
					}
				case "login", "switchWorkspace":
					if w.Code != 401 {
						t.Fatalf("foreign credentials/ticket: %d", w.Code)
					}
				case "acceptInvite":
					if w.Code != 404 {
						t.Fatalf("foreign invite: %d", w.Code)
					}
				case "connectGitHub":
					if w.Code != 503 {
						t.Fatalf("unconfigured GitHub connect: %d", w.Code)
					}
				case "disconnectGitHub":
					if w.Code != 204 {
						t.Fatalf("own GitHub disconnect: %d", w.Code)
					}
				case "getDocumentNonce":
					if w.Code != 204 || len(w.Header().Get("X-CSP-Nonce")) != 43 {
						t.Fatal("missing cryptographic nonce")
					}
				case "logout":
					if w.Code != 204 {
						t.Fatalf("own logout: %d", w.Code)
					}
					b.token()
					decode[auth.Session](t, b.request("POST", "/auth/login", map[string]string{"email": sb.User.Email, "password": securityPassword}, true), 200)
					b.token()
				default:
					if w.Code != 200 {
						t.Fatalf("own/public action: %d %s", w.Code, w.Body)
					}
					noForeign(w)
					if op.ID == "refresh" {
						b.token()
					}
				}
			default:
				t.Fatal("unhandled OpenAPI scope")
			}
		})
	}
	// No foreign write changed A's actual business state.
	ts := &tasks.Store{Auth: f.identity}
	task, e := ts.GetTask(ctx, sa, f.objects[0].Task)
	if e != nil || task.Version != 1 {
		t.Fatalf("foreign mutation changed task: %v", e)
	}
	p, e := f.ps.Get(ctx, sa, f.objects[0].Proposal.ID)
	if e != nil || p.Status != "pending" || p.Version != 1 {
		t.Fatal("foreign proposal action changed state")
	}
	decode[auth.Session](t, a.request("GET", "/auth/session", nil, false), 200)
}
