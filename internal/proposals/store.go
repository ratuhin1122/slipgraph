package proposals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/llm"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/tasks"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Store struct{ Auth *auth.Service }
type SourceLink struct {
	ID    uuid.UUID `json:"id"`
	Label string    `json:"label"`
	Href  string    `json:"href"`
}
type Proposal struct {
	SourceLinks     []SourceLink    `json:"source_links"`
	ID              uuid.UUID       `json:"id"`
	Kind            string          `json:"kind"`
	Payload         json.RawMessage `json:"payload"`
	PayloadHash     string          `json:"payload_hash"`
	ApprovalHash    *string         `json:"approval_hash"`
	Status          string          `json:"status"`
	CreatedByAgent  uuid.UUID       `json:"created_by_agent"`
	RequestedByUser uuid.UUID       `json:"requested_by_user"`
	ApprovedBy      *uuid.UUID      `json:"approved_by"`
	ExpiresAt       time.Time       `json:"expires_at"`
	ExecutedAt      *time.Time      `json:"executed_at"`
	SourceObjectIDs []uuid.UUID     `json:"source_object_ids"`
	Before          json.RawMessage `json:"before"`
	After           json.RawMessage `json:"after"`
	Version         int64           `json:"version"`
	RejectionReason *string         `json:"rejection_reason"`
	FailureCode     *string         `json:"failure_code"`
}
type Page struct {
	Items      []Proposal `json:"items"`
	NextCursor *uuid.UUID `json:"next_cursor"`
}

const selectProposal = `SELECT id,kind,payload,payload_hash,approval_hash,status,created_by_agent,requested_by_user,approved_by,expires_at,executed_at,before_state,after_state,version,rejection_reason,failure_code FROM app.proposals WHERE workspace_id=$1`

func scan(row pgx.Row) (Proposal, error) {
	var p Proposal
	err := row.Scan(&p.ID, &p.Kind, &p.Payload, &p.PayloadHash, &p.ApprovalHash, &p.Status, &p.CreatedByAgent, &p.RequestedByUser, &p.ApprovedBy, &p.ExpiresAt, &p.ExecutedAt, &p.Before, &p.After, &p.Version, &p.RejectionReason, &p.FailureCode)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	return p, err
}
func sources(ctx context.Context, tx database.TenantTx, id uuid.UUID) ([]uuid.UUID, error) {
	result := []uuid.UUID{}
	rows, err := tx.Query(ctx, `SELECT to_object FROM app.links WHERE workspace_id=$1 AND from_object=$2 AND link_type='source' ORDER BY to_object`, tx.WorkspaceID(), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}
func get(ctx context.Context, tx database.TenantTx, id uuid.UUID, lock bool) (Proposal, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	p, err := scan(tx.QueryRow(ctx, selectProposal+` AND id=$2`+suffix, tx.WorkspaceID(), id))
	if err == nil {
		p.SourceObjectIDs, err = sources(ctx, tx, p.ID)
	}
	if err == nil {
		p.SourceLinks, err = sourceLinks(ctx, tx, p.SourceObjectIDs)
	}
	return p, err
}
func hash(workspace uuid.UUID, p Proposal) (string, error) {
	payload, e := llm.JSON(p.Payload)
	if e != nil {
		return "", e
	}
	before, e := llm.JSON(p.Before)
	if e != nil {
		return "", e
	}
	after, e := llm.JSON(p.After)
	if e != nil {
		return "", e
	}
	data, e := json.Marshal([]interface{}{"proposal-v1", workspace, p.ID, p.Kind, payload, before, after, p.SourceObjectIDs, p.RequestedByUser, p.CreatedByAgent, p.ExpiresAt.UTC()})
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// All proposal transitions take the audit lock first, matching dispatcher ordering.
func lockAudit(ctx context.Context, tx database.TenantTx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9401))`, tx.WorkspaceID().String())
	return err
}
func emit(ctx context.Context, tx database.TenantTx, p Proposal, actor uuid.UUID, kind, before string) error {
	return events.WriteAudited(ctx, tx, p.ID, actor, kind, events.Payload{Version: p.Version, Before: map[string]string{"status": before}, After: map[string]interface{}{"status": p.Status, "payload_hash": p.PayloadHash, "proposal_kind": p.Kind, "approved_by": p.ApprovedBy}})
}
func linkSource(ctx context.Context, tx database.TenantTx, from, to uuid.UUID) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.links(id,workspace_id,from_object,to_object,link_type) VALUES($1,$2,$3,$4,'source')`, id, tx.WorkspaceID(), from, to)
	return err
}
func sourceContent(ctx context.Context, tx database.TenantTx, a policy.Actor, ids []uuid.UUID) ([]string, error) {
	if len(ids) == 0 || len(ids) > 8 {
		return nil, auth.ErrInvalid
	}
	content := []string{}
	for _, id := range ids {
		var kind string
		if err := tx.QueryRow(ctx, `SELECT type FROM app.objects WHERE workspace_id=$1 AND id=$2 AND deleted_at IS NULL`, tx.WorkspaceID(), id).Scan(&kind); err != nil {
			return nil, auth.ErrNotFound
		}
		var text string
		switch kind {
		case "transcript_segment":
			segment, e := transcriptSource(ctx, tx, a, id)
			if e != nil {
				return nil, e
			}
			data, e := json.Marshal(segment)
			if e != nil {
				return nil, e
			}
			text = string(data)
		case "brief":
			if !policy.Can(ctx, a, policy.ProposalRead, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
				return nil, auth.ErrForbidden
			}
			if err := tx.QueryRow(ctx, `SELECT body FROM app.briefs WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id).Scan(&text); err != nil {
				return nil, err
			}
		case "message":
			if !policy.Can(ctx, a, policy.ChannelRead, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
				return nil, auth.ErrForbidden
			}
			if err := tx.QueryRow(ctx, `SELECT body FROM app.messages WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id).Scan(&text); err != nil {
				return nil, err
			}
		case "task":
			t, err := tasks.ReadTx(ctx, tx, a, id)
			if err != nil {
				return nil, err
			}
			data, err := json.Marshal(t)
			if err != nil {
				return nil, err
			}
			text = string(data)
		case "project":
			p, err := tasks.ProjectTx(ctx, tx, a, id)
			if err != nil {
				return nil, err
			}
			data, err := json.Marshal(p)
			if err != nil {
				return nil, err
			}
			text = string(data)
		default:
			return nil, auth.ErrInvalid
		}
		content = append(content, id.String()+" "+text)
	}
	return content, nil
}

