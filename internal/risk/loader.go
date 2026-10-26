package risk

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

var ErrNotFound = errors.New("project unavailable")

type Loaded struct {
	Plan       Plan
	Version    int64
	Downstream []string
}

// Load obtains a coherent graph snapshot under a shared project lock. Callers
// already holding the exclusive mutation lock pass acquire=false. All predicates
// include the trusted TenantTx scope; recursive closure never crosses projects.
func Load(ctx context.Context, tx database.TenantTx, actor policy.Actor, project uuid.UUID, changed string, acquire bool) (Loaded, error) {
	var result Loaded
	resource := policy.Resource{WorkspaceID: tx.WorkspaceID().String()}
	if !policy.Can(ctx, actor, policy.TaskRead, resource) && !policy.Can(ctx, actor, policy.RiskCompute, resource) {
		return result, errors.New("not permitted")
	}
	if acquire {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock_shared(hashtextextended($1,9403))`, tx.WorkspaceID().String()+":"+project.String()); err != nil {
			return result, err
		}
	}
	err := tx.QueryRow(ctx, `SELECT p.start_at,p.launch_due,p.graph_version FROM app.projects p JOIN app.objects o ON o.id=p.id AND o.workspace_id=p.workspace_id WHERE p.id=$1 AND p.workspace_id=$2 AND o.deleted_at IS NULL`, project, tx.WorkspaceID()).Scan(&result.Plan.Start, &result.Plan.LaunchDue, &result.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, ErrNotFound
	}
	if err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, `SELECT t.id::text,t.start_at,t.due_at,t.estimate_minutes,t.milestone,t.reported_delay_days,t.completed_at,t.forecast_finish_at FROM app.tasks t JOIN app.objects o ON o.id=t.id AND o.workspace_id=t.workspace_id JOIN app.links parent ON parent.workspace_id=t.workspace_id AND parent.from_object=t.id AND parent.link_type='belongs_to' WHERE t.workspace_id=$1 AND parent.to_object=$2 AND o.deleted_at IS NULL AND t.status<>'cancelled' ORDER BY t.id LIMIT 100001`, tx.WorkspaceID(), project)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var task Task
		if err := rows.Scan(&task.ID, &task.Start, &task.Due, &task.EstimateMinutes, &task.Milestone, &task.ReportedDelayDays, &task.CompletedAt, &task.FinishNotBefore); err != nil {
			rows.Close()
			return result, err
		}
		result.Plan.Tasks = append(result.Plan.Tasks, task)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Plan.Tasks) > 100000 {
		return result, ErrInvalidGraph
	}
	rows, err = tx.Query(ctx, `SELECT l.from_object::text,l.to_object::text FROM app.links l JOIN app.links parent ON parent.from_object=l.from_object AND parent.workspace_id=l.workspace_id AND parent.link_type='belongs_to' JOIN app.tasks a ON a.id=l.from_object AND a.workspace_id=l.workspace_id JOIN app.tasks b ON b.id=l.to_object AND b.workspace_id=l.workspace_id JOIN app.objects ao ON ao.id=a.id AND ao.workspace_id=a.workspace_id JOIN app.objects bo ON bo.id=b.id AND bo.workspace_id=b.workspace_id WHERE l.workspace_id=$1 AND parent.to_object=$2 AND l.link_type='blocked_by' AND ao.deleted_at IS NULL AND bo.deleted_at IS NULL AND a.status<>'cancelled' AND b.status<>'cancelled' ORDER BY l.from_object,l.to_object LIMIT 1000001`, tx.WorkspaceID(), project)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var dependency Dependency
		if err := rows.Scan(&dependency.TaskID, &dependency.BlockerID); err != nil {
			rows.Close()
			return result, err
		}
		result.Plan.Dependencies = append(result.Plan.Dependencies, dependency)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(result.Plan.Dependencies) > 1000000 {
		return result, ErrInvalidGraph
	}
	var changedID *uuid.UUID
	if changed != "" {
		parsed, err := uuid.Parse(changed)
		if err != nil {
			return result, ErrInvalidGraph
		}
		changedID = &parsed
	}
	rows, err = tx.Query(ctx, `WITH RECURSIVE downstream(id) AS (
 SELECT t.id FROM app.tasks t JOIN app.links parent ON parent.workspace_id=t.workspace_id AND parent.from_object=t.id AND parent.link_type='belongs_to' JOIN app.objects o ON o.id=t.id AND o.workspace_id=t.workspace_id WHERE t.workspace_id=$1 AND parent.to_object=$2 AND o.deleted_at IS NULL AND ($3::uuid IS NULL OR t.id=$3)
 UNION
 SELECT l.from_object FROM downstream d JOIN app.links l ON l.to_object=d.id AND l.workspace_id=$1 AND l.link_type='blocked_by' JOIN app.links parent ON parent.from_object=l.from_object AND parent.workspace_id=$1 AND parent.link_type='belongs_to' JOIN app.objects o ON o.id=l.from_object AND o.workspace_id=$1 WHERE parent.to_object=$2 AND o.deleted_at IS NULL
 ) SELECT id::text FROM downstream ORDER BY id`, tx.WorkspaceID(), project, changedID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	result.Downstream = make([]string, 0)
	for rows.Next() {
		var taskID string
		if err := rows.Scan(&taskID); err != nil {
			return result, err
		}
		result.Downstream = append(result.Downstream, taskID)
	}
	return result, rows.Err()
}
