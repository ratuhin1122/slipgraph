package search

import (
	"net/http"
	"os"

	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/platform/contract"
	"github.com/slipgraph/slipgraph/internal/policy"
)

type HTTP struct {
	Store    *Store
	Identity *auth.HTTP
}

func (h *HTTP) request(w http.ResponseWriter, r *http.Request, kind string) {
	action := policy.SearchRead
	if kind == "ask" {
		action = policy.SearchAsk
	}
	session, ok := h.Identity.Require(w, r, action)
	if !ok {
		return
	}
	if len(r.URL.Query()) != 0 {
		auth.Error(w, auth.ErrInvalid)
		return
	}
	mode := os.Getenv("EMBEDDING_PROVIDER")
	if mode != "mock" && mode != "openai" {
		auth.JSON(w, 503, map[string]string{"error": "An administrator must enable the embedding worker provider."})
		return
	}
	if kind == "ask" {
		llm := os.Getenv("LLM_PROVIDER")
		if llm != "mock" && llm != "openai" && llm != "anthropic" {
			auth.JSON(w, 503, map[string]string{"error": "An administrator must enable the answer worker provider."})
			return
		}
	}
	var body contract.SearchInput
	if e := auth.Decode(w, r, &body); e != nil {
		auth.Error(w, e)
		return
	}
	result, e := h.Store.Request(r.Context(), session, body.Query, kind)
	if e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 202, result)
}
func (h *HTTP) RequestSearch(w http.ResponseWriter, r *http.Request) { h.request(w, r, "search") }
func (h *HTTP) RequestAnswer(w http.ResponseWriter, r *http.Request) { h.request(w, r, "ask") }
func (h *HTTP) GetSearchResult(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	session, ok := h.Identity.Require(w, r, policy.SearchRead)
	if !ok {
		return
	}
	if len(r.URL.Query()) != 0 {
		auth.Error(w, auth.ErrInvalid)
		return
	}
	result, e := h.Store.Get(r.Context(), session, id)
	if e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 200, result)
}
