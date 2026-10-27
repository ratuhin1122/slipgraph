package tasks

import (
	"context"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"time"
)

func (s *Store) GetTask(ctx context.Context, session auth.Session, id uuid.UUID) (Task, error) {
	var result Task
	err := s.Auth.InTenant(ctx, session, policy.TaskRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		var err error
		result, err = getTask(ctx, tx, id)
		return err
	})
	return result, err
}
func owner(ctx context.Context, tx database.TenantTx, task uuid.UUID, target *uuid.UUID) error {
	if target != nil {
		var member uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT m.id FROM app.members m JOIN app.objects o ON o.workspace_id=m.workspace_id AND o.id=m.id WHERE m.id=$1 AND m.workspace_id=$2 AND o.deleted_at IS NULL`, *target, tx.WorkspaceID()).Scan(&member); err != nil {
			return auth.ErrNotFound
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app.links WHERE from_object=$1 AND workspace_id=$2 AND link_type='owned_by'`, task, tx.WorkspaceID()); err != nil {
		return err
	}
	if target != nil {
		return link(ctx, tx, task, *target, "owned_by")
	}
	return nil
}
func (s *Store) CreateTask(ctx context.Context, session auth.Session, project uuid.UUID, input TaskInput) (Task, error) {
	var result Task
	err := s.Auth.InTenant(ctx, session, policy.TaskWrite, "", func(tx database.TenantTx, actor policy.Actor) error {
		var err error
		result, err = CreateTx(ctx, tx, actor, project, input)
		return err
	})
	return result, err
}
func CreateTx(ctx context.Context, tx database.TenantTx, actor policy.Actor, project uuid.UUID, input TaskInput) (Task, error) {
	var result Task
	if !policy.Can(ctx, actor, policy.TaskWrite, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return result, auth.ErrForbidden
	}
	if !validTask(input) {
		return result, auth.ErrInvalid
	}
	id, err := uuid.NewV7()
	if err != nil {
		return result, err
	}
	errResult := func() error {
		if err := projectLock(ctx, tx, project, false); err != nil {
			return err
		}
		if _, err := getProject(ctx, tx, project); err != nil {
			return err
		}
		if err := object(ctx, tx, id, actor, "task"); err != nil {
			return err
		}
		var completed *time.Time
		if input.Status == "done" {
			value := normalize(time.Now())
			completed = &value
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.tasks(id,workspace_id,title,description,status,start_at,due_at,estimate_minutes,milestone,completed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, tx.WorkspaceID(), input.Title, input.Description, input.Status, normalize(input.Start), normalize(input.Due), input.EstimateMinutes, input.Milestone, completed); err != nil {
			return err
		}
		if err := link(ctx, tx, id, project, "belongs_to"); err != nil {
			return err
		}
		if err := owner(ctx, tx, id, input.OwnerID); err != nil {
			return err
		}
		graphVersion, err := graphChanged(ctx, tx, project)
		if err != nil {
			return err
		}
		result, err = getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		return emit(ctx, tx, actor, id, project, "task.created", 1, graphVersion, nil, snapshot(result))
	}()
	return result, errResult
}

func (s *Store) UpdateTask(ctx context.Context, session auth.Session, id uuid.UUID, version int64, input TaskInput) (Task, error) {
	var result Task
	err := s.Auth.InTenant(ctx, session, policy.TaskWrite, "", func(tx database.TenantTx, actor policy.Actor) error {
		var err error
		result, err = UpdateTx(ctx, tx, actor, id, version, input)
		return err
	})
	return result, err
}
func UpdateTx(ctx context.Context, tx database.TenantTx, actor policy.Actor, id uuid.UUID, version int64, input TaskInput) (Task, error) {
	var result Task
	if !policy.Can(ctx, actor, policy.TaskWrite, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return result, auth.ErrForbidden
	}
	if !validTask(input) || version < 1 {
		return result, auth.ErrInvalid
	}
	errResult := func() error {
		old, err := getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := projectLock(ctx, tx, old.ProjectID, false); err != nil {
			return err
		}
		old, err = getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.Version != version {
			return auth.ErrConflict
		}
		var completed *time.Time
		if input.Status == "done" {
			completed = old.CompletedAt
			if completed == nil {
				value := normalize(time.Now())
				completed = &value
			}
		}
		forecast := old.ForecastFinishAt
		delay := old.ReportedDelayDays
		if !input.Due.Equal(old.Due) {
			date := normalize(input.Due)
			forecast = &date
			delay = 0
		} else if !input.Start.Equal(old.Start) || input.EstimateMinutes != old.EstimateMinutes {
			forecast = nil
		}
		if input.Status == "done" || input.Status == "cancelled" {
			forecast = nil
			delay = 0
		}
		tag, err := tx.Exec(ctx, `UPDATE app.tasks SET title=$3,description=$4,status=$5,start_at=$6,due_at=$7,estimate_minutes=$8,milestone=$9,completed_at=$10,reported_delay_days=$11,forecast_finish_at=$12,version=version+1 WHERE id=$1 AND workspace_id=$2 AND version=$13`, id, tx.WorkspaceID(), input.Title, input.Description, input.Status, normalize(input.Start), normalize(input.Due), input.EstimateMinutes, input.Milestone, completed, delay, forecast, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrConflict
		}
		if err := owner(ctx, tx, id, input.OwnerID); err != nil {
			return err
		}
		if err := touch(ctx, tx, id); err != nil {
			return err
		}
		graphVersion, err := graphChanged(ctx, tx, old.ProjectID)
		if err != nil {
			return err
		}
		result, err = getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		return emit(ctx, tx, actor, id, old.ProjectID, "task.updated", result.Version, graphVersion, snapshot(old), snapshot(result))
	}()
	return result, errResult
}

func (s *Store) DeleteTask(ctx context.Context, session auth.Session, id uuid.UUID, version int64) error {
	return s.Auth.InTenant(ctx, session, policy.TaskWrite, "", func(tx database.TenantTx, actor policy.Actor) error {
		old, err := getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := projectLock(ctx, tx, old.ProjectID, false); err != nil {
			return err
		}
		old, err = getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.Version != version {
			return auth.ErrConflict
		}
		tag, err := tx.Exec(ctx, `UPDATE app.tasks SET version=version+1 WHERE id=$1 AND workspace_id=$2 AND version=$3`, id, tx.WorkspaceID(), version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE app.objects SET deleted_at=now(),updated_at=now() WHERE id=$1 AND workspace_id=$2 AND deleted_at IS NULL`, id, tx.WorkspaceID()); err != nil {
			return err
		}
		graphVersion, err := graphChanged(ctx, tx, old.ProjectID)
		if err != nil {
			return err
		}
		return emit(ctx, tx, actor, id, old.ProjectID, "task.deleted", version+1, graphVersion, snapshot(old), map[string]bool{"deleted": true})
	})
}
func (s *Store) ReportDelay(ctx context.Context, session auth.Session, id uuid.UUID, version int64, days int) (Task, error) {
	var result Task
	if days < 0 || days > 3650 || version < 1 {
		return result, auth.ErrInvalid
	}
	err := s.Auth.InTenant(ctx, session, policy.TaskWrite, "", func(tx database.TenantTx, actor policy.Actor) error {
		old, err := getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := projectLock(ctx, tx, old.ProjectID, false); err != nil {
			return err
		}
		old, err = getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.Version != version {
			return auth.ErrConflict
		}
		if old.Status == "done" || old.Status == "cancelled" {
			return auth.ErrInvalid
		}
		tag, err := tx.Exec(ctx, `UPDATE app.tasks SET reported_delay_days=$3,version=version+1 WHERE id=$1 AND workspace_id=$2 AND version=$4`, id, tx.WorkspaceID(), days, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrConflict
		}
		if err := touch(ctx, tx, id); err != nil {
			return err
		}
		graphVersion, err := graphChanged(ctx, tx, old.ProjectID)
		if err != nil {
			return err
		}
		result, err = getTask(ctx, tx, id)
		if err != nil {
			return err
		}
		actorID, err := uuid.Parse(actor.ID)
		if err != nil {
			return err
		}
		return events.Write(ctx, tx, id, actorID, "delay.reported", events.Payload{ProjectID: old.ProjectID.String(), ChangedTaskID: id.String(), DelayDays: days, Version: result.Version, GraphVersion: graphVersion, Before: snapshot(old), After: snapshot(result)})
	})
	return result, err
}
func (s *Store) AddDependency(ctx context.Context, session auth.Session, taskID, blockerID uuid.UUID, version int64) (Task, error) {
	return s.dependency(ctx, session, taskID, blockerID, version, true)
}
func (s *Store) RemoveDependency(ctx context.Context, session auth.Session, taskID, blockerID uuid.UUID, version int64) (Task, error) {
	return s.dependency(ctx, session, taskID, blockerID, version, false)
}
func (s *Store) dependency(ctx context.Context, session auth.Session, taskID, blockerID uuid.UUID, version int64, add bool) (Task, error) {
	var result Task
	err := s.Auth.InTenant(ctx, session, policy.TaskWrite, "", func(tx database.TenantTx, actor policy.Actor) error {
		var err error
		result, err = DependencyTx(ctx, tx, actor, taskID, blockerID, version, add)
		return err
	})
	return result, err
}
func DependencyTx(ctx context.Context, tx database.TenantTx, actor policy.Actor, taskID, blockerID uuid.UUID, version int64, add bool) (Task, error) {
	var result Task
	if !policy.Can(ctx, actor, policy.TaskWrite, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return result, auth.ErrForbidden
	}
	if version < 1 {
		return result, auth.ErrInvalid
	}
	errResult := func() error {
		task, err := getTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		if err := projectLock(ctx, tx, task.ProjectID, false); err != nil {
			return err
		}
		task, err = getTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		blocker, err := getTask(ctx, tx, blockerID)
		if err != nil {
			return err
		}
		if task.ProjectID != blocker.ProjectID {
			return auth.ErrNotFound
		}
		if task.Version != version {
			return auth.ErrConflict
		}
		event := "dependency.removed"
		if add {
			if task.Status == "cancelled" || blocker.Status == "cancelled" {
				return auth.ErrInvalid
			}
			// Include cancelled nodes in structural cycle checks: their retained
			// edges must remain acyclic if a task is later reopened.
			var cycle bool
			err := tx.QueryRow(ctx, `WITH RECURSIVE downstream(id) AS (
                SELECT $2::uuid
                UNION
                SELECT l.from_object FROM downstream d
                JOIN app.links l ON l.to_object=d.id AND l.workspace_id=$1 AND l.link_type='blocked_by'
                JOIN app.links parent ON parent.from_object=l.from_object AND parent.workspace_id=$1 AND parent.link_type='belongs_to'
                JOIN app.objects o ON o.id=l.from_object AND o.workspace_id=$1
                WHERE parent.to_object=$4 AND o.deleted_at IS NULL
            ) SELECT EXISTS(SELECT 1 FROM downstream WHERE id=$3)`, tx.WorkspaceID(), taskID, blockerID, task.ProjectID).Scan(&cycle)
			if err != nil {
				return err
			}
			if cycle {
				return auth.ErrConflict
			}
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.links WHERE workspace_id=$1 AND from_object=$2 AND to_object=$3 AND link_type='blocked_by')`, tx.WorkspaceID(), taskID, blockerID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return auth.ErrConflict
			}
			if err := link(ctx, tx, taskID, blockerID, "blocked_by"); err != nil {
				return err
			}
			event = "dependency.added"
		} else {
			tag, err := tx.Exec(ctx, `DELETE FROM app.links WHERE workspace_id=$1 AND from_object=$2 AND to_object=$3 AND link_type='blocked_by'`, tx.WorkspaceID(), taskID, blockerID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return auth.ErrNotFound
			}
		}
		tag, err := tx.Exec(ctx, `UPDATE app.tasks SET version=version+1 WHERE id=$1 AND workspace_id=$2 AND version=$3`, taskID, tx.WorkspaceID(), version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrConflict
		}
		if err := touch(ctx, tx, taskID); err != nil {
			return err
		}
		graphVersion, err := graphChanged(ctx, tx, task.ProjectID)
		if err != nil {
			return err
		}
		result, err = getTask(ctx, tx, taskID)
		if err != nil {
			return err
		}
		return emit(ctx, tx, actor, taskID, task.ProjectID, event, result.Version, graphVersion, nil, map[string]string{"task_id": taskID.String(), "blocker_id": blockerID.String()})
	}()
	return result, errResult
}

func ReadTx(ctx context.Context, tx database.TenantTx, actor policy.Actor, id uuid.UUID) (Task, error) {
	if !policy.Can(ctx, actor, policy.TaskRead, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return Task{}, auth.ErrForbidden
	}
	return getTask(ctx, tx, id)
}
func ProjectTx(ctx context.Context, tx database.TenantTx, actor policy.Actor, id uuid.UUID) (Project, error) {
	if !policy.Can(ctx, actor, policy.ProjectRead, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return Project{}, auth.ErrForbidden
	}
	return getProject(ctx, tx, id)
}
func ValidInput(input TaskInput) bool { return validTask(input) }
