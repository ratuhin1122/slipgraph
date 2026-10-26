package tasks

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"time"
)

type Project struct {
	ID           uuid.UUID  `json:"id"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Start        time.Time  `json:"start"`
	LaunchDue    *time.Time `json:"launch_due"`
	Version      int64      `json:"version"`
	GraphVersion int64      `json:"graph_version"`
}
type Task struct {
	ForecastFinishAt  *time.Time `json:"forecast_finish_at"`
	ID                uuid.UUID  `json:"id"`
	ProjectID         uuid.UUID  `json:"project_id"`
	Title             string     `json:"title"`
	Description       string     `json:"description"`
	Status            string     `json:"status"`
	OwnerID           *uuid.UUID `json:"owner_id"`
	Start             time.Time  `json:"start"`
	Due               time.Time  `json:"due"`
	EstimateMinutes   int64      `json:"estimate_minutes"`
	Milestone         bool       `json:"milestone"`
	Version           int64      `json:"version"`
	ReportedDelayDays int        `json:"reported_delay_days"`
	CompletedAt       *time.Time `json:"completed_at"`
}
type Dependency struct {
	TaskID    uuid.UUID `json:"task_id"`
	BlockerID uuid.UUID `json:"blocker_id"`
}
type Board struct {
	Project      Project      `json:"project"`
	Tasks        []Task       `json:"tasks"`
	Dependencies []Dependency `json:"dependencies"`
}
type ProjectInput struct {
	Name        string
	Description string
	Start       time.Time
	LaunchDue   *time.Time
}
type TaskInput struct {
	Title, Description, Status string
	OwnerID                    *uuid.UUID
	Start, Due                 time.Time
	EstimateMinutes            int64
	Milestone                  bool
}
type Store struct{ Auth *auth.Service }

func normalize(t time.Time) time.Time { return t.UTC().Truncate(time.Minute) }
func validDate(t time.Time) bool      { return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 9999 }
func validProject(v ProjectInput) bool {
	return len(v.Name) > 0 && len(v.Name) <= 200 && len(v.Description) <= 4000 && validDate(v.Start) && (v.LaunchDue == nil || (validDate(*v.LaunchDue) && !v.LaunchDue.Before(v.Start)))
}
func validTask(v TaskInput) bool {
	return len(v.Title) > 0 && len(v.Title) <= 200 && len(v.Description) <= 32000 && validDate(v.Start) && validDate(v.Due) && !v.Due.Before(v.Start) && v.EstimateMinutes >= 0 && v.EstimateMinutes <= 5256000 && (v.EstimateMinutes > 0 || v.Milestone) && (v.Status == "todo" || v.Status == "in_progress" || v.Status == "blocked" || v.Status == "done" || v.Status == "cancelled")
}
func projectLock(ctx context.Context, tx database.TenantTx, project uuid.UUID, shared bool) error {
	query := `SELECT pg_advisory_xact_lock(hashtextextended($1,9403))`
	if shared {
		query = `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,9403))`
	}
	_, err := tx.Exec(ctx, query, tx.WorkspaceID().String()+":"+project.String())
	return err
}

const projectSelect = `SELECT p.id,p.name,p.description,p.start_at,p.launch_due,p.version,p.graph_version FROM app.projects p JOIN app.objects o ON o.id=p.id AND o.workspace_id=p.workspace_id WHERE p.workspace_id=$1 AND o.deleted_at IS NULL`

func scanProject(row pgx.Row) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.Start, &p.LaunchDue, &p.Version, &p.GraphVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	return p, err
}
func getProject(ctx context.Context, tx database.TenantTx, id uuid.UUID) (Project, error) {
	return scanProject(tx.QueryRow(ctx, projectSelect+` AND p.id=$2`, tx.WorkspaceID(), id))
}

const taskSelect = `SELECT t.id,parent.to_object,t.title,t.description,t.status,owner.to_object,t.start_at,t.due_at,t.estimate_minutes,t.milestone,t.version,t.reported_delay_days,t.completed_at,t.forecast_finish_at FROM app.tasks t JOIN app.objects o ON o.id=t.id AND o.workspace_id=t.workspace_id JOIN app.links parent ON parent.workspace_id=t.workspace_id AND parent.from_object=t.id AND parent.link_type='belongs_to' JOIN app.projects p ON p.id=parent.to_object AND p.workspace_id=t.workspace_id JOIN app.objects po ON po.id=p.id AND po.workspace_id=p.workspace_id LEFT JOIN app.links owner ON owner.workspace_id=t.workspace_id AND owner.from_object=t.id AND owner.link_type='owned_by' WHERE t.workspace_id=$1 AND o.deleted_at IS NULL AND po.deleted_at IS NULL`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	err := row.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Description, &t.Status, &t.OwnerID, &t.Start, &t.Due, &t.EstimateMinutes, &t.Milestone, &t.Version, &t.ReportedDelayDays, &t.CompletedAt, &t.ForecastFinishAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	return t, err
}
func getTask(ctx context.Context, tx database.TenantTx, id uuid.UUID) (Task, error) {
	return scanTask(tx.QueryRow(ctx, taskSelect+` AND t.id=$2`, tx.WorkspaceID(), id))
}
func object(ctx context.Context, tx database.TenantTx, id uuid.UUID, actor policy.Actor, kind string) error {
	_, err := tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,$3,$4)`, id, tx.WorkspaceID(), kind, actor.ID)
	return err
}
func link(ctx context.Context, tx database.TenantTx, from, to uuid.UUID, kind string) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.links(id,workspace_id,from_object,to_object,link_type) VALUES($1,$2,$3,$4,$5)`, id, tx.WorkspaceID(), from, to, kind)
	return err
}
func touch(ctx context.Context, tx database.TenantTx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `UPDATE app.objects SET updated_at=now() WHERE id=$1 AND workspace_id=$2`, id, tx.WorkspaceID())
	return err
}
func graphChanged(ctx context.Context, tx database.TenantTx, project uuid.UUID) (int64, error) {
	var version int64
	err := tx.QueryRow(ctx, `UPDATE app.projects SET graph_version=graph_version+1 WHERE id=$1 AND workspace_id=$2 RETURNING graph_version`, project, tx.WorkspaceID()).Scan(&version)
	return version, err
}
func emit(ctx context.Context, tx database.TenantTx, actor policy.Actor, id, project uuid.UUID, event string, version, graphVersion int64, before, after interface{}) error {
	actorID, err := uuid.Parse(actor.ID)
	if err != nil {
		return err
	}
	return events.Write(ctx, tx, id, actorID, event, events.Payload{ProjectID: project.String(), Version: version, GraphVersion: graphVersion, Before: before, After: after})
}

// Snapshots deliberately omit free text to keep pasted credentials and AI content
// out of audit payloads. The scoped object itself retains the full user content.
func snapshot(t Task) map[string]interface{} {
	return map[string]interface{}{"status": t.Status, "owner_id": t.OwnerID, "start": t.Start, "due": t.Due, "estimate_minutes": t.EstimateMinutes, "milestone": t.Milestone, "reported_delay_days": t.ReportedDelayDays, "version": t.Version}
}
