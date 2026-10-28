package integrations

import (
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/platform/contract"
	"github.com/slipgraph/slipgraph/internal/policy"
)

type HTTP struct {
	Store    *GitHub
	Identity *auth.HTTP
}

func (h *HTTP) GetGitHub(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Identity.Require(w, r, policy.IntegrationRead)
	if !ok {
		return
	}
	out, e := h.Store.Status(r.Context(), s)
	if e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 200, out)
}
func (h *HTTP) ConnectGitHub(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Identity.Require(w, r, policy.IntegrationManage)
	if !ok {
		return
	}
	if !h.Store.Config.enabled() {
		auth.JSON(w, 503, map[string]string{"error": "GitHub is not configured"})
		return
	}
	out, e := h.Store.StartOAuth(r.Context(), s)
	if e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 200, map[string]string{"url": out})
}
func (h *HTTP) CompleteGitHub(w http.ResponseWriter, r *http.Request, params contract.CompleteGitHubParams) {
	s, ok := h.Identity.Require(w, r, policy.IntegrationManage)
	if !ok {
		return
	}
	if e := h.Store.Complete(r.Context(), s, params.State, params.Code); e != nil {
		auth.Error(w, e)
		return
	}
	http.Redirect(w, r, "/settings/integrations", http.StatusSeeOther)
}
func (h *HTTP) ConfigureGitHub(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Identity.Require(w, r, policy.IntegrationManage)
	if !ok {
		return
	}
	var in contract.GitHubSetup
	if e := auth.Decode(w, r, &in); e != nil {
		auth.Error(w, e)
		return
	}
	channel, e := uuid.Parse(in.ChannelId)
	if e != nil {
		auth.Error(w, auth.ErrInvalid)
		return
	}
	out, e := h.Store.Configure(r.Context(), s, GitHubSetup{Repository: in.Repository, ChannelID: channel, StaleDays: in.StaleDays, DelayDays: in.DelayDays})
	if e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 200, out)
}
func (h *HTTP) DisconnectGitHub(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Identity.Require(w, r, policy.IntegrationManage)
	if !ok {
		return
	}
	if e := h.Store.Disconnect(r.Context(), s); e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 204, nil)
}
func (h *HTTP) ListGitHubEvents(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Identity.Require(w, r, policy.IntegrationRead)
	if !ok {
		return
	}
	out, e := h.Store.Logs(r.Context(), s)
	if e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 200, out)
}
func (h *HTTP) MapGitHubIssue(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Identity.Require(w, r, policy.IntegrationManage)
	if !ok {
		return
	}
	var in contract.MapGitHubIssueJSONBody
	if e := auth.Decode(w, r, &in); e != nil {
		auth.Error(w, e)
		return
	}
	id, e := uuid.Parse(in.TaskId)
	if e != nil {
		auth.Error(w, auth.ErrInvalid)
		return
	}
	if e = h.Store.MapIssue(r.Context(), s, in.IssueNumber, id); e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 204, nil)
}
func (h *HTTP) ReceiveGitHub(w http.ResponseWriter, r *http.Request, routingToken string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, e := io.ReadAll(r.Body)
	if e != nil {
		auth.Error(w, auth.ErrInvalid)
		return
	}
	if e = h.Store.Receive(r.Context(), routingToken, r.Header.Get("X-GitHub-Delivery"), r.Header.Get("X-GitHub-Event"), r.Header.Get("X-Hub-Signature-256"), body); e != nil {
		auth.Error(w, webhookError(e))
		return
	}
	auth.JSON(w, 202, nil)
}
