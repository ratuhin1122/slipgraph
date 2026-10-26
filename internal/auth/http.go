package auth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/audit"
	"github.com/slipgraph/slipgraph/internal/platform/contract"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
)

const AccessCookie = "__Host-slipgraph_access"
const RefreshCookie = "__Host-slipgraph_refresh"

type HTTP struct {
	TrustedProxyHost string
	Service          *Service
	Origin           string
}

func NewHTTP(service *Service, origin string, production bool) (*HTTP, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("invalid PUBLIC_ORIGIN")
	}
	if parsed.Scheme != "https" && (production || parsed.Scheme != "http" || (parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1")) {
		return nil, errors.New("PUBLIC_ORIGIN requires HTTPS outside loopback development")
	}
	return &HTTP{Service: service, Origin: strings.TrimSuffix(origin, "/")}, nil
}
func JSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func Error(w http.ResponseWriter, err error) {
	code := 500
	message := "internal error"
	switch {
	case errors.Is(err, ErrUnauthenticated):
		code = 401
		message = "authentication required"
	case errors.Is(err, ErrForbidden):
		code = 403
		message = "not permitted"
	case errors.Is(err, ErrInvalid):
		code = 400
		message = "invalid request"
	case errors.Is(err, ErrConflict):
		code = 409
		message = "conflict"
	case errors.Is(err, ErrNotFound):
		code = 404
		message = "not found"
	case errors.Is(err, ErrLimited):
		code = 429
		message = "too many attempts"
		w.Header().Set("Retry-After", "60")
	}
	JSON(w, code, map[string]string{"error": message})
}
func Decode(w http.ResponseWriter, r *http.Request, target interface{}) error {
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		return ErrInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return ErrInvalid
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return ErrInvalid
	}
	if err := decoder.Decode(new(interface{})); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		return ErrInvalid
	}
	kind := reflect.TypeOf(target)
	if kind.Kind() != reflect.Pointer || kind.Elem().Kind() != reflect.Struct {
		return ErrInvalid
	}
	kind = kind.Elem()
	for i := 0; i < kind.NumField(); i++ {
		field := kind.Field(i)
		tag := field.Tag.Get("json")
		parts := strings.Split(tag, ",")
		if parts[0] == "" || parts[0] == "-" {
			continue
		}
		raw, exists := fields[parts[0]]
		optional := strings.Contains(tag, ",omitempty")
		if !exists && !optional {
			return ErrInvalid
		}
		if exists && string(raw) == "null" && field.Type.Kind() != reflect.Pointer {
			return ErrInvalid
		}
	}

	return nil
}
func cookie(r *http.Request, key string) string {
	c, err := r.Cookie(key)
	if err != nil {
		return ""
	}
	return c.Value
}
func setCookie(w http.ResponseWriter, key, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: key, Value: value, Path: "/", MaxAge: age, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
}
func (h *HTTP) setPair(w http.ResponseWriter, pair pair) {
	setCookie(w, AccessCookie, pair.Access, 900)
	setCookie(w, RefreshCookie, pair.Refresh, 604800)
}
func (h *HTTP) client(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	if h.TrustedProxyHost != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
		defer cancel()
		peers, err := net.DefaultResolver.LookupIPAddr(ctx, h.TrustedProxyHost)
		if err == nil {
			for _, peer := range peers {
				if peer.IP.Equal(net.ParseIP(host)) {
					forwarded := net.ParseIP(r.Header.Get("X-Real-IP"))
					if forwarded != nil {
						return forwarded.String()
					}
				}
			}
		}
	}
	return host
}

