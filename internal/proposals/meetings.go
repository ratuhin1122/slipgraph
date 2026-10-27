package proposals

import (
	"context"
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
	"strings"
	"time"
)

type Evidence struct {
	SegmentID uuid.UUID `json:"segment_id"`
	StartMS   int64     `json:"start_ms"`
	EndMS     int64     `json:"end_ms"`
	Quote     string    `json:"quote"`
}
type transcriptData struct {
	SegmentID    uuid.UUID `json:"segment_id"`
	MeetingID    uuid.UUID `json:"meeting_id"`
	ProjectID    uuid.UUID `json:"project_id"`
	ProjectStart time.Time `json:"project_start"`
	StartMS      int64     `json:"start_ms"`
	EndMS        int64     `json:"end_ms"`
	Speaker      string    `json:"speaker"`
	Text         string    `json:"text"`
}

func transcriptSource(ctx context.Context, tx database.TenantTx, h policy.Actor, id uuid.UUID) (transcriptData, error) {
	var d transcriptData
	if !policy.Can(ctx, h, policy.MeetingRead, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return d, auth.ErrForbidden
	}
	e := tx.QueryRow(ctx, `SELECT t.id,m.id,p.id,p.start_at,t.start_ms,t.end_ms,t.speaker,t.text FROM app.transcript_segments t JOIN app.objects o ON o.workspace_id=t.workspace_id AND o.id=t.id JOIN app.links parent ON parent.workspace_id=t.workspace_id AND parent.from_object=t.id AND parent.link_type='belongs_to' JOIN app.meetings m ON m.workspace_id=parent.workspace_id AND m.id=parent.to_object JOIN app.objects mo ON mo.workspace_id=m.workspace_id AND mo.id=m.id JOIN app.links project ON project.workspace_id=m.workspace_id AND project.from_object=m.id AND project.link_type='belongs_to' JOIN app.projects p ON p.workspace_id=project.workspace_id AND p.id=project.to_object JOIN app.objects po ON po.workspace_id=p.workspace_id AND po.id=p.id WHERE t.workspace_id=$1 AND t.id=$2 AND o.deleted_at IS NULL AND mo.deleted_at IS NULL AND po.deleted_at IS NULL AND m.status IN ('transcribing','ready')`, tx.WorkspaceID(), id).Scan(&d.SegmentID, &d.MeetingID, &d.ProjectID, &d.ProjectStart, &d.StartMS, &d.EndMS, &d.Speaker, &d.Text)
	if errors.Is(e, pgx.ErrNoRows) {
		e = auth.ErrNotFound
	}
	return d, e
}
func (e Evidence) Validate() bool {
	return e.SegmentID != uuid.Nil && e.StartMS >= 0 && e.EndMS > e.StartMS && strings.TrimSpace(e.Quote) != "" && len(e.Quote) <= 1000 && !strings.ContainsRune(e.Quote, 0)
}
func (e Evidence) check(ctx context.Context, tx database.TenantTx, h policy.Actor) (transcriptData, error) {
	d, err := transcriptSource(ctx, tx, h, e.SegmentID)
	if err != nil {
		return d, err
	}
	if !e.Validate() || d.StartMS != e.StartMS || d.EndMS != e.EndMS || !strings.Contains(d.Text, e.Quote) {
		return d, auth.ErrInvalid
	}
	return d, nil
}

type MeetingTask struct {
	Task     CreateTask `json:"task"`
	Evidence Evidence   `json:"evidence"`
}

func (p MeetingTask) Validate() bool { return p.Task.Validate() && p.Evidence.Validate() }
func (p MeetingTask) Preview(ctx context.Context, tx database.TenantTx, h policy.Actor) (interface{}, interface{}, error) {
	d, e := p.Evidence.check(ctx, tx, h)
	if e != nil {
		return nil, nil, e
	}
	if d.ProjectID != p.Task.ProjectID {
		return nil, nil, auth.ErrInvalid
	}
	if _, _, e := p.Task.Preview(ctx, tx, h); e != nil {
		return nil, nil, e
	}
	return map[string]interface{}{}, p, nil
}
func (p MeetingTask) Execute(ctx context.Context, tx database.TenantTx, h policy.Actor) error {
	if _, _, e := p.Preview(ctx, tx, h); e != nil {
		return e
	}
	task, e := tasks.CreateTx(ctx, tx, h, p.Task.ProjectID, p.Task.input())
	if e != nil {
		return e
	}
	if e = linkSource(ctx, tx, task.ID, p.Evidence.SegmentID); e != nil {
		return e
	}
	user, e := uuid.Parse(h.ID)
	if e != nil {
		return e
	}
	return events.WriteAudited(ctx, tx, task.ID, user, "task.evidence_linked", events.Payload{Before: map[string]string{}, After: map[string]interface{}{"segment_id": p.Evidence.SegmentID, "start_ms": p.Evidence.StartMS, "end_ms": p.Evidence.EndMS}})
}

type RecordDecision struct {
	Title    string   `json:"title"`
	Details  string   `json:"details"`
	Evidence Evidence `json:"evidence"`
}

