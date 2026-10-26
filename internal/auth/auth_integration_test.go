//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/slipgraph/slipgraph/internal/audit"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/platform/httpserver"
	"github.com/slipgraph/slipgraph/internal/platform/testdb"
	"github.com/slipgraph/slipgraph/internal/policy"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type browser struct {
	handler http.Handler
	cookies map[string]*http.Cookie
	t       *testing.T
	csrf    string
}

func (b *browser) request(method, path string, body interface{}, csrf bool) *httptest.ResponseRecorder {
	b.t.Helper()
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			b.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("Origin", "http://localhost:5173")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range b.cookies {
		r.AddCookie(cookie)
	}
	if csrf {
		r.Header.Set("X-CSRF-Token", b.csrf)
	}
	response := httptest.NewRecorder()
	b.handler.ServeHTTP(response, r)
	for _, cookie := range response.Result().Cookies() {
		if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.Domain != "" {
			b.t.Fatal("unsafe cookie")
		}
		if cookie.MaxAge < 0 {
			delete(b.cookies, cookie.Name)
		} else {
			b.cookies[cookie.Name] = cookie
		}
	}
	return response
}
func (b *browser) token() {
	b.t.Helper()
	r := b.request("GET", "/auth/csrf", nil, false)
	if r.Code != 200 {
		b.t.Fatalf("csrf: %d %s", r.Code, r.Body)
	}
	var value struct {
		Token string `json:"csrf_token"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &value); err != nil {
		b.t.Fatal(err)
	}
	b.csrf = value.Token
}
func decode[T interface{}](t *testing.T, response *httptest.ResponseRecorder, code int) T {
	t.Helper()
	if response.Code != code {
		t.Fatalf("status %d want %d: %s", response.Code, code, response.Body)
	}
	var result T
	if code != 204 {
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	return result
}
func TestIdentitySecurity(t *testing.T) {
	fixture := testdb.New(t)
	ctx := context.Background()
	service, err := auth.New(fixture.Pool, bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	endpoints, err := auth.NewHTTP(service, "http://localhost:5173", false)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpserver.NewAPI(slog.New(slog.NewJSONHandler(io.Discard, nil)), endpoints, fixture.Pool.Ping)
	newBrowser := func() *browser { return &browser{handler: handler, cookies: map[string]*http.Cookie{}, t: t} }
	a, b := newBrowser(), newBrowser()
	a.token()
	preauth := a.cookies[auth.AccessCookie].Value
	if r := a.request("POST", "/auth/signup", map[string]string{"email": "a@example.com", "password": "correct horse battery staple", "display_name": "A", "workspace_name": "A"}, false); r.Code != 403 {
		t.Fatal("missing CSRF accepted")
	}
	sa := decode[auth.Session](t, a.request("POST", "/auth/signup", map[string]string{"email": "a@example.com", "password": "correct horse battery staple", "display_name": "A", "workspace_name": "A"}, true), 200)
	if a.cookies[auth.AccessCookie].Value == preauth {
		t.Fatal("session fixation")
	}
	if _, err := service.Resolve(ctx, preauth); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("preauth remained valid")
	}
	a.token()
	b.token()
	sb := decode[auth.Session](t, b.request("POST", "/auth/signup", map[string]string{"email": "b@example.com", "password": "correct horse battery staple", "display_name": "B", "workspace_name": "B"}, true), 200)
	b.token()
	own := decode[[]auth.Workspace](t, a.request("GET", "/auth/workspaces", nil, false), 200)
	if len(own) != 1 || own[0].ID != sa.WorkspaceID {
		t.Fatal("membership directory leaked")
	}
	if r := a.request("GET", "/auth/workspaces?user_id="+sb.User.ID.String(), nil, false); r.Code != 400 {
		t.Fatal("caller-selected directory identity accepted")
	}
	sessions := decode[[]auth.SessionInfo](t, a.request("GET", "/auth/sessions", nil, false), 200)
	for _, s := range sessions {
		if s.WorkspaceID != sa.WorkspaceID {
			t.Fatal("another user's session leaked")
		}
	}
	membersA := decode[[]auth.Member](t, a.request("GET", "/members", nil, false), 200)
	if len(membersA) != 1 {
		t.Fatal("wrong member count")
	}
	if r := a.request("PUT", "/members/"+membersA[0].ID.String()+"/role", map[string]interface{}{"role": "admin", "version": 1}, true); r.Code != 403 {
		t.Fatalf("self-role edit accepted: %d", r.Code)
	}
	if r := b.request("PUT", "/members/"+membersA[0].ID.String()+"/role", map[string]interface{}{"role": "admin", "version": 1}, true); r.Code != 404 {
		t.Fatalf("cross-tenant member exposed: %d", r.Code)
	}
	if r := b.request("POST", "/auth/workspace-switch", map[string]string{"ticket": own[0].SwitchToken}, true); r.Code != 401 {
		t.Fatalf("stolen switch ticket accepted: %d", r.Code)
	}
	if r := a.request("POST", "/auth/workspace-switch", map[string]string{"workspace_id": sb.WorkspaceID.String()}, true); r.Code != 400 {
		t.Fatal("body-selected workspace accepted")
	}
	token := decode[map[string]string](t, a.request("POST", "/invites", map[string]string{"email": "b@example.com", "role": "member"}, true), 200)["token"]
	var stored []byte
	if err := fixture.Admin.QueryRow(ctx, `SELECT token_hash FROM identity.invites WHERE email='b@example.com'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(token))
	if !bytes.Equal(stored, hash[:]) {
		t.Fatal("invite token not hashed")
	}
	joined := decode[auth.Session](t, b.request("POST", "/invites/accept", map[string]string{"token": token}, true), 200)
	if joined.WorkspaceID != sa.WorkspaceID || joined.Role != policy.Member {
		t.Fatal("incorrect invited scope/role")
	}
	b.token()
	if r := b.request("POST", "/invites/accept", map[string]string{"token": token}, true); r.Code != 404 {
		t.Fatal("invite reused")
	}
	expired := decode[map[string]string](t, a.request("POST", "/invites", map[string]string{"email": "b@example.com", "role": "viewer"}, true), 200)["token"]
	if _, err := fixture.Admin.Exec(ctx, `UPDATE identity.invites SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, sha256Bytes(expired)); err != nil {
		t.Fatal(err)
	}
	if r := b.request("POST", "/invites/accept", map[string]string{"token": expired}, true); r.Code != 404 {
		t.Fatal("expired invite accepted")
	}
	// Sharing one workspace must not expose the other user's private directory.
	sharedDirectory := decode[[]auth.Workspace](t, a.request("GET", "/auth/workspaces", nil, false), 200)
	if len(sharedDirectory) != 1 || sharedDirectory[0].ID != sa.WorkspaceID {
		t.Fatal("peer directory leaked after joining")
	}
	aMembers := decode[[]auth.Member](t, a.request("GET", "/members", nil, false), 200)
	var memberB auth.Member
	for _, member := range aMembers {
		if member.UserID == sb.User.ID {
			memberB = member
		}
	}
	if r := b.request("PUT", "/members/"+memberB.ID.String()+"/role", map[string]interface{}{"role": "admin", "version": 1}, true); r.Code != 403 {
		t.Fatal("member promoted itself")
	}
	if r := a.request("PUT", "/members/"+memberB.ID.String()+"/role", map[string]interface{}{"role": "viewer", "version": 1, "workspace_id": sb.WorkspaceID.String()}, true); r.Code != 400 {
		t.Fatal("mass assignment accepted")
	}
	decode[map[string]string](t, a.request("PUT", "/members/"+memberB.ID.String()+"/role", map[string]interface{}{"role": "viewer", "version": 1}, true), 204)
	dispatcher := &events.Dispatcher{Pool: fixture.Pool, Sessions: service, Logger: slog.Default(), Consumers: []events.Consumer{{Name: "audit-v1", Handle: audit.Consume}}}
	if err := dispatcher.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	verified := decode[audit.Result](t, a.request("GET", "/audit/verify", nil, false), 200)
	if !verified.Valid || verified.Entries < 6 {
		t.Fatalf("invalid audit: %+v", verified)
	}
	if r := b.request("GET", "/audit/verify", nil, false); r.Code != 403 {
		t.Fatal("viewer verified audit")
	}
	if _, err := fixture.Pool.Exec(ctx, `SELECT * FROM app.audit_log`); err == nil {
		t.Fatal("runtime can read audit table")
	}
	if _, err := fixture.Admin.Exec(ctx, `UPDATE app.audit_log SET event_type='tampered'`); err == nil {
		t.Fatal("audit mutation trigger failed")
	}
	if _, err := fixture.Admin.Exec(ctx, `TRUNCATE app.audit_log`); err == nil {
		t.Fatal("audit truncate trigger failed")
	}
	// Deliberately omit the tenant predicate: Postgres must still isolate rows.
	if err := service.InTenant(ctx, sa, policy.MemberRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.workspaces`).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("RLS did not isolate unfiltered query")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldAccess := a.cookies[auth.AccessCookie].Value
	oldRefresh := a.cookies[auth.RefreshCookie].Value
	decode[auth.Session](t, a.request("POST", "/auth/refresh", nil, true), 200)
	a.token()
	if a.cookies[auth.AccessCookie].Value == oldAccess {
		t.Fatal("access not rotated")
	}
	if _, err := service.Resolve(ctx, oldAccess); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("old access survived rotation")
	}
	newAccess := a.cookies[auth.AccessCookie].Value
	a.cookies[auth.RefreshCookie].Value = oldRefresh
	if r := a.request("POST", "/auth/refresh", nil, true); r.Code != 401 {
		t.Fatal("refresh reused")
	}
	if _, err := service.Resolve(ctx, newAccess); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("refresh replay did not revoke family")
	}
	// Login failures are persisted and locked atomically; credentials do not leak.
	login := newBrowser()
	login.token()
	for i := 0; i < 5; i++ {
		r := login.request("POST", "/auth/login", map[string]string{"email": "a@example.com", "password": "incorrect-password"}, true)
		if r.Code != 401 {
			t.Fatalf("wrong password: %d", r.Code)
		}
	}
	if r := login.request("POST", "/auth/login", map[string]string{"email": "a@example.com", "password": "correct horse battery staple"}, true); r.Code != 401 {
		t.Fatal("locked login accepted")
	}
	if _, err := fixture.Admin.Exec(ctx, `UPDATE identity.credentials SET locked_until=now()-interval '1 second' WHERE user_id=$1`, sa.User.ID); err != nil {
		t.Fatal(err)
	}
	decode[auth.Session](t, login.request("POST", "/auth/login", map[string]string{"email": "a@example.com", "password": "correct horse battery staple"}, true), 200)
	login.token()
	current := login.cookies[auth.AccessCookie].Value
	decode[map[string]string](t, login.request("POST", "/auth/logout", nil, true), 204)
	if _, err := service.Resolve(ctx, current); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("logout not revocable")
	}
	// Reset tokens cannot cross users, survive expiry, or be replayed after use.
	login.token()
	resetSession := decode[auth.Session](t, login.request("POST", "/auth/login", map[string]string{"email": "a@example.com", "password": "correct horse battery staple"}, true), 200)
	resetToken, err := service.IssueReset(ctx, resetSession.User)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ResetPassword(ctx, joined, resetToken, "a different long password"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("cross-user reset: %v", err)
	}
	if _, err := fixture.Admin.Exec(ctx, `UPDATE identity.password_resets SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, sha256Bytes(resetToken)); err != nil {
		t.Fatal(err)
	}
	if err := service.ResetPassword(ctx, resetSession, resetToken, "a different long password"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("expired reset: %v", err)
	}
	resetToken, err = service.IssueReset(ctx, resetSession.User)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ResetPassword(ctx, resetSession, resetToken, "a different long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Resolve(ctx, login.cookies[auth.AccessCookie].Value); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("password reset did not revoke sessions")
	}
	login.token()
	newSession := decode[auth.Session](t, login.request("POST", "/auth/login", map[string]string{"email": "a@example.com", "password": "a different long password"}, true), 200)
	if err := service.ResetPassword(ctx, newSession, resetToken, "yet another long password"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("reset token replayed: %v", err)
	}
	if pool, err := database.Open(ctx, fixture.AdminDSN); err == nil {
		pool.Close()
		t.Fatal("superuser startup accepted")
	}
	if err := service.Rate(ctx, "test-rate", "client", 1, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := service.Rate(ctx, "test-rate", "client", 1, time.Hour); !errors.Is(err, auth.ErrLimited) {
		t.Fatal("shared rate limit failed")
	}
	if strings.Contains(verified.Head, "password") {
		t.Fatal("secret in audit")
	}
}
func sha256Bytes(value string) []byte { hash := sha256.Sum256([]byte(value)); return hash[:] }
