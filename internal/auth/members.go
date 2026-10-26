package auth

import (
	"context"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"time"
)

func (s *Service) Workspaces(ctx context.Context, session Session) ([]Workspace, error) {
	if !policy.Can(ctx, session.Actor(), policy.SessionRead, policy.Resource{WorkspaceID: session.WorkspaceID.String(), OwnerID: session.User.ID.String()}) {
		return nil, ErrForbidden
	}
	rows, err := s.pool.Query(ctx, `SELECT workspace_id FROM identity.memberships WHERE user_id=$1 ORDER BY created_at,id`, session.User.ID)
	if err != nil {
		return nil, err
	}
	var scopes []uuid.UUID
	for rows.Next() {
		var workspace uuid.UUID
		if err := rows.Scan(&workspace); err != nil {
			rows.Close()
			return nil, err
		}
		scopes = append(scopes, workspace)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := make([]Workspace, 0, len(scopes))
	for _, scope := range scopes {
		// Directory rows belong to this authenticated user. Resolve the target into
		// an expiring server-side switch session; the browser never submits a scope ID.
		var item Workspace
		item.ID = scope
		err := database.WithTenant(ctx, s.pool, scope, func(tx database.TenantTx) error {
			if err := tx.QueryRow(ctx, `SELECT w.name,m.role FROM app.workspaces w JOIN app.members m ON m.workspace_id=w.workspace_id WHERE w.workspace_id=$1 AND w.id=$1 AND m.user_id=$2`, scope, session.User.ID).Scan(&item.Name, &item.Role); err != nil {
				return err
			}
			ticket, err := randomToken()
			if err != nil {
				return err
			}
			ticketID, err := id()
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO identity.sessions(id,user_id,workspace_id,kind,token_hash,family_id,expires_at) VALUES($1,$2,$3,'switch',$4,$1,$5)`, ticketID, session.User.ID, scope, tokenHash(ticket), s.now().Add(2*time.Minute))
			item.SwitchToken = ticket
			return err
		})
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}
func (s *Service) Switch(ctx context.Context, session Session, ticket, oldToken string) (pair, error) {
	var result pair
	var scope uuid.UUID
	var ticketID uuid.UUID
	if !policy.Can(ctx, session.Actor(), policy.SessionWrite, policy.Resource{WorkspaceID: session.WorkspaceID.String(), OwnerID: session.User.ID.String()}) {
		return result, ErrForbidden
	}
	if len(ticket) != 43 {
		return result, ErrInvalid
	}
	if err := s.pool.QueryRow(ctx, `SELECT workspace_id,id FROM identity.sessions WHERE token_hash=$1 AND user_id=$2 AND kind='switch' AND expires_at>$3 AND revoked_at IS NULL`, tokenHash(ticket), session.User.ID, s.now()).Scan(&scope, &ticketID); err != nil {
		return result, ErrUnauthenticated
	}
	err := database.WithTenant(ctx, s.pool, scope, func(tx database.TenantTx) error {
		var role policy.Role
		if err := tx.QueryRow(ctx, `SELECT role FROM app.members WHERE user_id=$1 AND workspace_id=$2 FOR SHARE`, session.User.ID, scope).Scan(&role); err != nil {
			return ErrUnauthenticated
		}
		actor := policy.Actor{ID: session.User.ID.String(), WorkspaceID: scope.String(), Role: role}
		if !policy.Can(ctx, actor, policy.SessionWrite, policy.Resource{WorkspaceID: scope.String(), OwnerID: actor.ID}) {
			return ErrForbidden
		}
		var consumed uuid.UUID
		if err := tx.QueryRow(ctx, `UPDATE identity.sessions SET revoked_at=$3 WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>$3 RETURNING id`, ticketID, session.User.ID, s.now()).Scan(&consumed); err != nil {
			return ErrUnauthenticated
		}
		var err error
		result, err = s.issue(ctx, tx, session.User, role, oldToken, uuid.Nil, time.Time{})
		if err != nil {
			return err
		}
		return events.Write(ctx, tx, scope, session.User.ID, "auth.workspace_switched", events.Payload{After: map[string]string{"session_id": result.Session.ID.String()}})
	})
	return result, err
}
func (s *Service) Members(ctx context.Context, session Session) ([]Member, error) {
	result := make([]Member, 0)
	err := s.InTenant(ctx, session, policy.MemberRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		rows, err := tx.Query(ctx, `SELECT m.id,m.user_id,m.role,m.display_name,m.email,m.version FROM app.members m JOIN app.objects o ON o.workspace_id=m.workspace_id AND o.id=m.id WHERE m.workspace_id=$1 AND o.deleted_at IS NULL ORDER BY m.display_name,m.id`, session.WorkspaceID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m Member
			if err := rows.Scan(&m.ID, &m.UserID, &m.Role, &m.DisplayName, &m.Email, &m.Version); err != nil {
				return err
			}
			result = append(result, m)
		}
		return rows.Err()
	})
	return result, err
}
func (s *Service) ChangeRole(ctx context.Context, session Session, memberID uuid.UUID, role policy.Role, version int64) error {
	if role != policy.Admin && role != policy.Member && role != policy.Viewer {
		return ErrInvalid
	}
	return s.InTenant(ctx, session, policy.MemberInvite, "", func(tx database.TenantTx, actor policy.Actor) error {
		// This lock serializes admin changes and preserves at least one admin.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9402))`, session.WorkspaceID.String()); err != nil {
			return err
		}
		var target Member
		if err := tx.QueryRow(ctx, `SELECT id,user_id,role,version FROM app.members WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, memberID, session.WorkspaceID).Scan(&target.ID, &target.UserID, &target.Role, &target.Version); err != nil {
			return ErrNotFound
		}
		if !policy.Can(ctx, actor, policy.MemberRoleChange, policy.Resource{WorkspaceID: actor.WorkspaceID, OwnerID: target.UserID.String()}) {
			return ErrForbidden
		}
		if target.Version != version {
			return ErrConflict
		}
		if target.Role == policy.Admin && role != policy.Admin {
			var admins int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.members WHERE workspace_id=$1 AND role='admin'`, session.WorkspaceID).Scan(&admins); err != nil {
				return err
			}
			if admins <= 1 {
				return ErrConflict
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE app.members SET role=$3,version=version+1 WHERE id=$1 AND workspace_id=$2 AND version=$4`, memberID, session.WorkspaceID, role, version); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.memberships SET role=$3 WHERE user_id=$1 AND workspace_id=$2`, target.UserID, session.WorkspaceID, role); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE app.objects SET updated_at=now() WHERE id=$1 AND workspace_id=$2`, memberID, session.WorkspaceID); err != nil {
			return err
		}
		return events.Write(ctx, tx, memberID, session.User.ID, "member.role_changed", events.Payload{Before: map[string]string{"role": string(target.Role)}, After: map[string]string{"role": string(role)}, Version: version + 1})
	})
}
func (s *Service) Invite(ctx context.Context, session Session, email string, role policy.Role) (string, error) {
	email, err := emailAddress(email)
	if err != nil {
		return "", err
	}
	if role != policy.Member && role != policy.Viewer {
		return "", ErrInvalid
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	inviteID, err := id()
	if err != nil {
		return "", err
	}
	err = s.InTenant(ctx, session, policy.MemberInvite, "", func(tx database.TenantTx, _ policy.Actor) error {
		if _, err := tx.Exec(ctx, `INSERT INTO identity.invites(id,workspace_id,email,role,token_hash,created_by,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, inviteID, session.WorkspaceID, email, role, tokenHash(token), session.User.ID, s.now().Add(24*time.Hour)); err != nil {
			return err
		}
		return events.Write(ctx, tx, session.WorkspaceID, session.User.ID, "member.invited", events.Payload{After: map[string]string{"invite_id": inviteID.String(), "role": string(role)}})
	})
	return token, err
}
func (s *Service) AcceptInvite(ctx context.Context, session Session, token, oldToken string) (pair, error) {
	var result pair
	var scope uuid.UUID
	var role policy.Role
	var inviteID uuid.UUID
	if len(token) != 43 {
		return result, ErrInvalid
	}
	if !policy.Can(ctx, session.Actor(), policy.SessionWrite, policy.Resource{WorkspaceID: session.WorkspaceID.String(), OwnerID: session.User.ID.String()}) {
		return result, ErrForbidden
	}
	// Token lookup is restricted to the authenticated user's verified identity.
	if err := s.pool.QueryRow(ctx, `SELECT id,workspace_id,role FROM identity.invites WHERE token_hash=$1 AND email=$2 AND used_at IS NULL AND expires_at>$3`, tokenHash(token), session.User.Email, s.now()).Scan(&inviteID, &scope, &role); err != nil {
		return result, ErrNotFound
	}
	err := database.WithTenant(ctx, s.pool, scope, func(tx database.TenantTx) error {
		var consumed uuid.UUID
		if err := tx.QueryRow(ctx, `UPDATE identity.invites SET used_at=$4 WHERE id=$1 AND workspace_id=$2 AND email=$3 AND used_at IS NULL AND expires_at>$4 RETURNING id`, inviteID, scope, session.User.Email, s.now()).Scan(&consumed); err != nil {
			return ErrNotFound
		}
		// Possession of the one-use invitation and matching authenticated email create
		// a new verified membership, not an arbitrary request-selected scope.
		if err := s.addMember(ctx, tx, session.User, role); err != nil {
			return err
		}
		var err error
		result, err = s.issue(ctx, tx, session.User, role, oldToken, uuid.Nil, time.Time{})
		return err
	})
	return result, err
}
