package httpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/chat"
	"github.com/slipgraph/slipgraph/internal/meetings"
	"github.com/slipgraph/slipgraph/internal/platform/contract"
	"github.com/slipgraph/slipgraph/internal/platform/logging"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/proposals"
	"github.com/slipgraph/slipgraph/internal/realtime"
	"github.com/slipgraph/slipgraph/internal/tasks"
)

type Probe func(context.Context) error
type taskEndpoints = tasks.HTTP
type chatEndpoints = chat.HTTP
type proposalEndpoints = proposals.HTTP
type meetingEndpoints = meetings.HTTP
type Handler struct {
	*taskEndpoints
	*chatEndpoints
	*proposalEndpoints
	*meetingEndpoints
	*realtime.Gateway
	*auth.HTTP
	probes []Probe
}

var _ contract.ServerInterface = (*Handler)(nil)

func (h *Handler) GetHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if !policy.Can(ctx, policy.Actor{}, policy.HealthRead, policy.Resource{}) {
		auth.JSON(w, 503, contract.Health{Status: contract.Unavailable})
		return
	}
	for _, probe := range h.probes {
		if err := probe(ctx); err != nil {
			auth.JSON(w, 503, contract.Health{Status: contract.Unavailable})
			return
		}
	}
	auth.JSON(w, 200, contract.Health{Status: contract.Ok})
}
func New(logger *slog.Logger, probes ...Probe) http.Handler {
	r := chi.NewRouter()
	r.Use(Middleware(logger))
	h := &Handler{probes: probes}
	r.Get("/healthz", h.GetHealth)
	return r
}
func NewAPI(logger *slog.Logger, identity *auth.HTTP, probes ...Probe) http.Handler {
	return NewRealtimeAPI(logger, identity, nil, probes...)
}
func NewRealtimeAPI(logger *slog.Logger, identity *auth.HTTP, gateway *realtime.Gateway, probes ...Probe) http.Handler {
	r := chi.NewRouter()
	r.Use(Middleware(logger))
	h := &Handler{meetingEndpoints: &meetings.HTTP{Store: meetings.DefaultStore(identity.Service), Identity: identity, Uploads: make(chan struct{}, 4)}, proposalEndpoints: &proposals.HTTP{Store: &proposals.Store{Auth: identity.Service}, Identity: identity}, Gateway: gateway, chatEndpoints: &chat.HTTP{Store: &chat.Store{Auth: identity.Service}, Identity: identity}, HTTP: identity, taskEndpoints: &tasks.HTTP{Store: &tasks.Store{Auth: identity.Service}, Identity: identity}, probes: probes}
	return contract.HandlerFromMux(h, r)
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (w *recorder) WriteHeader(code int) {
	if w.status != 0 {
		return
	}
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
func (w *recorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}
func (w *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		w.status = 101
	}
	return conn, rw, err
}
func (w *recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func Middleware(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := uuid.NewV7()
			if err != nil {
				http.Error(w, "request ID unavailable", http.StatusServiceUnavailable)
				return
			}
			r = r.WithContext(logging.WithRequestID(r.Context(), id.String()))
			w.Header().Set("X-Request-ID", id.String())
			w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			w.Header().Set("Cache-Control", "no-store")
			rw := &recorder{ResponseWriter: w}
			start := time.Now()
			defer func() {
				if recover() != nil {
					logger.ErrorContext(r.Context(), "panic recovered", "request_id", id.String())
					if rw.status == 0 {
						rw.Header().Set("Content-Type", "application/json")
						rw.WriteHeader(http.StatusInternalServerError)
						_ = json.NewEncoder(rw).Encode(map[string]string{"error": "internal error"})
					}
				}
				status := rw.status
				if status == 0 {
					status = http.StatusOK
				}
				logger.InfoContext(r.Context(), "request", "request_id", id.String(), "method", r.Method, "status", status, "duration_ms", time.Since(start).Milliseconds())
			}()
			next.ServeHTTP(rw, r)
		})
	}
}
