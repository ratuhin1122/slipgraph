package proposals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/chat"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/risk"
	"github.com/slipgraph/slipgraph/internal/tasks"
	"strings"
	"time"
)

type Detection struct {
	IsDelay        bool     `json:"is_delay"`
	TaskCandidates []string `json:"task_candidates"`
	Days           int      `json:"days"`
	Confidence     float64  `json:"confidence"`
	EvidenceQuote  string   `json:"evidence_quote"`
}
type Candidate struct {
	ID    uuid.UUID `json:"id"`
	Title string    `json:"title"`
}
type DateChange struct {
	TaskID      uuid.UUID `json:"task_id"`
	Title       string    `json:"title"`
	BeforeStart time.Time `json:"before_start"`
	BeforeDue   time.Time `json:"before_due"`
	AfterStart  time.Time `json:"after_start"`
	AfterDue    time.Time `json:"after_due"`
	Reason      string    `json:"reason"`
	Milestone   bool      `json:"milestone"`
}
type WorkflowResult struct {
	Detection     *Detection       `json:"detection,omitempty"`
	Candidates    []Candidate      `json:"candidates"`
	ProposalIDs   []uuid.UUID      `json:"proposal_ids"`
	ProjectID     *uuid.UUID       `json:"project_id,omitempty"`
	Assessment    *risk.Assessment `json:"assessment,omitempty"`
	Changes       []DateChange     `json:"changes"`
	GraphVersion  int64            `json:"graph_version"`
	LaunchFlagged bool             `json:"launch_flagged"`
}
type Run struct {
	ID          uuid.UUID      `json:"id"`
	Status      string         `json:"status"`
	Kind        string         `json:"kind"`
	RequestedBy uuid.UUID      `json:"requested_by"`
	Result      WorkflowResult `json:"result"`
}

func emptyResult() WorkflowResult {
	return WorkflowResult{Candidates: []Candidate{}, ProposalIDs: []uuid.UUID{}, Changes: []DateChange{}}
}
func saveResult(ctx context.Context, tx database.TenantTx, run uuid.UUID, result WorkflowResult) error {
	b, e := json.Marshal(result)
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE app.agent_runs SET result=$3 WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), run, b)
	return e
}
func (s *Store) queue(ctx context.Context, tx database.TenantTx, human policy.Actor, kind string, sources []uuid.UUID, input interface{}, event *uuid.UUID) (uuid.UUID, error) {
	if !policy.Can(ctx, human, policy.AgentRequest, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return uuid.Nil, auth.ErrForbidden
	}
	id, e := uuid.NewV7()
	if e != nil {
		return id, e
	}
	b, e := json.Marshal(input)
	if e != nil {
		return id, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'agent_run',$3)`, id, tx.WorkspaceID(), human.ID); e != nil {
		return id, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.agent_runs(id,workspace_id,kind,requested_by_user,status,input,source_event_id) VALUES($1,$2,$3,$4,'queued',$5,$6)`, id, tx.WorkspaceID(), kind, human.ID, b, event); e != nil {
		return id, e
	}
	for _, source := range sources {
		if e = linkSource(ctx, tx, id, source); e != nil {
			return id, e
		}
	}
	user, e := uuid.Parse(human.ID)
	if e != nil {
		return id, e
	}
	return id, events.WriteAudited(ctx, tx, id, user, "agent.queued", events.Payload{After: map[string]string{"status": "queued", "kind": kind}})
}

