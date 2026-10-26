package tasks

import (
	"context"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

func (s *Store) ListProjects(ctx context.Context, session auth.Session) ([]Project, error) {
	result := make([]Project, 0)
	err := s.Auth.InTenant(ctx, session, policy.ProjectRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		rows, err := tx.Query(ctx, projectSelect+` ORDER BY p.id`, tx.WorkspaceID())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanProject(rows)
			if err != nil {
				return err
			}
			result = append(result, p)
		}
		return rows.Err()
	})
	return result, err
}
func (s *Store) CreateProject(ctx context.Context, session auth.Session, input ProjectInput) (Project, error) {
	var result Project
	if !validProject(input) {
		return result, auth.ErrInvalid
	}
	id, err := uuid.NewV7()
	if err != nil {
		return result, err
	}
	err = s.Auth.InTenant(ctx, session, policy.ProjectWrite, "", func(tx database.TenantTx, actor policy.Actor) error {
		if err := object(ctx, tx, id, actor, "project"); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.projects(id,workspace_id,name,description,start_at,launch_due) VALUES($1,$2,$3,$4,$5,$6)`, id, tx.WorkspaceID(), input.Name, input.Description, normalize(input.Start), input.LaunchDue); err != nil {
			return err
		}
		var err error
		result, err = getProject(ctx, tx, id)
		if err != nil {
			return err
		}
		return emit(ctx, tx, actor, id, id, "project.created", 1, 1, nil, map[string]int64{"version": 1})
	})
	return result, err
}
func (s *Store) UpdateProject(ctx context.Context, session auth.Session, id uuid.UUID, version int64, input ProjectInput) (Project, error) {
	var result Project
	if !validProject(input) || version < 1 {
		return result, auth.ErrInvalid
	}
	err := s.Auth.InTenant(ctx, session, policy.ProjectWrite, "", func(tx database.TenantTx, actor policy.Actor) error {
		if err := projectLock(ctx, tx, id, false); err != nil {
			return err
		}
		old, err := getProject(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.Version != version {
			return auth.ErrConflict
		}
		tag, err := tx.Exec(ctx, `UPDATE app.projects SET name=$3,description=$4,start_at=$5,launch_due=$6,version=version+1,graph_version=graph_version+1 WHERE id=$1 AND workspace_id=$2 AND version=$7`, id, tx.WorkspaceID(), input.Name, input.Description, normalize(input.Start), input.LaunchDue, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrConflict
		}
		if err := touch(ctx, tx, id); err != nil {
			return err
		}
		result, err = getProject(ctx, tx, id)
		if err != nil {
			return err
		}
		return emit(ctx, tx, actor, id, id, "project.updated", result.Version, result.GraphVersion, map[string]int64{"version": old.Version}, map[string]int64{"version": result.Version})
	})
	return result, err
}
func (s *Store) DeleteProject(ctx context.Context, session auth.Session, id uuid.UUID, version int64) error {
	return s.Auth.InTenant(ctx, session, policy.ProjectWrite, "", func(tx database.TenantTx, actor policy.Actor) error {
		if err := projectLock(ctx, tx, id, false); err != nil {
			return err
		}
		old, err := getProject(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.Version != version {
			return auth.ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE app.projects SET version=version+1,graph_version=graph_version+1 WHERE id=$1 AND workspace_id=$2 AND version=$3`, id, tx.WorkspaceID(), version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE app.objects SET deleted_at=now(),updated_at=now() WHERE id=$1 AND workspace_id=$2 AND deleted_at IS NULL`, id, tx.WorkspaceID()); err != nil {
			return err
		}
		return emit(ctx, tx, actor, id, id, "project.deleted", version+1, old.GraphVersion+1, map[string]int64{"version": version}, map[string]bool{"deleted": true})
	})
}
func (s *Store) Board(ctx context.Context, session auth.Session, id uuid.UUID) (Board, error) {
	result := Board{Tasks: make([]Task, 0), Dependencies: make([]Dependency, 0)}
	err := s.Auth.InTenant(ctx, session, policy.ProjectRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		if err := projectLock(ctx, tx, id, true); err != nil {
			return err
		}
		var err error
		result.Project, err = getProject(ctx, tx, id)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, taskSelect+` AND parent.to_object=$2 ORDER BY t.id LIMIT 100001`, tx.WorkspaceID(), id)
		if err != nil {
			return err
		}
		for rows.Next() {
			task, err := scanTask(rows)
			if err != nil {
				rows.Close()
				return err
			}
			result.Tasks = append(result.Tasks, task)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(result.Tasks) > 100000 {
			return auth.ErrInvalid
		}
		rows, err = tx.Query(ctx, `SELECT l.from_object,l.to_object FROM app.links l JOIN app.links parent ON parent.workspace_id=l.workspace_id AND parent.from_object=l.from_object AND parent.link_type='belongs_to' JOIN app.objects a ON a.id=l.from_object AND a.workspace_id=l.workspace_id JOIN app.objects b ON b.id=l.to_object AND b.workspace_id=l.workspace_id WHERE l.workspace_id=$1 AND l.link_type='blocked_by' AND parent.to_object=$2 AND a.deleted_at IS NULL AND b.deleted_at IS NULL ORDER BY l.from_object,l.to_object`, tx.WorkspaceID(), id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var dependency Dependency
			if err := rows.Scan(&dependency.TaskID, &dependency.BlockerID); err != nil {
				return err
			}
			result.Dependencies = append(result.Dependencies, dependency)
		}
		return rows.Err()
	})
	return result, err
}
