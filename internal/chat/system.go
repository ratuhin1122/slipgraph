package chat

import (
	"context"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

// PostSystem creates a clearly identified, immutable worker notice, not a human message.
func PostSystem(ctx context.Context, tx database.TenantTx, service, human policy.Actor, run, channelID uuid.UUID, body string, taskIDs []uuid.UUID) error {
	if !policy.Can(ctx, service, policy.AgentRun, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) || !policy.Can(ctx, human, policy.MessagePost, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return auth.ErrForbidden
	}
	if _, err := channel(ctx, tx, channelID); err != nil {
		return err
	}
	if !validBody(body, nil) {
		return auth.ErrInvalid
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'message',$3)`, id, tx.WorkspaceID(), human.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO app.messages(id,workspace_id,body,client_id,system_run_id) VALUES($1,$2,$3,$1,$4)`, id, tx.WorkspaceID(), body, run); err != nil {
		return err
	}
	if err := addLink(ctx, tx, id, channelID, "belongs_to"); err != nil {
		return err
	}
	for _, task := range taskIDs {
		if err := addLink(ctx, tx, id, task, "references"); err != nil {
			return err
		}
	}
	return events.WriteAgentAudited(ctx, tx, service, id, run, "message.created", events.Payload{Version: 1, After: map[string]interface{}{"channel_id": channelID, "system": true}})
}
