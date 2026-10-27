package proposals

import (
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/platform/contract"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"net/http"
	"os"
)

type HTTP struct {
	Store    *Store
	Identity *auth.HTTP
}

func respond(w http.ResponseWriter, p interface{}, err error) {
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, p)
}
func (h *HTTP) ListProposals(w http.ResponseWriter, r *http.Request, p contract.ListProposalsParams) {
	s, ok := h.Identity.Require(w, r, policy.ProposalRead)
	if !ok {
		return
	}
	v, e := h.Store.List(r.Context(), s, p.Before)
	respond(w, v, e)
}
func (h *HTTP) GetProposal(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s, ok := h.Identity.Require(w, r, policy.ProposalRead)
	if !ok {
		return
	}
	v, e := h.Store.Get(r.Context(), s, id)
	respond(w, v, e)
}
func (h *HTTP) ApproveProposal(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s, ok := h.Identity.Require(w, r, policy.ProposalDecide)
	if !ok {
		return
	}
	var b contract.ApproveProposalInput
	if err := auth.Decode(w, r, &b); err != nil {
		auth.Error(w, err)
		return
	}
	v, e := h.Store.Decide(r.Context(), s, id, b.Version, b.PayloadHash, "", true)
	respond(w, v, e)
}
func (h *HTTP) RejectProposal(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s, ok := h.Identity.Require(w, r, policy.ProposalDecide)
	if !ok {
		return
	}
	var b contract.RejectProposalInput
	if err := auth.Decode(w, r, &b); err != nil {
		auth.Error(w, err)
		return
	}
	v, e := h.Store.Decide(r.Context(), s, id, b.Version, b.PayloadHash, b.Reason, false)
	respond(w, v, e)
}

func providerMode() string {
	mode := os.Getenv("LLM_PROVIDER")
	switch mode {
	case "mock", "openai", "anthropic":
		return mode
	}
	return "disabled"
}
func (h *HTTP) GetAgentSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.Identity.Require(w, r, policy.ProposalRead); !ok {
		return
	}
	mode := providerMode()
	auth.JSON(w, 200, map[string]interface{}{"enabled": mode != "disabled", "mode": mode})
}
func (h *HTTP) RequestPlan(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Identity.Require(w, r, policy.AgentRequest)
	if !ok {
		return
	}
	if providerMode() == "disabled" {
		auth.JSON(w, 503, map[string]string{"error": "An administrator must enable a worker provider first."})
		return
	}
	var b contract.PlanBriefInput
	if e := auth.Decode(w, r, &b); e != nil {
		auth.Error(w, e)
		return
	}
	id, e := h.Store.RequestPlan(r.Context(), session, b.Brief)
	respond(w, map[string]interface{}{"id": id}, e)
}
func (h *HTTP) GetAgentRun(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	session, ok := h.Identity.Require(w, r, policy.ProposalRead)
	if !ok {
		return
	}
	v, e := h.Store.GetRun(r.Context(), session, id)
	respond(w, v, e)
}
func (h *HTTP) ConfirmDelay(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	session, ok := h.Identity.Require(w, r, policy.AgentRequest)
	if !ok {
		return
	}
	var b contract.ConfirmDelayInput
	if e := auth.Decode(w, r, &b); e != nil {
		auth.Error(w, e)
		return
	}
	if e := h.Store.ConfirmDelay(r.Context(), session, id, b.TaskId, b.Days); e != nil {
		auth.Error(w, e)
		return
	}
	v, e := h.Store.GetRun(r.Context(), session, id)
	respond(w, v, e)
}
func (h *HTTP) ListProjectImpacts(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	session, ok := h.Identity.Require(w, r, policy.RiskRead)
	if !ok {
		return
	}
	v, e := h.Store.Impacts(r.Context(), session, id)
	respond(w, v, e)
}
func (h *HTTP) GetBrief(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	session, ok := h.Identity.Require(w, r, policy.ProposalRead)
	if !ok {
		return
	}
	var body string
	e := h.Store.Auth.InTenant(r.Context(), session, policy.ProposalRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		e := tx.QueryRow(r.Context(), `SELECT b.body FROM app.briefs b JOIN app.objects o ON o.workspace_id=b.workspace_id AND o.id=b.id WHERE b.workspace_id=$1 AND b.id=$2 AND o.deleted_at IS NULL`, tx.WorkspaceID(), id).Scan(&body)
		if errors.Is(e, pgx.ErrNoRows) {
			return auth.ErrNotFound
		}
		return e
	})
	respond(w, map[string]interface{}{"id": id, "body": body}, e)
}
