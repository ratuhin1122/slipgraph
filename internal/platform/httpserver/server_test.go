package httpserver

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/platform/logging"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
		body   string
	}{{"ready", nil, 200, "ok"}, {"failed", errors.New("postgres://private-secret"), 503, "unavailable"}} {
		t.Run(tc.name, func(t *testing.T) {
			handler := New(logging.New(io.Discard, "info"), func(context.Context) error { return tc.err })
			req := httptest.NewRequest(http.MethodGet, "/healthz?token=query-secret", nil)
			req.Header.Set("X-Request-ID", "attacker")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatal("secret in health response")
			}
			if _, err := uuid.Parse(w.Header().Get("X-Request-ID")); err != nil {
				t.Fatal("missing server-generated request ID")
			}
			for _, header := range []string{"Content-Security-Policy", "Strict-Transport-Security", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Cache-Control"} {
				if w.Header().Get(header) == "" {
					t.Fatal("missing " + header)
				}
			}
		})
	}
}
func TestHealthNeverReturnsTenantData(t *testing.T) {
	handler := New(logging.New(io.Discard, "info"))
	for _, workspace := range []string{"workspace-a", "workspace-b"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz?workspace_id="+workspace, nil)
		req.Header.Set("X-Workspace-ID", workspace)
		handler.ServeHTTP(w, req)
		if w.Body.String() != "{\"status\":\"ok\"}\n" {
			t.Fatalf("unexpected public response %s", w.Body)
		}
	}
}
func TestPanicIsSanitized(t *testing.T) {
	h := Middleware(logging.New(io.Discard, "info"))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("token-secret") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != 500 || strings.Contains(w.Body.String(), "token-secret") {
		t.Fatal(w.Body.String())
	}
}