func (p RecordDecision) Validate() bool {
	return strings.TrimSpace(p.Title) != "" && len(p.Title) <= 200 && len(p.Details) <= 4000 && !strings.ContainsRune(p.Title+p.Details, 0) && p.Evidence.Validate()
}
func (p RecordDecision) Preview(ctx context.Context, tx database.TenantTx, h policy.Actor) (interface{}, interface{}, error) {
	if !p.Validate() {
		return nil, nil, auth.ErrInvalid
	}
	if !policy.Can(ctx, h, policy.DecisionWrite, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return nil, nil, auth.ErrForbidden
	}
	_, e := p.Evidence.check(ctx, tx, h)
	return map[string]interface{}{}, p, e
}
func (p RecordDecision) Execute(ctx context.Context, tx database.TenantTx, h policy.Actor) error {
	if _, _, e := p.Preview(ctx, tx, h); e != nil {
		return e
	}
	id, e := uuid.NewV7()
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'decision',$3)`, id, tx.WorkspaceID(), h.ID); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.decisions(id,workspace_id,title,details) VALUES($1,$2,$3,$4)`, id, tx.WorkspaceID(), p.Title, p.Details); e != nil {
		return e
	}
	if e = linkSource(ctx, tx, id, p.Evidence.SegmentID); e != nil {
		return e
	}
	user, e := uuid.Parse(h.ID)
	if e != nil {
		return e
	}
	return events.WriteAudited(ctx, tx, id, user, "decision.recorded", events.Payload{Before: map[string]string{}, After: map[string]interface{}{"segment_id": p.Evidence.SegmentID, "start_ms": p.Evidence.StartMS, "end_ms": p.Evidence.EndMS}})
}
func (s *Store) QueueMeetingExtraction(ctx context.Context, tx database.TenantTx, service, human policy.Actor, ids []uuid.UUID) error {
	if e := s.Auth.AuthorizeConsumer(ctx, tx, service); e != nil {
		return e
	}
	if !policy.Can(ctx, service, policy.AgentRun, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return auth.ErrForbidden
	}
	batch := []uuid.UUID{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		_, e := s.queue(ctx, tx, human, "meeting_extraction", batch, map[string]string{}, nil)
		batch = []uuid.UUID{}
		return e
	}
	for _, id := range ids {
		if len(batch) == 8 {
			if e := flush(); e != nil {
				return e
			}
		}
		candidate := append(append([]uuid.UUID{}, batch...), id)
		content, e := sourceContent(ctx, tx, human, candidate)
		if e != nil {
			return e
		}
		_, e = llm.Assemble(content)
		if e != nil {
			if e := flush(); e != nil {
				return e
			}
			candidate = []uuid.UUID{id}
			content, e = sourceContent(ctx, tx, human, candidate)
			if e != nil {
				return e
			}
			if _, e = llm.Assemble(content); e != nil {
				return e
			}
		}
		batch = candidate
	}
	return flush()
}

type extractedDecision struct {
	SegmentID uuid.UUID `json:"segment_id"`
	Quote     string    `json:"quote"`
	Title     string    `json:"title"`
	Details   string    `json:"details"`
}
type extractedAction struct {
	SegmentID       uuid.UUID `json:"segment_id"`
	Quote           string    `json:"quote"`
	Title           string    `json:"title"`
	Description     string    `json:"description"`
	Start           time.Time `json:"start"`
	Due             time.Time `json:"due"`
	EstimateMinutes int64     `json:"estimate_minutes"`
	Milestone       bool      `json:"milestone"`
}

func (s *Store) completeMeeting(ctx context.Context, tx database.TenantTx, h, service policy.Actor, run uuid.UUID, raw []byte, ids []uuid.UUID) (string, error) {
	var out struct {
		Decisions []extractedDecision `json:"decisions"`
		Actions   []extractedAction   `json:"actions"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", auth.ErrInvalid
	}
	allowed := map[uuid.UUID]bool{}
	for _, id := range ids {
		allowed[id] = true
	}
	evidence := func(id uuid.UUID, quote string) (Evidence, transcriptData, error) {
		if !allowed[id] {
			return Evidence{}, transcriptData{}, auth.ErrInvalid
		}
		d, e := transcriptSource(ctx, tx, h, id)
		if e != nil {
			return Evidence{}, d, e
		}
		ev := Evidence{SegmentID: id, StartMS: d.StartMS, EndMS: d.EndMS, Quote: quote}
		_, e = ev.check(ctx, tx, h)
		return ev, d, e
	}
	result := emptyResult()
	for _, d := range out.Decisions {
		ev, _, e := evidence(d.SegmentID, d.Quote)
		if e != nil {
			return "", e
		}
		raw, e := json.Marshal(RecordDecision{Title: d.Title, Details: d.Details, Evidence: ev})
		if e != nil {
			return "", e
		}
		p, e := create(ctx, tx, h, service, run, "record_decision", raw, []uuid.UUID{d.SegmentID})
		if e != nil {
			return "", e
		}
		result.ProposalIDs = append(result.ProposalIDs, p.ID)
	}
	for _, a := range out.Actions {
		ev, d, e := evidence(a.SegmentID, a.Quote)
		if e != nil {
			return "", e
		}
		payload := MeetingTask{Task: CreateTask{ProjectID: d.ProjectID, Title: a.Title, Description: a.Description, Start: a.Start, Due: a.Due, EstimateMinutes: a.EstimateMinutes, Milestone: a.Milestone}, Evidence: ev}
		raw, e := json.Marshal(payload)
		if e != nil {
			return "", e
		}
		p, e := create(ctx, tx, h, service, run, "meeting_task", raw, []uuid.UUID{a.SegmentID})
		if e != nil {
			return "", e
		}
		result.ProposalIDs = append(result.ProposalIDs, p.ID)
	}
	return "completed", saveResult(ctx, tx, run, result)
}