func unsafe(method string) bool { return method != "GET" && method != "HEAD" && method != "OPTIONS" }
func (h *HTTP) mutation(ctx context.Context, r *http.Request) error {
	token := cookie(r, AccessCookie)
	if r.Header.Get("Origin") != h.Origin || !h.Service.CheckCSRF(token, r.Header.Get("X-CSRF-Token")) {
		return ErrForbidden
	}
	var valid bool
	if err := h.Service.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.sessions WHERE token_hash=$1 AND kind IN ('anonymous','human') AND revoked_at IS NULL AND expires_at>$2)`, tokenHash(token), h.Service.now()).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrForbidden
	}
	return nil
}
func (h *HTTP) public(w http.ResponseWriter, r *http.Request, purpose string, limit int, window time.Duration) bool {
	if !policy.Can(r.Context(), policy.Actor{}, policy.AuthPublic, policy.Resource{}) {
		Error(w, ErrForbidden)
		return false
	}
	if err := h.Service.Rate(r.Context(), purpose, h.client(r), limit, window); err != nil {
		Error(w, err)
		return false
	}
	if unsafe(r.Method) {
		if err := h.mutation(r.Context(), r); err != nil {
			Error(w, err)
			return false
		}
	}
	return true
}

// Require authenticates cookies and enforces CSRF before protected handlers decode
// any DTO. The actual resource-specific authorization runs again inside InTenant.
func (h *HTTP) Require(w http.ResponseWriter, r *http.Request, action policy.Action) (Session, bool) {
	if r.URL.Query().Has("workspace_id") {
		Error(w, ErrInvalid)
		return Session{}, false
	}
	session, err := h.Service.Resolve(r.Context(), cookie(r, AccessCookie))
	if err != nil {
		_ = policy.Can(r.Context(), policy.Actor{}, action, policy.Resource{})
		Error(w, err)
		return session, false
	}
	if !policy.Can(r.Context(), session.Actor(), policy.SessionRead, policy.Resource{WorkspaceID: session.WorkspaceID.String(), OwnerID: session.User.ID.String()}) {
		Error(w, ErrForbidden)
		return session, false
	}
	if err := h.Service.Rate(r.Context(), string(action)+":"+session.WorkspaceID.String(), session.User.ID.String(), 120, time.Minute); err != nil {
		Error(w, err)
		return session, false
	}
	if unsafe(r.Method) {
		if err := h.mutation(r.Context(), r); err != nil {
			Error(w, err)
			return session, false
		}
	}
	return session, true
}
func (h *HTTP) GetCSRF(w http.ResponseWriter, r *http.Request) {
	if !h.public(w, r, "csrf", 600, 10*time.Minute) {
		return
	}
	token, err := h.Service.Anonymous(r.Context(), cookie(r, AccessCookie))
	if err != nil {
		Error(w, err)
		return
	}
	if token != cookie(r, AccessCookie) {
		setCookie(w, AccessCookie, token, 1800)
	}
	JSON(w, 200, contract.CSRF{CsrfToken: h.Service.CSRF(token)})
}
func (h *HTTP) Signup(w http.ResponseWriter, r *http.Request) {
	if !h.public(w, r, "signup", 5, time.Hour) {
		return
	}
	var input contract.SignupInput
	if err := Decode(w, r, &input); err != nil {
		Error(w, err)
		return
	}
	result, err := h.Service.Signup(r.Context(), input.Email, input.Password, input.DisplayName, input.WorkspaceName, cookie(r, AccessCookie))
	if err != nil {
		Error(w, err)
		return
	}
	h.setPair(w, result)
	JSON(w, 200, result.Session)
}
func (h *HTTP) Login(w http.ResponseWriter, r *http.Request) {
	if !h.public(w, r, "login", 30, 10*time.Minute) {
		return
	}
	var input contract.LoginInput
	if err := Decode(w, r, &input); err != nil {
		Error(w, err)
		return
	}
	result, err := h.Service.Login(r.Context(), input.Email, input.Password, cookie(r, AccessCookie))
	if err != nil {
		Error(w, err)
		return
	}
	h.setPair(w, result)
	JSON(w, 200, result.Session)
}
func (h *HTTP) Refresh(w http.ResponseWriter, r *http.Request) {
	if !h.public(w, r, "refresh", 120, 10*time.Minute) {
		return
	}
	result, err := h.Service.Refresh(r.Context(), cookie(r, AccessCookie), cookie(r, RefreshCookie), r.Header.Get("X-CSRF-Token"))
	if err != nil {
		Error(w, err)
		return
	}
	h.setPair(w, result)
	JSON(w, 200, result.Session)
}
func (h *HTTP) GetSession(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Require(w, r, policy.SessionRead)
	if !ok {
		return
	}
	JSON(w, 200, session)
}
func (h *HTTP) Logout(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Require(w, r, policy.SessionWrite)
	if !ok {
		return
	}
	if err := h.Service.Logout(r.Context(), session); err != nil {
		Error(w, err)
		return
	}
	setCookie(w, AccessCookie, "", -1)
	setCookie(w, RefreshCookie, "", -1)
	w.WriteHeader(204)
}
func (h *HTTP) ListSessions(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Require(w, r, policy.SessionRead)
	if !ok {
		return
	}
	if len(r.URL.Query()) != 0 {
		Error(w, ErrInvalid)
		return
	}
	result, err := h.Service.Sessions(r.Context(), session)
	if err != nil {
		Error(w, err)
		return
	}
	JSON(w, 200, result)
}
func (h *HTTP) ListWorkspaces(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Require(w, r, policy.SessionRead)
	if !ok {
		return
	}
	if len(r.URL.Query()) != 0 {
		Error(w, ErrInvalid)
		return
	}
	result, err := h.Service.Workspaces(r.Context(), session)
	if err != nil {
		Error(w, err)
		return
	}
	JSON(w, 200, result)
}
func (h *HTTP) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Require(w, r, policy.WorkspaceCreate)
	if !ok {
		return
	}
	var input contract.NameInput
	if err := Decode(w, r, &input); err != nil {
		Error(w, err)
		return
	}
	result, err := h.Service.CreateWorkspace(r.Context(), session, input.Name, cookie(r, AccessCookie))
	if err != nil {
		Error(w, err)
		return
	}
	h.setPair(w, result)
	JSON(w, 200, result.Session)
}
func (h *HTTP) SwitchWorkspace(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Require(w, r, policy.SessionWrite)
	if !ok {
		return
	}
	var input contract.TicketInput
	if err := Decode(w, r, &input); err != nil {
		Error(w, err)
		return
	}
	result, err := h.Service.Switch(r.Context(), session, input.Ticket, cookie(r, AccessCookie))
	if err != nil {
		Error(w, err)
		return
	}
	h.setPair(w, result)
	JSON(w, 200, result.Session)
}
func (h *HTTP) ListMembers(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Require(w, r, policy.MemberRead)
	if !ok {
		return
	}
	result, err := h.Service.Members(r.Context(), session)
	if err != nil {
		Error(w, err)
		return
	}
	JSON(w, 200, result)
}
func (h *HTTP) ChangeRole(w http.ResponseWriter, r *http.Request, memberID string) {
	session, ok := h.Require(w, r, policy.MemberRoleChange)
	if !ok {
		return
	}
	id, err := uuid.Parse(memberID)
	if err != nil {
		Error(w, ErrInvalid)
		return
	}
	var input contract.RoleInput
	if err := Decode(w, r, &input); err != nil {
		Error(w, err)
		return
	}
	if err := h.Service.ChangeRole(r.Context(), session, id, policy.Role(input.Role), input.Version); err != nil {
		Error(w, err)
		return
	}
	w.WriteHeader(204)
}
func (h *HTTP) CreateInvite(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Require(w, r, policy.MemberInvite)
	if !ok {
		return
	}
	if err := h.Service.Rate(r.Context(), "invite:"+session.WorkspaceID.String(), session.User.ID.String(), 20, time.Hour); err != nil {
		Error(w, err)
		return
	}
	var input contract.InviteInput
	if err := Decode(w, r, &input); err != nil {
		Error(w, err)
		return
	}
	token, err := h.Service.Invite(r.Context(), session, input.Email, policy.Role(input.Role))
	if err != nil {
		Error(w, err)
		return
	}
	JSON(w, 200, contract.InviteToken{Token: token})
}
func (h *HTTP) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Require(w, r, policy.SessionWrite)
	if !ok {
		return
	}
	var input contract.TokenInput
	if err := Decode(w, r, &input); err != nil {
		Error(w, err)
		return
	}
	result, err := h.Service.AcceptInvite(r.Context(), session, input.Token, cookie(r, AccessCookie))
	if err != nil {
		Error(w, err)
		return
	}
	h.setPair(w, result)
	JSON(w, 200, result.Session)
}
func (h *HTTP) VerifyAudit(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Require(w, r, policy.AuditVerify)
	if !ok {
		return
	}
	var result audit.Result
	err := h.Service.InTenant(r.Context(), session, policy.AuditVerify, "", func(tx database.TenantTx, actor policy.Actor) error {
		var err error
		result, err = audit.Verify(r.Context(), tx, actor)
		return err
	})
	if err != nil {
		Error(w, err)
		return
	}
	JSON(w, 200, result)
}
