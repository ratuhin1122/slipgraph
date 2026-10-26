package chat

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

func respond(w http.ResponseWriter, value interface{}, err error) {
	if err != nil {
		auth.Error(w, err)
		return
	}
	auth.JSON(w, 200, value)
}
func (h *HTTP) ListChannels(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Identity.Require(w, r, policy.ChannelRead)
	if !ok {
		return
	}
	v, e := h.Store.Channels(r.Context(), s)
	respond(w, v, e)
}
func (h *HTTP) CreateChannel(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Identity.Require(w, r, policy.ChannelCreate)
	if !ok {
		return
	}
	var b contract.CreateChannelInput
	if e := auth.Decode(w, r, &b); e != nil {
		auth.Error(w, e)
		return
	}
	v, e := h.Store.CreateChannel(r.Context(), s, b.Name)
	respond(w, v, e)
}
func (h *HTTP) GetChannel(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s, ok := h.Identity.Require(w, r, policy.ChannelRead)
	if !ok {
		return
	}
	v, e := h.Store.Channel(r.Context(), s, id)
	respond(w, v, e)
}
func (h *HTTP) ListMessages(w http.ResponseWriter, r *http.Request, id uuid.UUID, p contract.ListMessagesParams) {
	s, ok := h.Identity.Require(w, r, policy.ChannelRead)
	if !ok {
		return
	}
	limit := 50
	if p.Limit != nil {
		limit = *p.Limit
	}
	v, e := h.Store.Messages(r.Context(), s, id, p.ThreadId, p.Before, limit)
	respond(w, v, e)
}
func (h *HTTP) PostMessage(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s, ok := h.Identity.Require(w, r, policy.MessagePost)
	if !ok {
		return
	}
	var b contract.PostMessageInput
	if e := auth.Decode(w, r, &b); e != nil {
		auth.Error(w, e)
		return
	}
	v, e := h.Store.Post(r.Context(), s, id, Input{Body: b.Body, MentionIDs: b.MentionIds, ThreadID: b.ThreadId, ClientID: b.ClientId})
	respond(w, v, e)
}
func (h *HTTP) EditMessage(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s, ok := h.Identity.Require(w, r, policy.MessageEdit)
	if !ok {
		return
	}
	var b contract.EditMessageInput
	if e := auth.Decode(w, r, &b); e != nil {
		auth.Error(w, e)
		return
	}
	v, e := h.Store.Change(r.Context(), s, id, b.Version, b.Body, b.MentionIds, false)
	respond(w, v, e)
}
func (h *HTTP) DeleteMessage(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s, ok := h.Identity.Require(w, r, policy.MessageDelete)
	if !ok {
		return
	}
	var b contract.VersionInput
	if e := auth.Decode(w, r, &b); e != nil {
		auth.Error(w, e)
		return
	}
	_, e := h.Store.Change(r.Context(), s, id, b.Version, "", nil, true)
	if e != nil {
		auth.Error(w, e)
		return
	}
	w.WriteHeader(204)
}
