package proposals

import (
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/platform/contract"
	"github.com/slipgraph/slipgraph/internal/policy"
	"net/http"
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