// create is private: only the metered worker can turn model output into pending proposals.
func create(ctx context.Context, tx database.TenantTx, a, service policy.Actor, run uuid.UUID, kind string, data []byte, sourceIDs []uuid.UUID) (Proposal, error) {
	var p Proposal
	e, canonical, err := Decode(kind, data)
	if err != nil {
		return p, err
	}
	if _, err := sourceContent(ctx, tx, a, sourceIDs); err != nil {
		return p, err
	}
	before, after, err := e.Preview(ctx, tx, a)
	if err != nil {
		return p, err
	}
	p.Before, err = json.Marshal(before)
	if err != nil {
		return p, err
	}
	p.After, err = json.Marshal(after)
	if err != nil {
		return p, err
	}
	p.ID, err = uuid.NewV7()
	if err != nil {
		return p, err
	}
	p.Kind = kind
	p.Payload = canonical
	p.Status = "pending"
	p.Version = 1
	p.CreatedByAgent = run
	p.RequestedByUser = uuid.MustParse(a.ID)
	p.ExpiresAt = time.Now().UTC().Truncate(time.Microsecond).Add(24 * time.Hour)
	p.SourceObjectIDs = append([]uuid.UUID{}, sourceIDs...)
	sort.Slice(p.SourceObjectIDs, func(i, j int) bool { return p.SourceObjectIDs[i].String() < p.SourceObjectIDs[j].String() })
	p.PayloadHash, err = hash(tx.WorkspaceID(), p)
	if err != nil {
		return p, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'proposal',$3)`, p.ID, tx.WorkspaceID(), a.ID); err != nil {
		return p, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.proposals(id,workspace_id,kind,payload,payload_hash,created_by_agent,requested_by_user,expires_at,before_state,after_state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, p.ID, tx.WorkspaceID(), p.Kind, p.Payload, p.PayloadHash, run, p.RequestedByUser, p.ExpiresAt, p.Before, p.After); err != nil {
		return p, err
	}
	for _, source := range p.SourceObjectIDs {
		if err := linkSource(ctx, tx, p.ID, source); err != nil {
			return p, err
		}
	}
	return p, events.WriteAgentAudited(ctx, tx, service, p.ID, run, "proposal.created", events.Payload{Version: 1, After: map[string]interface{}{"status": "pending", "requested_by_user": p.RequestedByUser, "payload_hash": p.PayloadHash}})
}
func (s *Store) List(ctx context.Context, session auth.Session, before *uuid.UUID) (Page, error) {
	page := Page{Items: []Proposal{}}
	err := s.Auth.InTenant(ctx, session, policy.ProposalRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		rows, err := tx.Query(ctx, selectProposal+` AND ($2::uuid IS NULL OR id<$2) ORDER BY id DESC LIMIT 51`, tx.WorkspaceID(), before)
		if err != nil {
			return err
		}
		for rows.Next() {
			p, e := scan(rows)
			if e != nil {
				rows.Close()
				return e
			}
			page.Items = append(page.Items, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(page.Items) > 50 {
			page.Items = page.Items[:50]
			id := page.Items[49].ID
			page.NextCursor = &id
		}
		for i := range page.Items {
			page.Items[i].SourceObjectIDs, err = sources(ctx, tx, page.Items[i].ID)
			if err != nil {
				return err
			}
			page.Items[i].SourceLinks, err = sourceLinks(ctx, tx, page.Items[i].SourceObjectIDs)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return page, err
}
func (s *Store) Get(ctx context.Context, session auth.Session, id uuid.UUID) (Proposal, error) {
	var p Proposal
	err := s.Auth.InTenant(ctx, session, policy.ProposalRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		var e error
		p, e = get(ctx, tx, id, false)
		return e
	})
	return p, err
}
func (s *Store) Decide(ctx context.Context, session auth.Session, id uuid.UUID, version int64, expected, reason string, approve bool) (Proposal, error) {
	var p Proposal
	if version < 1 || len(expected) != 64 || (!approve && (strings.TrimSpace(reason) == "" || len(reason) > 1000 || strings.ContainsRune(reason, 0))) {
		return p, auth.ErrInvalid
	}
	err := s.Auth.InTenant(ctx, session, policy.ProposalDecide, "", func(tx database.TenantTx, a policy.Actor) error {
		if err := lockAudit(ctx, tx); err != nil {
			return err
		}
		var err error
		p, err = get(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if p.Status != "pending" || p.Version != version {
			return auth.ErrConflict
		}
		h, err := hash(tx.WorkspaceID(), p)
		if err != nil || h != p.PayloadHash || h != expected {
			return auth.ErrConflict
		}
		var alive bool
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()<$1`, p.ExpiresAt).Scan(&alive); err != nil {
			return err
		}
		if !alive {
			return auth.ErrConflict
		}
		kind := "proposal.rejected"
		p.Status = "rejected"
		p.RejectionReason = &reason
		if approve {
			e, _, err := Decode(p.Kind, p.Payload)
			if err != nil {
				return err
			}
			if _, _, err := e.Preview(ctx, tx, a); err != nil {
				return err
			}
			if _, err := sourceContent(ctx, tx, a, p.SourceObjectIDs); err != nil {
				return err
			}
			p.Status = "approved"
			p.ApprovedBy = &session.User.ID
			p.ApprovalHash = &h
			p.RejectionReason = nil
			kind = "proposal.approved"
		}
		p.Version++
		if _, err := tx.Exec(ctx, `UPDATE app.proposals SET status=$3,approved_by=$4,approval_hash=$5,rejection_reason=$6,version=$7 WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id, p.Status, p.ApprovedBy, p.ApprovalHash, p.RejectionReason, p.Version); err != nil {
			return err
		}
		return emit(ctx, tx, p, session.User.ID, kind, "pending")
	})
	return p, err
}
func (s *Store) Execute(ctx context.Context, service policy.Actor, id uuid.UUID) error {
	return s.Auth.InConsumer(ctx, service, func(tx database.TenantTx) error { return s.execute(ctx, tx, service, id) })
}
func (s *Store) execute(ctx context.Context, tx database.TenantTx, service policy.Actor, id uuid.UUID) error {
	if !policy.Can(ctx, service, policy.ProposalExecute, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return auth.ErrForbidden
	}
	if err := lockAudit(ctx, tx); err != nil {
		return err
	}
	p, err := get(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if p.Status != "approved" || p.ApprovedBy == nil || p.ApprovalHash == nil || p.ExecutedAt != nil {
		return auth.ErrConflict
	}
	var alive bool
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()<$1`, p.ExpiresAt).Scan(&alive); err != nil {
		return err
	}
	if !alive {
		return auth.ErrConflict
	}
	h, err := hash(tx.WorkspaceID(), p)
	if err != nil || h != p.PayloadHash || h != *p.ApprovalHash {
		return auth.ErrConflict
	}
	human, err := s.Auth.JobMember(ctx, tx, *p.ApprovedBy, policy.ProposalDecide)
	if err != nil {
		return err
	}
	if !policy.Can(ctx, human, policy.TaskWrite, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return auth.ErrForbidden
	}
	if _, err := sourceContent(ctx, tx, human, p.SourceObjectIDs); err != nil {
		return err
	}
	executor, _, err := Decode(p.Kind, p.Payload)
	if err != nil {
		return err
	}
	if err := executor.Execute(tasks.WithSynchronousAudit(ctx), tx, human); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE app.proposals SET status='executed',executed_at=clock_timestamp(),version=version+1 WHERE workspace_id=$1 AND id=$2 AND status='approved' AND executed_at IS NULL AND expires_at>clock_timestamp()`, tx.WorkspaceID(), id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return auth.ErrConflict
	}
	p.Status = "executed"
	p.Version++
	return emit(ctx, tx, p, *p.ApprovedBy, "proposal.executed", "approved")
}

// Consume treats permanent refusal as a terminal proposal state, not a poison outbox event.
func (s *Store) Consume(ctx context.Context, tx database.TenantTx, service policy.Actor, eventID uuid.UUID) error {
	if err := s.Auth.AuthorizeConsumer(ctx, tx, service); err != nil {
		return err
	}
	var kind string
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT event_type,object_id FROM app.events WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), eventID).Scan(&kind, &id); err != nil {
		return err
	}
	if kind != "proposal.approved" {
		return nil
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT proposal_execution"); err != nil {
		return err
	}
	err := s.execute(ctx, tx, service, id)
	if err == nil {
		_, err = tx.Exec(ctx, "RELEASE SAVEPOINT proposal_execution")
		return err
	}
	if _, rollback := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT proposal_execution"); rollback != nil {
		return rollback
	}
	if !errors.Is(err, auth.ErrConflict) && !errors.Is(err, auth.ErrForbidden) && !errors.Is(err, auth.ErrInvalid) && !errors.Is(err, auth.ErrNotFound) {
		return err
	}
	if err := lockAudit(ctx, tx); err != nil {
		return err
	}
	p, err := get(ctx, tx, id, true)
	if errors.Is(err, auth.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if p.Status != "approved" {
		return nil
	}
	p.Status = "failed"
	if time.Now().After(p.ExpiresAt) {
		p.Status = "expired"
	}
	p.Version++
	if _, err := tx.Exec(ctx, `UPDATE app.proposals SET status=$3,failure_code='execution_refused',version=$4 WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id, p.Status, p.Version); err != nil {
		return err
	}
	return events.WriteWorkerAudited(ctx, tx, service, p.ID, "proposal.failed", events.Payload{Version: p.Version, Before: map[string]string{"status": "approved"}, After: map[string]string{"status": p.Status, "failure_code": "execution_refused"}})
}

