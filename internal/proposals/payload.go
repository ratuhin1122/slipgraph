package proposals

import (
	"context"
	"embed"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/llm"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/tasks"
	"strings"
	"time"
)

//go:embed schemas/*.json
var schemas embed.FS

func Schema(kind string) (json.RawMessage, error) {
	switch kind {
	case "reschedule_task", "add_dependency", "create_task", "change_assignee", "create_plan", "delay_detection", "meeting_extraction", "meeting_task", "record_decision":
		b, e := schemas.ReadFile("schemas/" + kind + ".json")
		return b, e
	}
	return nil, auth.ErrInvalid
}

type Executor interface {
	Validate() bool
	Preview(context.Context, database.TenantTx, policy.Actor) (interface{}, interface{}, error)
	Execute(context.Context, database.TenantTx, policy.Actor) error
}
type RescheduleTask struct {
	TaskID  uuid.UUID `json:"task_id"`
	Version int64     `json:"version"`
	Start   time.Time `json:"start"`
	Due     time.Time `json:"due"`
}
type AddDependency struct {
	TaskID    uuid.UUID `json:"task_id"`
	Version   int64     `json:"version"`
	BlockerID uuid.UUID `json:"blocker_id"`
}
type ChangeAssignee struct {
	TaskID  uuid.UUID  `json:"task_id"`
	Version int64      `json:"version"`
	OwnerID *uuid.UUID `json:"owner_id"`
}
type CreateTask struct {
	ProjectID       uuid.UUID  `json:"project_id"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	OwnerID         *uuid.UUID `json:"owner_id"`
	Start           time.Time  `json:"start"`
	Due             time.Time  `json:"due"`
	EstimateMinutes int64      `json:"estimate_minutes"`
	Milestone       bool       `json:"milestone"`
}

func Decode(kind string, data []byte) (Executor, json.RawMessage, error) {
	schema, err := Schema(kind)
	if err != nil || llm.Validate(schema, data) != nil {
		return nil, nil, auth.ErrInvalid
	}
	var e Executor
	switch kind {
	case "meeting_task":
		e = &MeetingTask{}
	case "record_decision":
		e = &RecordDecision{}
	case "create_plan":
		e = &CreatePlan{}
	case "reschedule_task":
		e = &RescheduleTask{}
	case "add_dependency":
		e = &AddDependency{}
	case "create_task":
		e = &CreateTask{}
	case "change_assignee":
		e = &ChangeAssignee{}
	}
	if e == nil {
		return nil, nil, auth.ErrInvalid
	}
	if json.Unmarshal(data, e) != nil || !e.Validate() {
		return nil, nil, auth.ErrInvalid
	}
	canonical, err := json.Marshal(e)
	return e, canonical, err
}
func validDates(start, due time.Time) bool {
	return start.Year() >= 1970 && due.Year() <= 9999 && !due.Before(start) && start.Equal(start.Truncate(time.Minute)) && due.Equal(due.Truncate(time.Minute))
}
func (p RescheduleTask) Validate() bool {
	return p.TaskID != uuid.Nil && p.Version > 0 && validDates(p.Start, p.Due)
}
func (p AddDependency) Validate() bool {
	return p.TaskID != uuid.Nil && p.BlockerID != uuid.Nil && p.TaskID != p.BlockerID && p.Version > 0
}
func (p ChangeAssignee) Validate() bool {
	return p.TaskID != uuid.Nil && p.Version > 0 && (p.OwnerID == nil || *p.OwnerID != uuid.Nil)
}
func (p CreateTask) input() tasks.TaskInput {
	return tasks.TaskInput{Title: p.Title, Description: p.Description, Status: "todo", OwnerID: p.OwnerID, Start: p.Start, Due: p.Due, EstimateMinutes: p.EstimateMinutes, Milestone: p.Milestone}
}
func (p CreateTask) Validate() bool {
	return strings.TrimSpace(p.Title) != "" && !strings.ContainsRune(p.Title+p.Description, 0) && p.ProjectID != uuid.Nil && (p.OwnerID == nil || *p.OwnerID != uuid.Nil) && validDates(p.Start, p.Due) && tasks.ValidInput(p.input())
}
func input(t tasks.Task) tasks.TaskInput {
	return tasks.TaskInput{Title: t.Title, Description: t.Description, Status: t.Status, OwnerID: t.OwnerID, Start: t.Start, Due: t.Due, EstimateMinutes: t.EstimateMinutes, Milestone: t.Milestone}
}
func read(ctx context.Context, tx database.TenantTx, a policy.Actor, id uuid.UUID, version int64) (tasks.Task, error) {
	t, e := tasks.ReadTx(ctx, tx, a, id)
	if e == nil && t.Version != version {
		return t, auth.ErrConflict
	}
	return t, e
}
func member(ctx context.Context, tx database.TenantTx, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	var found uuid.UUID
	err := tx.QueryRow(ctx, `SELECT m.id FROM app.members m JOIN app.objects o ON o.workspace_id=m.workspace_id AND o.id=m.id WHERE m.workspace_id=$1 AND m.id=$2 AND o.deleted_at IS NULL`, tx.WorkspaceID(), id).Scan(&found)
	if err != nil {
		return auth.ErrNotFound
	}
	return nil
}
func (p RescheduleTask) Preview(ctx context.Context, tx database.TenantTx, a policy.Actor) (interface{}, interface{}, error) {
	t, e := read(ctx, tx, a, p.TaskID, p.Version)
	return map[string]interface{}{"task_id": t.ID, "version": t.Version, "start": t.Start, "due": t.Due}, p, e
}
func (p ChangeAssignee) Preview(ctx context.Context, tx database.TenantTx, a policy.Actor) (interface{}, interface{}, error) {
	t, e := read(ctx, tx, a, p.TaskID, p.Version)
	if e == nil {
		e = member(ctx, tx, p.OwnerID)
	}
	return map[string]interface{}{"task_id": t.ID, "version": t.Version, "owner_id": t.OwnerID}, p, e
}
func (p AddDependency) Preview(ctx context.Context, tx database.TenantTx, a policy.Actor) (interface{}, interface{}, error) {
	t, e := read(ctx, tx, a, p.TaskID, p.Version)
	if e != nil {
		return nil, nil, e
	}
	b, e := tasks.ReadTx(ctx, tx, a, p.BlockerID)
	if e != nil {
		return nil, nil, e
	}
	if t.ProjectID != b.ProjectID {
		return nil, nil, auth.ErrNotFound
	}
	return map[string]interface{}{"task_id": t.ID, "version": t.Version, "blocker_id": nil}, p, nil
}
func (p CreateTask) Preview(ctx context.Context, tx database.TenantTx, a policy.Actor) (interface{}, interface{}, error) {
	_, e := tasks.ProjectTx(ctx, tx, a, p.ProjectID)
	if e == nil {
		e = member(ctx, tx, p.OwnerID)
	}
	return map[string]interface{}{}, p, e
}
func (p RescheduleTask) Execute(ctx context.Context, tx database.TenantTx, a policy.Actor) error {
	t, e := read(ctx, tx, a, p.TaskID, p.Version)
	if e != nil {
		return e
	}
	v := input(t)
	v.Start = p.Start
	v.Due = p.Due
	_, e = tasks.UpdateTx(ctx, tx, a, p.TaskID, p.Version, v)
	return e
}
func (p ChangeAssignee) Execute(ctx context.Context, tx database.TenantTx, a policy.Actor) error {
	t, e := read(ctx, tx, a, p.TaskID, p.Version)
	if e != nil {
		return e
	}
	v := input(t)
	v.OwnerID = p.OwnerID
	_, e = tasks.UpdateTx(ctx, tx, a, p.TaskID, p.Version, v)
	return e
}
func (p AddDependency) Execute(ctx context.Context, tx database.TenantTx, a policy.Actor) error {
	_, e := tasks.DependencyTx(ctx, tx, a, p.TaskID, p.BlockerID, p.Version, true)
	return e
}
func (p CreateTask) Execute(ctx context.Context, tx database.TenantTx, a policy.Actor) error {
	_, e := tasks.CreateTx(ctx, tx, a, p.ProjectID, p.input())
	return e
}
