package auth

import (
	"context"
	"crypto/subtle"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"time"
)

type SessionInfo struct {
	ID          uuid.UUID `json:"id"`
	WorkspaceID uuid.UUID `json:"workspace_id"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Revoked     bool      `json:"revoked"`
}

func (s *Service) Sessions(ctx context.Context, session Session) ([]SessionInfo, error) {
	if !policy.Can(ctx, session.Actor(), policy.SessionRead, policy.Resource{WorkspaceID: session.WorkspaceID.String(), OwnerID: session.User.ID.String()}) {
		return nil, ErrForbidden
	}
	rows, err := s.pool.Query(ctx, `SELECT id,workspace_id,created_at,expires_at,revoked_at IS NOT NULL FROM identity.sessions WHERE user_id=$1 AND kind='human' ORDER BY created_at DESC LIMIT 100`, session.User.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]SessionInfo, 0)
	for rows.Next() {
		var item SessionInfo
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.CreatedAt, &item.ExpiresAt, &item.Revoked); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

// ConsumerSessions authenticates only user-less background service sessions.
// It never reads or returns another user's sessions or membership directory.
func (s *Service) ConsumerSessions(ctx context.Context) ([]policy.Actor, error) {
	rows, err := s.pool.Query(ctx, `SELECT id,workspace_id,token_hash,revoked_at IS NOT NULL FROM identity.sessions WHERE kind='consumer' AND user_id IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	result := make([]policy.Actor, 0)
	var ids []uuid.UUID
	for rows.Next() {
		var sessionID, workspace uuid.UUID
		var hash []byte
		var revoked bool
		if err := rows.Scan(&sessionID, &workspace, &hash, &revoked); err != nil {
			rows.Close()
			return nil, err
		}
		if revoked || subtle.ConstantTimeCompare(hash, keyedHash(s.key, "consumer:"+workspace.String())) != 1 {
			continue
		}
		ids = append(ids, sessionID)
		result = append(result, policy.Actor{ID: sessionID.String(), WorkspaceID: workspace.String(), Role: policy.Consumer})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	// Renewal requires possession of the service credential, not an untrusted job.
	for _, sessionID := range ids {
		if _, err := s.pool.Exec(ctx, `UPDATE identity.sessions SET expires_at=$2 WHERE id=$1 AND kind='consumer' AND revoked_at IS NULL`, sessionID, s.now().Add(24*time.Hour)); err != nil {
			return nil, err
		}
	}
	return result, nil
}
func (s *Service) Prune(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM identity.sessions WHERE id IN (SELECT id FROM identity.sessions WHERE kind<>'consumer' AND expires_at<$1 AND (refresh_expires_at IS NULL OR refresh_expires_at<$1) ORDER BY expires_at LIMIT 500)`, s.now().Add(-7*24*time.Hour))
	return err
}

// Reset issuance is an internal delivery boundary. It is deliberately not a
// public "return a reset token" route: callers must authenticate the delivery channel.
func (s *Service) IssueReset(ctx context.Context, user User) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	resetID, err := id()
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO identity.password_resets(id,user_id,token_hash,expires_at) VALUES($1,$2,$3,$4)`, resetID, user.ID, tokenHash(token), s.now().Add(15*time.Minute))
	return token, err
}
func (s *Service) ResetPassword(ctx context.Context, session Session, token, password string) error {
	if len(token) != 43 || !validPassword(password) {
		return ErrInvalid
	}
	if !s.acquire() {
		return ErrLimited
	}
	hash, err := HashPassword(password)
	s.release()
	if err != nil {
		return err
	}
	return s.InTenant(ctx, session, policy.SessionWrite, session.User.ID.String(), func(tx database.TenantTx, _ policy.Actor) error {
		var resetID uuid.UUID
		if err := tx.QueryRow(ctx, `UPDATE identity.password_resets SET used_at=$4 WHERE token_hash=$1 AND user_id=$2 AND used_at IS NULL AND expires_at>$3 RETURNING id`, tokenHash(token), session.User.ID, s.now(), s.now()).Scan(&resetID); err != nil {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.credentials SET password_hash=$2,failed_attempts=0,locked_until=NULL,updated_at=$3 WHERE user_id=$1`, session.User.ID, hash, s.now()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE identity.sessions SET revoked_at=$2 WHERE user_id=$1 AND revoked_at IS NULL AND kind='human'`, session.User.ID, s.now()); err != nil {
			return err
		}
		return events.Write(ctx, tx, session.WorkspaceID, session.User.ID, "auth.password_changed", events.Payload{After: map[string]string{"user_id": session.User.ID.String()}})
	})
}

func (s *Service) AuthorizeConsumer(ctx context.Context, tx database.TenantTx, actor policy.Actor) error {
	if !policy.Can(ctx, actor, policy.EventConsume, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return ErrForbidden
	}
	sessionID, err := uuid.Parse(actor.ID)
	if err != nil {
		return ErrForbidden
	}
	var hash []byte
	err = tx.QueryRow(ctx, `SELECT token_hash FROM identity.sessions WHERE id=$1 AND workspace_id=$2 AND kind='consumer' AND user_id IS NULL AND revoked_at IS NULL AND expires_at>$3 FOR SHARE`, sessionID, tx.WorkspaceID(), s.now()).Scan(&hash)
	if err != nil || subtle.ConstantTimeCompare(hash, keyedHash(s.key, "consumer:"+tx.WorkspaceID().String())) != 1 {
		return ErrForbidden
	}
	return nil
}
