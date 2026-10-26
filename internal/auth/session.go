package auth

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"time"
)

func (s *Service) CSRF(token string) string {
	return base64.RawURLEncoding.EncodeToString(keyedHash(s.key, "csrf:"+token))
}
func (s *Service) CheckCSRF(token, header string) bool {
	return len(token) >= 40 && len(header) == 43 && subtle.ConstantTimeCompare([]byte(s.CSRF(token)), []byte(header)) == 1
}
func (s *Service) Anonymous(ctx context.Context, token string) (string, error) {
	if len(token) == 43 {
		var exists bool
		err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.sessions WHERE token_hash=$1 AND kind IN ('anonymous','human') AND expires_at>$2 AND revoked_at IS NULL)`, tokenHash(token), s.now()).Scan(&exists)
		if err != nil {
			return "", err
		}
		if exists {
			return token, nil
		}
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	sessionID, err := id()
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO identity.sessions(id,kind,token_hash,family_id,expires_at) VALUES($1,'anonymous',$2,$1,$3)`, sessionID, tokenHash(token), s.now().Add(30*time.Minute))
	return token, err
}
func (s *Service) Resolve(ctx context.Context, token string) (Session, error) {
	var result Session
	if len(token) != 43 {
		return result, ErrUnauthenticated
	}
	err := s.pool.QueryRow(ctx, `SELECT s.id,u.id,u.email,u.display_name,s.workspace_id,m.role,s.expires_at FROM identity.sessions s JOIN identity.users u ON u.id=s.user_id JOIN identity.memberships m ON m.user_id=s.user_id AND m.workspace_id=s.workspace_id WHERE s.token_hash=$1 AND s.kind='human' AND s.revoked_at IS NULL AND s.expires_at>$2`, tokenHash(token), s.now()).Scan(&result.ID, &result.User.ID, &result.User.Email, &result.User.DisplayName, &result.WorkspaceID, &result.Role, &result.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return result, ErrUnauthenticated
		}
		return result, err
	}
	err = s.InTenant(ctx, result, policy.SessionRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		return tx.QueryRow(ctx, `SELECT name FROM app.workspaces WHERE id=$1 AND workspace_id=$1`, result.WorkspaceID).Scan(&result.WorkspaceName)
	})
	return result, err
}
func (s *Service) issue(ctx context.Context, tx database.TenantTx, user User, role policy.Role, oldToken string, family uuid.UUID, refreshExpiry time.Time) (pair, error) {
	var result pair
	access, err := randomToken()
	if err != nil {
		return result, err
	}
	refresh, err := randomToken()
	if err != nil {
		return result, err
	}
	sessionID, err := id()
	if err != nil {
		return result, err
	}
	if family == uuid.Nil {
		family = sessionID
	}
	if refreshExpiry.IsZero() {
		refreshExpiry = s.now().Add(7 * 24 * time.Hour)
	}
	_, err = tx.Exec(ctx, `INSERT INTO identity.sessions(id,user_id,workspace_id,kind,token_hash,refresh_hash,family_id,expires_at,refresh_expires_at) VALUES($1,$2,$3,'human',$4,$5,$6,$7,$8)`, sessionID, user.ID, tx.WorkspaceID(), tokenHash(access), tokenHash(refresh), family, s.now().Add(15*time.Minute), refreshExpiry)
	if err != nil {
		return result, err
	}
	if oldToken != "" {
		_, err = tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$2 WHERE token_hash=$1 AND revoked_at IS NULL`, tokenHash(oldToken), s.now())
		if err != nil {
			return result, err
		}
	}
	result = pair{Access: access, Refresh: refresh, Session: Session{ID: sessionID, User: user, WorkspaceID: tx.WorkspaceID(), Role: role, ExpiresAt: s.now().Add(15 * time.Minute)}}
	err = tx.QueryRow(ctx, `SELECT name FROM app.workspaces WHERE id=$1 AND workspace_id=$1`, tx.WorkspaceID()).Scan(&result.Session.WorkspaceName)
	return result, err
}
func (s *Service) Logout(ctx context.Context, session Session) error {
	return s.InTenant(ctx, session, policy.SessionWrite, "", func(tx database.TenantTx, _ policy.Actor) error {
		_, err := tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$3 WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, session.ID, session.User.ID, s.now())
		if err != nil {
			return err
		}
		return events.Write(ctx, tx, session.WorkspaceID, session.User.ID, "auth.logout", events.Payload{After: map[string]string{"session_id": session.ID.String()}})
	})
}
func (s *Service) Refresh(ctx context.Context, access, refresh, csrf string) (pair, error) {
	var result pair
	if len(refresh) != 43 || !s.CheckCSRF(access, csrf) {
		return result, ErrForbidden
	}
	// The refresh credential resolves the server-side scope; no tenant ID is accepted.
	var sessionID, userID, workspace, family uuid.UUID
	var expiry time.Time
	var revoked *time.Time
	err := s.pool.QueryRow(ctx, `SELECT id,user_id,workspace_id,family_id,refresh_expires_at,revoked_at FROM identity.sessions WHERE refresh_hash=$1 AND kind='human'`, tokenHash(refresh)).Scan(&sessionID, &userID, &workspace, &family, &expiry, &revoked)
	if err != nil {
		return result, ErrUnauthenticated
	}
	if revoked != nil {
		_, err := s.pool.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$3 WHERE user_id=$1 AND family_id=$2 AND revoked_at IS NULL`, userID, family, s.now())
		if err != nil {
			return result, err
		}
		return result, ErrUnauthenticated
	}
	if !expiry.After(s.now()) {
		return result, ErrUnauthenticated
	}
	err = database.WithTenant(ctx, s.pool, workspace, func(tx database.TenantTx) error {
		var role policy.Role
		var user User
		user.ID = userID
		if err := tx.QueryRow(ctx, `SELECT role FROM app.members WHERE user_id=$1 AND workspace_id=$2 FOR SHARE`, userID, workspace).Scan(&role); err != nil {
			return ErrUnauthenticated
		}
		if !policy.Can(ctx, policy.Actor{ID: userID.String(), WorkspaceID: workspace.String(), Role: role}, policy.SessionWrite, policy.Resource{WorkspaceID: workspace.String(), OwnerID: userID.String()}) {
			return ErrForbidden
		}
		var changed uuid.UUID
		if err := tx.QueryRow(ctx, `UPDATE identity.sessions SET revoked_at=$3 WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL AND refresh_expires_at>$3 RETURNING id`, sessionID, userID, s.now()).Scan(&changed); err != nil {
			return ErrUnauthenticated
		}
		if err := tx.QueryRow(ctx, `SELECT email,display_name FROM identity.users WHERE id=$1`, userID).Scan(&user.Email, &user.DisplayName); err != nil {
			return err
		}
		var err error
		result, err = s.issue(ctx, tx, user, role, access, family, expiry)
		if err != nil {
			return err
		}
		return events.Write(ctx, tx, workspace, userID, "auth.refreshed", events.Payload{After: map[string]string{"session_id": result.Session.ID.String()}})
	})
	return result, err
}
