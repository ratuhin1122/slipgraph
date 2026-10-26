package auth

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"time"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrForbidden       = errors.New("not permitted")
	ErrConflict        = errors.New("conflict")
	ErrInvalid         = errors.New("invalid request")
	ErrLimited         = errors.New("too many attempts")
	ErrNotFound        = errors.New("not found")
)

type User struct {
	ID          uuid.UUID `json:"id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
}
type Session struct {
	ID            uuid.UUID   `json:"id"`
	User          User        `json:"user"`
	WorkspaceID   uuid.UUID   `json:"workspace_id"`
	WorkspaceName string      `json:"workspace_name"`
	Role          policy.Role `json:"role"`
	ExpiresAt     time.Time   `json:"expires_at"`
}

func (s Session) Actor() policy.Actor {
	return policy.Actor{ID: s.User.ID.String(), WorkspaceID: s.WorkspaceID.String(), Role: s.Role}
}

type pair struct {
	Access  string
	Refresh string
	Session Session
}
type Workspace struct {
	ID          uuid.UUID   `json:"id"`
	Name        string      `json:"name"`
	Role        policy.Role `json:"role"`
	SwitchToken string      `json:"switch_token"`
}
type Member struct {
	ID          uuid.UUID   `json:"id"`
	UserID      uuid.UUID   `json:"user_id"`
	Role        policy.Role `json:"role"`
	DisplayName string      `json:"display_name"`
	Email       string      `json:"email"`
	Version     int64       `json:"version"`
}
type Service struct {
	pool  *pgxpool.Pool
	key   []byte
	now   func() time.Time
	slots chan struct{}
	dummy string
}

func New(pool *pgxpool.Pool, key []byte) (*Service, error) {
	if len(key) != 32 {
		return nil, errors.New("auth key must have 32 bytes")
	}
	dummy, err := HashPassword("dummy-password-for-timing-only")
	if err != nil {
		return nil, err
	}
	return &Service{pool: pool, key: append([]byte(nil), key...), now: func() time.Time { return time.Now().UTC() }, slots: make(chan struct{}, 2), dummy: dummy}, nil
}
func id() (uuid.UUID, error) { return uuid.NewV7() }
func (s *Service) identityTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		rollback, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = tx.Rollback(rollback)
	}()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// InTenant re-verifies the session's membership inside the transaction and holds
// a row lock until commit, so concurrent revocation cannot race a policy decision.
func (s *Service) InTenant(ctx context.Context, session Session, action policy.Action, owner string, fn func(database.TenantTx, policy.Actor) error) error {
	return database.WithTenant(ctx, s.pool, session.WorkspaceID, func(tx database.TenantTx) error {
		if action == policy.MemberInvite {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9402))`, session.WorkspaceID.String()); err != nil {
				return err
			}
		}
		var sessionID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM identity.sessions WHERE id=$1 AND user_id=$2 AND workspace_id=$3 AND kind='human' AND revoked_at IS NULL AND expires_at>$4 FOR UPDATE`, session.ID, session.User.ID, session.WorkspaceID, s.now()).Scan(&sessionID); err != nil {
			return ErrUnauthenticated
		}
		var role policy.Role
		if err := tx.QueryRow(ctx, `SELECT role FROM app.members WHERE workspace_id=$1 AND user_id=$2 FOR SHARE`, session.WorkspaceID, session.User.ID).Scan(&role); err != nil {
			return ErrUnauthenticated
		}
		actor := session.Actor()
		actor.Role = role
		if !policy.Can(ctx, actor, action, policy.Resource{WorkspaceID: actor.WorkspaceID, OwnerID: owner}) {
			return ErrForbidden
		}
		return fn(tx, actor)
	})
}