func sourceLinks(ctx context.Context, tx database.TenantTx, ids []uuid.UUID) ([]SourceLink, error) {
	out := []SourceLink{}
	for _, id := range ids {
		var kind string
		var parent *uuid.UUID
		err := tx.QueryRow(ctx, `SELECT o.type,l.to_object FROM app.objects o LEFT JOIN app.links l ON l.workspace_id=o.workspace_id AND l.from_object=o.id AND l.link_type='belongs_to' WHERE o.workspace_id=$1 AND o.id=$2 AND o.deleted_at IS NULL`, tx.WorkspaceID(), id).Scan(&kind, &parent)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		link := SourceLink{ID: id, Label: kind}
		switch kind {
		case "task":
			if parent != nil {
				link.Href = "/projects/" + parent.String() + "?task=" + id.String()
			}
		case "message":
			if parent != nil {
				var root *uuid.UUID
				if err := tx.QueryRow(ctx, `SELECT thread_id FROM app.messages WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id).Scan(&root); err != nil {
					return nil, err
				}
				thread := id
				if root != nil {
					thread = *root
				}
				link.Href = "/chat/" + parent.String() + "?thread=" + thread.String()
			}
		case "transcript_segment":
			var meeting uuid.UUID
			var startMS int64
			if e := tx.QueryRow(ctx, `SELECT l.to_object,t.start_ms FROM app.transcript_segments t JOIN app.links l ON l.workspace_id=t.workspace_id AND l.from_object=t.id AND l.link_type='belongs_to' WHERE t.workspace_id=$1 AND t.id=$2`, tx.WorkspaceID(), id).Scan(&meeting, &startMS); e != nil {
				return nil, e
			}
			link.Href = "/meetings/" + meeting.String() + "?segment=" + id.String() + "&t=" + strconv.FormatFloat(float64(startMS)/1000, 'f', 3, 64)
		case "brief":
			link.Href = "/plans?brief=" + id.String()
		case "project":
			link.Href = "/projects/" + id.String()
		}
		if link.Href != "" {
			out = append(out, link)
		}
	}
	return out, nil
}