// ConsumeMessage only queues durable work. Network calls happen after the outbox transaction.
func (s *Store) ConsumeMessage(ctx context.Context, tx database.TenantTx, service policy.Actor, event uuid.UUID) error {
	if e := s.Auth.AuthorizeConsumer(ctx, tx, service); e != nil {
		return e
	}
	if !policy.Can(ctx, service, policy.AgentRun, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return auth.ErrForbidden
	}
	var kind string
	var message uuid.UUID
	if e := tx.QueryRow(ctx, `SELECT event_type,object_id FROM app.events WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), event).Scan(&kind, &message); e != nil {
		return e
	}
	if kind != "message.created" {
		return nil
	}
	if e := lockAudit(ctx, tx); e != nil {
		return e
	}
	var user uuid.UUID
	var version int64
	e := tx.QueryRow(ctx, `SELECT o.created_by,m.version FROM app.messages m JOIN app.objects o ON o.workspace_id=m.workspace_id AND o.id=m.id WHERE m.workspace_id=$1 AND m.id=$2 AND m.system_run_id IS NULL AND o.deleted_at IS NULL`, tx.WorkspaceID(), message).Scan(&user, &version)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	human, e := s.Auth.JobMember(ctx, tx, user, policy.AgentRequest)
	if errors.Is(e, auth.ErrForbidden) {
		return nil
	}
	if e != nil {
		return e
	}
	var exists bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.agent_runs WHERE workspace_id=$1 AND source_event_id=$2)`, tx.WorkspaceID(), event).Scan(&exists); e != nil {
		return e
	}
	if exists {
		return nil
	}
	var count int
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM app.agent_runs WHERE workspace_id=$1 AND requested_by_user=$2 AND started_at>clock_timestamp()-interval '1 hour'`, tx.WorkspaceID(), user).Scan(&count); e != nil {
		return e
	}
	if count >= 10 {
		return nil
	}
	_, e = s.queue(ctx, tx, human, "delay_detection", []uuid.UUID{message}, map[string]int64{"message_version": version}, &event)
	return e
}
func (s *Store) RequestPlan(ctx context.Context, session auth.Session, brief string) (uuid.UUID, error) {
	var id uuid.UUID
	if strings.TrimSpace(brief) == "" || len(brief) > 8000 || strings.ContainsRune(brief, 0) {
		return id, auth.ErrInvalid
	}
	if e := s.Auth.Rate(ctx, "agent:request:"+session.WorkspaceID.String(), session.User.ID.String(), 10, time.Hour); e != nil {
		return id, e
	}
	e := s.Auth.InTenant(ctx, session, policy.AgentRequest, "", func(tx database.TenantTx, h policy.Actor) error {
		if e := lockAudit(ctx, tx); e != nil {
			return e
		}
		source, e := uuid.NewV7()
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'brief',$3)`, source, tx.WorkspaceID(), h.ID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO app.briefs(id,workspace_id,body) VALUES($1,$2,$3)`, source, tx.WorkspaceID(), brief); e != nil {
			return e
		}
		id, e = s.queue(ctx, tx, h, "create_plan", []uuid.UUID{source}, map[string]string{}, nil)
		return e
	})
	return id, e
}
func signalMessage(ctx context.Context, tx database.TenantTx, run uuid.UUID) (uuid.UUID, uuid.UUID, string, error) {
	var id, channel uuid.UUID
	var body string
	e := tx.QueryRow(ctx, `SELECT m.id,l.to_object,m.body FROM app.agent_runs r JOIN app.links src ON src.workspace_id=r.workspace_id AND src.from_object=r.id AND src.link_type='source' JOIN app.messages m ON m.workspace_id=src.workspace_id AND m.id=src.to_object JOIN app.objects o ON o.workspace_id=m.workspace_id AND o.id=m.id JOIN app.links l ON l.workspace_id=m.workspace_id AND l.from_object=m.id AND l.link_type='belongs_to' JOIN app.objects co ON co.workspace_id=l.workspace_id AND co.id=l.to_object WHERE r.workspace_id=$1 AND r.id=$2 AND m.system_run_id IS NULL AND m.version=(r.input->>'message_version')::bigint AND o.created_by=r.requested_by_user AND o.deleted_at IS NULL AND co.deleted_at IS NULL FOR SHARE OF m,o,co`, tx.WorkspaceID(), run).Scan(&id, &channel, &body)
	if errors.Is(e, pgx.ErrNoRows) {
		e = auth.ErrConflict
	}
	return id, channel, body, e
}
func (s *Store) prepare(ctx context.Context, tx database.TenantTx, h policy.Actor, run uuid.UUID, kind string, ids []uuid.UUID) ([]string, error) {
	if kind == "delay_detection" {
		_, _, body, e := signalMessage(ctx, tx, run)
		return []string{body}, e
	}
	return sourceContent(ctx, tx, h, ids)
}
func (s *Store) complete(ctx context.Context, tx database.TenantTx, h, service policy.Actor, run uuid.UUID, kind string, raw []byte, ids []uuid.UUID) (string, error) {
	if kind == "delay_detection" {
		var d Detection
		if json.Unmarshal(raw, &d) != nil {
			return "", auth.ErrInvalid
		}
		target, days, e := confirmedInput(ctx, tx, run)
		if e != nil {
			return "", e
		}
		if target != nil {
			d.Days = days
		}
		return s.delay(ctx, tx, h, service, run, d, target)
	}
	p, e := create(ctx, tx, h, service, run, kind, raw, ids)
	if e != nil {
		return "", e
	}
	result := emptyResult()
	result.ProposalIDs = append(result.ProposalIDs, p.ID)
	return "completed", saveResult(ctx, tx, run, result)
}
func resolveCandidates(ctx context.Context, tx database.TenantTx, h policy.Actor, message uuid.UUID, d Detection) ([]Candidate, error) {
	if !policy.Can(ctx, h, policy.TaskRead, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return nil, auth.ErrForbidden
	}
	// Task cards take precedence; otherwise names must match exactly (case insensitive).
	rows, e := tx.Query(ctx, `SELECT t.id,t.title FROM app.tasks t JOIN app.objects o ON o.workspace_id=t.workspace_id AND o.id=t.id JOIN app.links parent ON parent.workspace_id=t.workspace_id AND parent.from_object=t.id AND parent.link_type='belongs_to' JOIN app.objects po ON po.workspace_id=parent.workspace_id AND po.id=parent.to_object WHERE t.workspace_id=$1 AND o.deleted_at IS NULL AND po.deleted_at IS NULL AND t.status NOT IN ('done','cancelled') AND EXISTS(SELECT 1 FROM app.links l WHERE l.workspace_id=t.workspace_id AND l.from_object=$2 AND l.to_object=t.id AND l.link_type IN ('references','mentions')) ORDER BY t.id LIMIT 21`, tx.WorkspaceID(), message)
	if e != nil {
		return nil, e
	}
	read := func(rows pgx.Rows) ([]Candidate, error) {
		out := []Candidate{}
		defer rows.Close()
		for rows.Next() {
			var c Candidate
			if e := rows.Scan(&c.ID, &c.Title); e != nil {
				return nil, e
			}
			out = append(out, c)
		}
		return out, rows.Err()
	}
	linked, e := read(rows)
	if e != nil || len(linked) > 0 {
		return linked, e
	}
	names := []string{}
	for _, n := range d.TaskCandidates {
		names = append(names, strings.ToLower(strings.TrimSpace(n)))
	}
	rows, e = tx.Query(ctx, `SELECT t.id,t.title FROM app.tasks t JOIN app.objects o ON o.workspace_id=t.workspace_id AND o.id=t.id JOIN app.links parent ON parent.workspace_id=t.workspace_id AND parent.from_object=t.id AND parent.link_type='belongs_to' JOIN app.objects po ON po.workspace_id=parent.workspace_id AND po.id=parent.to_object WHERE t.workspace_id=$1 AND o.deleted_at IS NULL AND po.deleted_at IS NULL AND t.status NOT IN ('done','cancelled') AND (lower(t.title)=ANY($2::text[]) OR t.id::text=ANY($2::text[])) ORDER BY t.id LIMIT 21`, tx.WorkspaceID(), names)
	if e != nil {
		return nil, e
	}
	return read(rows)
}
func (s *Store) delay(ctx context.Context, tx database.TenantTx, h, service policy.Actor, run uuid.UUID, d Detection, confirmed *uuid.UUID) (string, error) {
	message, channel, body, e := signalMessage(ctx, tx, run)
	if e != nil {
		return "", e
	}
	result := emptyResult()
	result.Detection = &d
	if !d.IsDelay {
		return "ignored", saveResult(ctx, tx, run, result)
	}
	if d.Days < 1 || d.Days > 365 || strings.TrimSpace(d.EvidenceQuote) == "" || !strings.Contains(body, d.EvidenceQuote) {
		return "", auth.ErrInvalid
	}
	candidates, e := resolveCandidates(ctx, tx, h, message, d)
	if e != nil {
		return "", e
	}
	result.Candidates = candidates
	if confirmed == nil && (d.Confidence < 0.85 || len(candidates) != 1) {
		if e = saveResult(ctx, tx, run, result); e != nil {
			return "", e
		}
		e = chat.PostSystem(ctx, tx, service, h, run, channel, "Possible schedule delay. Please confirm the task and number of days before an impact is proposed. If no task matches, post again with a task card. No task dates have changed.", nil)
		return "needs_confirmation", e
	}
	var target uuid.UUID
	if confirmed != nil {
		target = *confirmed
	} else {
		target = candidates[0].ID
	}
	task, e := tasks.ReadTx(ctx, tx, h, target)
	if e != nil {
		return "", e
	}
	if task.Status == "done" || task.Status == "cancelled" {
		return "", auth.ErrConflict
	}
	loaded, e := risk.Load(ctx, tx, h, task.ProjectID, target.String(), true)
	if e != nil {
		return "", e
	}
	// Re-read under the project graph lock so the delay uses the same snapshot
	// as dependency analysis, even if an edit committed while acquiring it.
	task, e = tasks.ReadTx(ctx, tx, h, target)
	if e != nil {
		return "", e
	}
	if task.Status == "done" || task.Status == "cancelled" {
		return "", auth.ErrConflict
	}
	// Forecast the explicit due-date slip without mutating the graph or reported delay.
	due := task.Due.Add(time.Duration(d.Days) * 24 * time.Hour)
	assessment, e := risk.Analyze(loaded.Plan, &risk.Change{TaskID: target.String(), NewDue: &due})
	if e != nil {
		return "", e
	}
	result.ProjectID = &task.ProjectID
	result.GraphVersion = loaded.Version
	result.Assessment = &assessment
	result.LaunchFlagged = assessment.LaunchSlipMinutes > 0
	if len(assessment.Tasks) > 10000 {
		return "", auth.ErrLimited
	}
	blockers := map[string][]string{}
	finishes := map[string]risk.Impact{}
	for _, impact := range assessment.Tasks {
		finishes[impact.TaskID] = impact
	}
	for _, edge := range loaded.Plan.Dependencies {
		blockers[edge.TaskID] = append(blockers[edge.TaskID], edge.BlockerID)
	}
	for _, impact := range assessment.Tasks {
		if !impact.Affected && impact.TaskID != target.String() {
			continue
		}
		id, e := uuid.Parse(impact.TaskID)
		if e != nil {
			return "", e
		}
		t, e := tasks.ReadTx(ctx, tx, h, id)
		if e != nil {
			return "", e
		}
		newStart := t.Start
		newDue := t.Due
		if impact.EarliestStart.After(newStart) {
			newStart = impact.EarliestStart
		}
		if impact.EarliestFinish.After(newDue) {
			newDue = impact.EarliestFinish
		}
		if id == target && due.After(newDue) {
			newDue = due
		}
		if !newStart.After(t.Start) && !newDue.After(t.Due) {
			continue
		}
		if len(result.Changes) >= 100 {
			return "", auth.ErrLimited
		}
		reason := fmt.Sprintf("A dependency finishes later; %d minutes of slack consumed, %d minutes remaining.", impact.SlackConsumedMinutes, impact.RemainingSlackMinutes)
		names := []string{}
		for _, blocker := range blockers[impact.TaskID] {
			b := finishes[blocker]
			if !b.EarliestFinish.Equal(impact.EarliestStart) || len(names) >= 3 {
				continue
			}
			blockerID, e := uuid.Parse(blocker)
			if e != nil {
				return "", e
			}
			predecessor, e := tasks.ReadTx(ctx, tx, h, blockerID)
			if e != nil {
				return "", e
			}
			names = append(names, predecessor.Title)
		}
		if len(names) > 0 {
			reason = "Waiting for " + strings.Join(names, ", ") + ". " + reason
		}
		if id == target {
			reason = fmt.Sprintf("Message author reported a %d-day due-date slip.", d.Days)
		}
		if impact.Milestone && impact.MilestoneSlipMinutes > 0 {
			result.LaunchFlagged = true
			reason += " Launch milestone is at risk."
		}
		result.Changes = append(result.Changes, DateChange{TaskID: id, Title: t.Title, BeforeStart: t.Start, BeforeDue: t.Due, AfterStart: newStart, AfterDue: newDue, Reason: reason, Milestone: t.Milestone})
		payload, e := json.Marshal(RescheduleTask{TaskID: id, Version: t.Version, Start: newStart, Due: newDue})
		if e != nil {
			return "", e
		}
		p, e := create(ctx, tx, h, service, run, "reschedule_task", payload, []uuid.UUID{message, id})
		if e != nil {
			return "", e
		}
		result.ProposalIDs = append(result.ProposalIDs, p.ID)
	}
	assessmentID, e := uuid.NewV7()
	if e != nil {
		return "", e
	}
	encoded, e := json.Marshal(assessment)
	if e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'risk_assessment',$3)`, assessmentID, tx.WorkspaceID(), h.ID); e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.risk_assessments(id,workspace_id,version,algorithm_version,source_event_id,assessment,scenario_run_id) SELECT $1,$2,$3,$4,source_event_id,$5,id FROM app.agent_runs WHERE workspace_id=$2 AND id=$6`, assessmentID, tx.WorkspaceID(), loaded.Version, risk.AlgorithmVersion, encoded, run); e != nil {
		return "", e
	}
	linkID, e := uuid.NewV7()
	if e != nil {
		return "", e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.links(id,workspace_id,from_object,to_object,link_type) VALUES($1,$2,$3,$4,'belongs_to')`, linkID, tx.WorkspaceID(), assessmentID, task.ProjectID); e != nil {
		return "", e
	}
	if e = events.WriteAgentAudited(ctx, tx, service, assessmentID, run, "risk.scenario_created", events.Payload{ProjectID: task.ProjectID.String(), GraphVersion: loaded.Version, After: map[string]interface{}{"affected_count": len(result.Changes), "launch_flagged": result.LaunchFlagged}}); e != nil {
		return "", e
	}
	if e = saveResult(ctx, tx, run, result); e != nil {
		return "", e
	}
	summary := fmt.Sprintf("A %d-day delay affects %d task dates. Forecast launch slip: %.1f days. Review the impact and proposals; no task dates have changed.", d.Days, len(result.Changes), float64(assessment.LaunchSlipMinutes)/1440)
	return "completed", chat.PostSystem(ctx, tx, service, h, run, channel, summary, []uuid.UUID{target})
}
func (s *Store) GetRun(ctx context.Context, session auth.Session, id uuid.UUID) (Run, error) {
	out := Run{Result: emptyResult()}
	e := s.Auth.InTenant(ctx, session, policy.ProposalRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		var raw []byte
		e := tx.QueryRow(ctx, `SELECT id,status,kind,requested_by_user,result FROM app.agent_runs WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id).Scan(&out.ID, &out.Status, &out.Kind, &out.RequestedBy, &raw)
		if errors.Is(e, pgx.ErrNoRows) {
			return auth.ErrNotFound
		}
		if e != nil {
			return e
		}
		return json.Unmarshal(raw, &out.Result)
	})
	return out, e
}
func (s *Store) ConfirmDelay(ctx context.Context, session auth.Session, id, task uuid.UUID, days int) error {
	if days < 1 || days > 365 {
		return auth.ErrInvalid
	}
	actors, e := s.Auth.ConsumerSessions(ctx)
	if e != nil {
		return e
	}
	var service policy.Actor
	for _, a := range actors {
		if a.WorkspaceID == session.WorkspaceID.String() {
			service = a
		}
	}
	return s.Auth.InTenant(ctx, session, policy.AgentRequest, "", func(tx database.TenantTx, h policy.Actor) error {
		if e := s.Auth.AuthorizeConsumer(ctx, tx, service); e != nil {
			return e
		}
		if e := lockAudit(ctx, tx); e != nil {
			return e
		}
		var raw []byte
		var user uuid.UUID
		var status, kind string
		e := tx.QueryRow(ctx, `SELECT requested_by_user,status,kind,result FROM app.agent_runs WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, tx.WorkspaceID(), id).Scan(&user, &status, &kind, &raw)
		if errors.Is(e, pgx.ErrNoRows) {
			return auth.ErrNotFound
		}
		if e != nil {
			return e
		}
		if user != session.User.ID {
			return auth.ErrForbidden
		}
		if status != "needs_confirmation" || kind != "delay_detection" {
			return auth.ErrConflict
		}
		var result WorkflowResult
		if json.Unmarshal(raw, &result) != nil || result.Detection == nil {
			return auth.ErrInvalid
		}
		found := false
		for _, c := range result.Candidates {
			if c.ID == task {
				found = true
			}
		}
		if !found {
			return auth.ErrInvalid
		}
		if _, _, _, e := signalMessage(ctx, tx, id); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE app.agent_runs SET status='queued',finished_at=NULL,input=input || jsonb_build_object('confirmed_task_id',$3::text,'confirmed_days',$4::int) WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id, task.String(), days); e != nil {
			return e
		}
		return events.WriteAudited(ctx, tx, id, user, "delay.confirmed", events.Payload{Before: map[string]string{"status": status}, After: map[string]string{"status": "queued"}})
	})
}
func (s *Store) Impacts(ctx context.Context, session auth.Session, project uuid.UUID) ([]Run, error) {
	out := []Run{}
	e := s.Auth.InTenant(ctx, session, policy.RiskRead, "", func(tx database.TenantTx, h policy.Actor) error {
		if _, e := tasks.ProjectTx(ctx, tx, h, project); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT r.id,r.status,r.kind,r.requested_by_user,r.result FROM app.agent_runs r JOIN app.risk_assessments a ON a.workspace_id=r.workspace_id AND a.scenario_run_id=r.id JOIN app.links l ON l.workspace_id=a.workspace_id AND l.from_object=a.id AND l.link_type='belongs_to' WHERE r.workspace_id=$1 AND l.to_object=$2 ORDER BY r.id DESC LIMIT 20`, tx.WorkspaceID(), project)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			v := Run{Result: emptyResult()}
			var raw []byte
			if e := rows.Scan(&v.ID, &v.Status, &v.Kind, &v.RequestedBy, &raw); e != nil {
				return e
			}
			if e := json.Unmarshal(raw, &v.Result); e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, e
}

func confirmedInput(ctx context.Context, tx database.TenantTx, run uuid.UUID) (*uuid.UUID, int, error) {
	var id *uuid.UUID
	var days int
	e := tx.QueryRow(ctx, `SELECT (input->>'confirmed_task_id')::uuid,coalesce((input->>'confirmed_days')::int,0) FROM app.agent_runs WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), run).Scan(&id, &days)
	if e == nil && id != nil && (days < 1 || days > 365) {
		e = auth.ErrInvalid
	}
	return id, days, e
}
