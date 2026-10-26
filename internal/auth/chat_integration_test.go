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

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/chat"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/platform/httpserver"
	"github.com/slipgraph/slipgraph/internal/platform/testdb"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/realtime"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestChatRealtimeIsolation(t *testing.T) {
	fixture := testdb.New(t)
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "redis:7-alpine", ExposedPorts: []string{"6379/tcp"}, WaitingFor: wait.ForListeningPort("6379/tcp")}, Started: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := container.Terminate(ctx); err != nil {
			t.Error(err)
		}
	}()
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	cache := redis.NewClient(&redis.Options{Addr: host + ":" + port.Port()})
	defer func() { _ = cache.Close() }()
	identity, err := auth.New(fixture.Pool, bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	endpoints, err := auth.NewHTTP(identity, "http://localhost:5173", false)
	if err != nil {
		t.Fatal(err)
	}
	broker := &realtime.Broker{Redis: cache}
	gateway := realtime.New(ctx, endpoints, &chat.Store{Auth: identity}, broker)
	defer gateway.Shutdown()
	handler := httpserver.NewRealtimeAPI(slog.New(slog.NewJSONHandler(io.Discard, nil)), endpoints, gateway)
	server := httptest.NewServer(handler)
	defer server.Close()
	newUser := func(email string) (*browser, auth.Session) {
		b := &browser{handler: handler, cookies: map[string]*http.Cookie{}, t: t}
		b.token()
		s := decode[auth.Session](t, b.request("POST", "/auth/signup", map[string]string{"email": email, "password": "correct horse battery staple", "display_name": email, "workspace_name": email}, true), 200)
		b.token()
		return b, s
	}
	a, sa := newUser("chat-a@example.com")
	b, sb := newUser("chat-b@example.com")
	channel := decode[chat.Channel](t, a.request("POST", "/channels", map[string]string{"name": "general"}, true), 200)
	other := decode[chat.Channel](t, b.request("POST", "/channels", map[string]string{"name": "general"}, true), 200)
	path := "/channels/" + channel.ID.String()
	otherPath := "/channels/" + other.ID.String()
	input := func(body string, thread *uuid.UUID) map[string]interface{} {
		return map[string]interface{}{"body": body, "mention_ids": []string{}, "thread_id": thread, "client_id": uuid.NewString()}
	}
	payload := input("<img src=x onerror=alert(1)> plain text", nil)
	root := decode[chat.Message](t, a.request("POST", path+"/messages", payload, true), 200)
	replay := decode[chat.Message](t, a.request("POST", path+"/messages", payload, true), 200)
	if replay.ID != root.ID {
		t.Fatal("retry duplicated message")
	}
	reply := decode[chat.Message](t, a.request("POST", path+"/messages", input("reply", &root.ID), true), 200)
	decode[chat.Message](t, a.request("POST", path+"/messages", input("second root", nil), true), 200)
	page := decode[chat.Page](t, a.request("GET", path+"/messages?limit=1", nil, false), 200)
	if len(page.Items) != 1 || page.NextCursor == nil {
		t.Fatal("missing pagination")
	}
	older := decode[chat.Page](t, a.request("GET", path+"/messages?limit=1&before="+page.NextCursor.String(), nil, false), 200)
	if len(older.Items) != 1 || older.Items[0].ID != root.ID {
		t.Fatal("pagination lost messages")
	}
	thread := decode[chat.Page](t, a.request("GET", path+"/messages?thread_id="+root.ID.String(), nil, false), 200)
	if len(thread.Items) != 1 || thread.Items[0].ID != reply.ID {
		t.Fatal("thread mismatch")
	}
	for _, test := range []struct {
		method, path string
		body         interface{}
		status       int
	}{
		{"GET", path, nil, 404}, {"GET", path + "/messages", nil, 404}, {"GET", path + "/presence", nil, 404},
		{"POST", path + "/messages", input("foreign", nil), 404}, {"POST", otherPath + "/messages", input("foreign thread", &root.ID), 404},
		{"PUT", "/messages/" + root.ID.String(), map[string]interface{}{"body": "tamper", "mention_ids": []string{}, "version": 1}, 404},
		{"DELETE", "/messages/" + root.ID.String(), map[string]interface{}{"version": 1}, 404},
		{"GET", otherPath + "/messages?thread_id=" + root.ID.String(), nil, 404},
		{"POST", "/channels", map[string]string{"name": "forged", "workspace_id": sa.WorkspaceID.String()}, 400},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			r := b.request(test.method, test.path, test.body, test.method != "GET")
			if r.Code != test.status {
				t.Fatalf("%d want %d: %s", r.Code, test.status, r.Body)
			}
		})
	}
	own := decode[[]chat.Channel](t, b.request("GET", "/channels", nil, false), 200)
	if len(own) != 1 || own[0].ID != other.ID {
		t.Fatal("channel list leaked")
	}
	foreignMember := decode[[]auth.Member](t, b.request("GET", "/members", nil, false), 200)[0]
	bad := input("foreign mention", nil)
	bad["mention_ids"] = []string{foreignMember.ID.String()}
	decode[map[string]string](t, a.request("POST", path+"/messages", bad, true), 404)
	// Unknown task references cannot become links or leak their existence.
	decode[map[string]string](t, a.request("POST", path+"/messages", input("slipgraph://task/"+uuid.NewString(), nil), true), 404)
	decode[map[string]string](t, a.request("POST", path+"/messages", input(strings.Repeat("x", 8001), nil), true), 400)
	decode[map[string]string](t, a.request("POST", path+"/messages", input("csrf", nil), false), 403)
	edited := decode[chat.Message](t, a.request("PUT", "/messages/"+root.ID.String(), map[string]interface{}{"body": "edited", "mention_ids": []string{}, "version": 1}, true), 200)
	if edited.Version != 2 {
		t.Fatal("version unchanged")
	}
	decode[map[string]string](t, a.request("PUT", "/messages/"+root.ID.String(), map[string]interface{}{"body": "stale", "mention_ids": []string{}, "version": 1}, true), 409)
	// RLS still protects callers that omit their workspace predicate.
	if err := database.WithTenant(ctx, fixture.Pool, sb.WorkspaceID, func(tx database.TenantTx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.messages WHERE id=$1`, root.ID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("unfiltered query crossed RLS")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	dial := func(user *browser, origin string) (*websocket.Conn, *http.Response, error) {
		header := http.Header{}
		if origin != "" {
			header.Set("Origin", origin)
		}
		if user != nil {
			r := &http.Request{Header: header}
			for _, cookie := range user.cookies {
				r.AddCookie(cookie)
			}
		}
		timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return websocket.Dial(timeout, "ws"+strings.TrimPrefix(server.URL, "http")+"/realtime", &websocket.DialOptions{HTTPHeader: header})
	}
	for _, origin := range []string{"https://evil.example", "null", "", "http://localhost:5173.evil.example"} {
		conn, response, err := dial(a, origin)
		if conn != nil {
			_ = conn.CloseNow()
		}
		if err == nil || response == nil || response.StatusCode != 403 {
			t.Fatalf("cross-site origin %q accepted", origin)
		}
	}
	conn, response, err := dial(nil, "http://localhost:5173")
	if conn != nil {
		_ = conn.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != 401 {
		t.Fatal("anonymous socket accepted")
	}
	read := func(conn *websocket.Conn) realtime.Notice {
		t.Helper()
		timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, data, err := conn.Read(timeout)
		if err != nil {
			t.Fatal(err)
		}
		var n realtime.Notice
		if err := json.Unmarshal(data, &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	write := func(conn *websocket.Conn, value interface{}) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := conn.Write(timeout, websocket.MessageText, data); err != nil {
			t.Fatal(err)
		}
	}
	open := func(user *browser) *websocket.Conn {
		t.Helper()
		conn, _, err := dial(user, "http://localhost:5173")
		if err != nil {
			t.Fatal(err)
		}
		if read(conn).Type != "ready" {
			t.Fatal("missing ready")
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		return conn
	}
	until := func(conn *websocket.Conn, kind string) realtime.Notice {
		t.Helper()
		for i := 0; i < 30; i++ {
			n := read(conn)
			if n.Type == kind {
				return n
			}
		}
		t.Fatalf("missing %s", kind)
		return realtime.Notice{}
	}
	foreign := open(b)
	write(foreign, map[string]interface{}{"type": "subscribe", "channel_id": channel.ID})
	if until(foreign, "error").Code != "not_found" {
		t.Fatal("foreign subscription accepted")
	}
	_ = foreign.CloseNow()
	good := open(a)
	write(good, map[string]interface{}{"type": "subscribe", "channel_id": channel.ID})
	until(good, "subscribed")
	write(good, map[string]interface{}{"type": "typing", "channel_id": channel.ID, "active": true})
	until(good, "presence")
	// Read until the typing write becomes observable; a subscribe notification may arrive first.
	deadline := time.Now().Add(3 * time.Second)
	for {
		members := decode[[]chat.OnlineMember](t, a.request("GET", path+"/presence", nil, false), 200)
		if len(members) == 1 && members[0].UserID == sa.User.ID && members[0].Typing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("typing missing")
		}
		time.Sleep(20 * time.Millisecond)
	}
	online, _, err := broker.Snapshot(ctx, sb.WorkspaceID, channel.ID)
	if err != nil || len(online) != 0 {
		t.Fatal("Redis presence crossed workspace")
	}
	// The actual dispatcher publishes stable invalidation IDs, never free text.
	dispatcher := events.Dispatcher{Pool: fixture.Pool, Sessions: identity, Logger: slog.Default(), Consumers: []events.Consumer{{Name: "chat-fanout-v1", Handle: broker.Consume}}}
	if err := dispatcher.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	event := until(good, "event")
	if event.EventID == "" || event.ChannelID != channel.ID.String() {
		t.Fatal("invalid fanout")
	}
	if err := dispatcher.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	// Consumer cannot read another workspace's queued event, even with a valid own scope.
	if err := database.WithTenant(ctx, fixture.Pool, sb.WorkspaceID, func(tx database.TenantTx) error {
		err := broker.Consume(ctx, tx, policy.Actor{ID: uuid.NewString(), WorkspaceID: sb.WorkspaceID.String(), Role: policy.Consumer}, uuid.MustParse(event.EventID))
		if err == nil {
			t.Fatal("consumer crossed workspace")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second := open(a)
	third := open(a)
	conn, response, err = dial(a, "http://localhost:5173")
	if conn != nil {
		_ = conn.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != 429 {
		t.Fatal("connection cap bypassed")
	}
	_ = second.CloseNow()
	_ = third.CloseNow()
	_ = good.CloseNow()
	// A separate user keeps flood and oversize tests independent of prior budgets.
	huge := open(b)
	if err := huge.Write(ctx, websocket.MessageText, []byte(strings.Repeat("x", 4097))); err != nil {
		t.Fatal(err)
	}
	timeout, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, _, err = huge.Read(timeout)
	cancel()
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("oversize not rejected: %v", err)
	}
	_ = huge.CloseNow()
	flood := open(b)
	limited := false
	for i := 0; i < 35; i++ {
		write(flood, map[string]string{"type": "heartbeat"})
		n := read(flood)
		if n.Type == "error" {
			if n.Code != "rate_limited" {
				t.Fatalf("unexpected flood error %+v", n)
			}
			limited = true
			break
		}
	}
	if !limited {
		t.Fatal("message flood accepted")
	}
	_ = flood.CloseNow()
	// Mutation flood is limited independently of websocket traffic.
	limited = false
	for i := 0; i < 35; i++ {
		r := b.request("POST", otherPath+"/messages", input("bounded", nil), true)
		if r.Code == 429 {
			limited = true
			break
		}
		if r.Code != 200 {
			t.Fatalf("unexpected post status %d", r.Code)
		}
	}
	if !limited {
		t.Fatal("REST message flood accepted")
	}
	decode[interface{}](t, a.request("DELETE", "/messages/"+root.ID.String(), map[string]interface{}{"version": 2}, true), 204)
	deleted := decode[chat.Page](t, a.request("GET", path+"/messages", nil, false), 200)
	for _, m := range deleted.Items {
		if m.ID == root.ID && (!m.Deleted || m.Body != "") {
			t.Fatal("delete did not redact content")
		}
	}

	// Invite another writer: even admins cannot edit a peer's message.
	c, sc := newUser("chat-c@example.com")
	token := decode[map[string]string](t, a.request("POST", "/invites", map[string]string{"email": "chat-c@example.com", "role": "member"}, true), 200)["token"]
	decode[auth.Session](t, c.request("POST", "/invites/accept", map[string]string{"token": token}, true), 200)
	c.token()
	memberMessage := decode[chat.Message](t, c.request("POST", path+"/messages", input("member message", nil), true), 200)
	decode[map[string]string](t, a.request("PUT", "/messages/"+memberMessage.ID.String(), map[string]interface{}{"body": "admin override", "mention_ids": []string{}, "version": 1}, true), 403)
	decode[map[string]string](t, a.request("DELETE", "/messages/"+memberMessage.ID.String(), map[string]interface{}{"version": 1}, true), 403)
	memberSocket := open(c)
	write(memberSocket, map[string]interface{}{"type": "subscribe", "channel_id": channel.ID})
	until(memberSocket, "subscribed")
	members := decode[[]auth.Member](t, a.request("GET", "/members", nil, false), 200)
	var member auth.Member
	for _, m := range members {
		if m.UserID == sc.User.ID {
			member = m
		}
	}
	decode[interface{}](t, a.request("PUT", "/members/"+member.ID.String()+"/role", map[string]interface{}{"role": "viewer", "version": member.Version}, true), 204)
	decode[map[string]string](t, c.request("POST", path+"/messages", input("viewer post", nil), true), 403)
	decode[map[string]string](t, c.request("POST", "/channels", map[string]string{"name": "viewer channel"}, true), 403)
	decode[map[string]string](t, c.request("PUT", "/messages/"+memberMessage.ID.String(), map[string]interface{}{"body": "viewer edit", "mention_ids": []string{}, "version": 1}, true), 403)
	write(memberSocket, map[string]interface{}{"type": "typing", "channel_id": channel.ID, "active": true})
	until(memberSocket, "error")
	_ = memberSocket.CloseNow()
	revoked := open(c)
	decode[interface{}](t, c.request("POST", "/auth/logout", nil, true), 204)
	write(revoked, map[string]string{"type": "heartbeat"})
	until(revoked, "error")
	_ = revoked.CloseNow()
	// Presence leases expire without a clean disconnect (crashed process).
	leaseID := uuid.New()
	if err := broker.Online(ctx, sa.WorkspaceID, channel.ID, sa.User.ID, leaseID, 30*time.Millisecond, true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	online, _, err = broker.Snapshot(ctx, sa.WorkspaceID, channel.ID)
	if err != nil || online[sa.User.ID] {
		t.Fatal("presence lease did not expire")
	}
	// Explicit shutdown must release directory subscriptions and connections without hanging.
	active := open(a)
	defer func() { _ = active.CloseNow() }()
	done := make(chan struct{})
	go func() { gateway.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("gateway did not drain")
	}
	// A responsive ping/pong peer still expires when application frames go idle.
	idleGateway := realtime.New(ctx, endpoints, &chat.Store{Auth: identity}, broker)
	idleGateway.Limits.Idle = 150 * time.Millisecond
	idleGateway.Limits.Heartbeat = 30 * time.Millisecond
	idleServer := httptest.NewServer(httpserver.NewRealtimeAPI(slog.Default(), endpoints, idleGateway))
	defer idleServer.Close()
	defer idleGateway.Shutdown()
	header := http.Header{"Origin": []string{"http://localhost:5173"}}
	req := &http.Request{Header: header}
	for _, cookie := range a.cookies {
		req.AddCookie(cookie)
	}
	idleCtx, idleCancel := context.WithTimeout(ctx, 2*time.Second)
	defer idleCancel()
	idleConn, _, err := websocket.Dial(idleCtx, "ws"+strings.TrimPrefix(idleServer.URL, "http")+"/realtime", &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = idleConn.CloseNow() }()
	read(idleConn)
	_, _, err = idleConn.Read(idleCtx)
	if err == nil || idleCtx.Err() != nil {
		t.Fatalf("application idle timeout missing: %v", err)
	}
}
