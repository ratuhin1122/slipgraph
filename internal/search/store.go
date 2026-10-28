package search

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

type Store struct{ Auth *auth.Service }
type Result struct {
	ID     uuid.UUID `json:"id"`
	Status string    `json:"status"`
	Items  []Hit     `json:"items"`
	Claims []Claim   `json:"claims"`
	Mock   bool      `json:"mock"`
}

func (s *Store) Request(ctx context.Context, session auth.Session, query, kind string) (Result, error) {
	out := Result{Items: []Hit{}, Claims: []Claim{}}
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 500 || !utf8.ValidString(query) || (kind != "search" && kind != "ask") {
		return out, auth.ErrInvalid
	}
	if e := s.Auth.Rate(ctx, "search:request:"+session.WorkspaceID.String(), session.User.ID.String(), 30, time.Minute); e != nil {
		return out, e
	}
	action := policy.SearchRead
	if kind == "ask" {
		action = policy.SearchAsk
	}
	e := s.Auth.InTenant(ctx, session, action, "", func(tx database.TenantTx, a policy.Actor) error {
		var e error
		out.ID, e = uuid.NewV7()
		if e != nil {
			return e
		}
		out.Status = "queued"
		if _, e = tx.Exec(ctx, `INSERT INTO app.objects(workspace_id,id,type,created_by) VALUES($1,$2,'search_request',$3)`, tx.WorkspaceID(), out.ID, a.ID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO app.search_requests(workspace_id,id,requested_by,query,kind) VALUES($1,$2,$3,$4,$5)`, tx.WorkspaceID(), out.ID, a.ID, query, kind); e != nil {
			return e
		}
		return events.WriteAudited(ctx, tx, out.ID, session.User.ID, "search.queued", events.Payload{After: map[string]string{"kind": kind}})
	})
	return out, e
}

// Get never trusts stored snippets or citations after permissions/content change.
func (s *Store) Get(ctx context.Context, session auth.Session, id uuid.UUID) (Result, error) {
	out := Result{ID: id, Items: []Hit{}, Claims: []Claim{}}
	e := s.Auth.InTenant(ctx, session, policy.SearchRead, "", func(tx database.TenantTx, a policy.Actor) error {
		var data []byte
		if e := tx.QueryRow(ctx, `SELECT status,result FROM app.search_requests WHERE workspace_id=$1 AND id=$2 AND requested_by=$3 AND created_at>now()-interval '24 hours'`, tx.WorkspaceID(), id, session.User.ID).Scan(&out.Status, &data); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return e
		}
		if out.Status != "complete" {
			return nil
		}
		var saved Result
		if json.Unmarshal(data, &saved) != nil {
			return auth.ErrInvalid
		}
		out.Mock = saved.Mock
		unchanged := policy.Can(ctx, a, policy.SearchAsk, policy.Resource{WorkspaceID: a.WorkspaceID})
		for _, h := range saved.Items {
			d, e := load(ctx, tx, a, h.ObjectID)
			if errors.Is(e, auth.ErrNotFound) || errors.Is(e, auth.ErrForbidden) {
				unchanged = false
				continue
			}
			if e != nil {
				return e
			}
			if d.ContentHash != h.ContentHash {
				unchanged = false
				continue
			}
			// URLs and titles always come from live tenant metadata, never model output.
			h.URL = d.URL
			h.Title = d.Title
			out.Items = append(out.Items, h)
		}
		if unchanged && ValidateClaims(saved.Claims, out.Items) == nil {
			out.Claims = saved.Claims
		}
		return nil
	})
	return out, e
}
func ValidateClaims(claims []Claim, hits []Hit) error {
	data, e := json.Marshal(Answer{Claims: claims})
	if e != nil {
		return e
	}
	_, e = ValidateAnswer(data, hits)
	return e
}

// Consume only schedules embedding work; no provider I/O happens in an outbox tx.
func (s *Store) Consume(ctx context.Context, tx database.TenantTx, a policy.Actor, event uuid.UUID) error {
	if !policy.Can(ctx, a, policy.SearchIndex, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return auth.ErrForbidden
	}
	var object uuid.UUID
	var kind string
	if e := tx.QueryRow(ctx, `SELECT object_id,event_type FROM app.events WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), event).Scan(&object, &kind); e != nil {
		return e
	}
	if strings.HasPrefix(kind, "task.") || strings.HasPrefix(kind, "message.") || strings.HasPrefix(kind, "transcript_segment.") {
		_, e := tx.Exec(ctx, `INSERT INTO app.search_index_jobs(workspace_id,object_id) VALUES($1,$2) ON CONFLICT(workspace_id,object_id) DO UPDATE SET generation=app.search_index_jobs.generation+1,retry_at=now()`, tx.WorkspaceID(), object)
		return e
	}
	if kind == "meeting.transcribed" {
		_, e := tx.Exec(ctx, `INSERT INTO app.search_index_jobs(workspace_id,object_id) SELECT $1,o.id FROM app.objects o JOIN app.links l ON l.workspace_id=o.workspace_id AND l.from_object=o.id AND l.link_type='belongs_to' WHERE o.workspace_id=$1 AND o.type='transcript_segment' AND l.to_object=$2 ON CONFLICT(workspace_id,object_id) DO UPDATE SET generation=app.search_index_jobs.generation+1,retry_at=now()`, tx.WorkspaceID(), object)
		return e
	}
	return nil
}
