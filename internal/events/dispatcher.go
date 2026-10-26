package events

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"log/slog"
	"time"
)

// SessionSource is implemented only by auth; scopes come from authenticated
// service sessions, never from an inbound event's body or a Redis channel name.
type SessionSource interface {
	ConsumerSessions(context.Context) ([]policy.Actor, error)
	Prune(context.Context) error
	AuthorizeConsumer(context.Context, database.TenantTx, policy.Actor) error
}
type Consumer struct {
	Name   string
	Handle func(context.Context, database.TenantTx, policy.Actor, uuid.UUID) error
}
type Dispatcher struct {
	Pool      *pgxpool.Pool
	Sessions  SessionSource
	Consumers []Consumer
	Logger    *slog.Logger
}

func (d *Dispatcher) Dispatch(ctx context.Context) error {
	actors, err := d.Sessions.ConsumerSessions(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, actor := range actors {
		if !policy.Can(ctx, actor, policy.EventConsume, policy.Resource{WorkspaceID: actor.WorkspaceID}) {
			return errors.New("consumer authorization failed")
		}
		workspace, err := uuid.Parse(actor.WorkspaceID)
		if err != nil {
			return err
		}
		for batch := 0; batch < 10; batch++ {
			processed := false
			err := database.WithTenant(ctx, d.Pool, workspace, func(tx database.TenantTx) error {
				if err := d.Sessions.AuthorizeConsumer(ctx, tx, actor); err != nil {
					return err
				}
				rows, err := tx.Query(ctx, `SELECT id FROM app.events WHERE workspace_id=$1 AND published_at IS NULL ORDER BY created_at,id LIMIT 25 FOR UPDATE SKIP LOCKED`, workspace)
				if err != nil {
					return err
				}
				var ids []uuid.UUID
				for rows.Next() {
					var eventID uuid.UUID
					if err := rows.Scan(&eventID); err != nil {
						rows.Close()
						return err
					}
					ids = append(ids, eventID)
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					return err
				}
				processed = len(ids) > 0
				for _, eventID := range ids {
					for _, consumer := range d.Consumers {
						var received bool
						if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.delivery_receipts WHERE workspace_id=$1 AND event_id=$2 AND consumer=$3)`, workspace, eventID, consumer.Name).Scan(&received); err != nil {
							return err
						}
						if received {
							continue
						}
						if err := consumer.Handle(ctx, tx, actor, eventID); err != nil {
							return err
						}
						if _, err := tx.Exec(ctx, `INSERT INTO app.delivery_receipts(workspace_id,event_id,consumer) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, workspace, eventID, consumer.Name); err != nil {
							return err
						}
					}
					if _, err := tx.Exec(ctx, `UPDATE app.events SET published_at=now() WHERE id=$1 AND workspace_id=$2`, eventID, workspace); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				failures = append(failures, err)
				break
			}
			if !processed {
				break
			}
		}
	}
	return errors.Join(failures...)
}
func (d *Dispatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	cleanup := time.NewTicker(time.Minute)
	defer cleanup.Stop()
	for {
		if err := d.Dispatch(ctx); err != nil && ctx.Err() == nil {
			d.Logger.Error("consumer failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-cleanup.C:
			if err := d.Sessions.Prune(ctx); err != nil {
				d.Logger.Error("consumer failed", "error", err)
			}
		}
	}
}
