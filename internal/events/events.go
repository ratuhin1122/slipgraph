package events

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"strings"
)

type Payload struct {
	GraphVersion  int64       `json:"graph_version,omitempty"`
	Before        interface{} `json:"before,omitempty"`
	After         interface{} `json:"after,omitempty"`
	ProjectID     string      `json:"project_id,omitempty"`
	Version       int64       `json:"version,omitempty"`
	ChangedTaskID string      `json:"changed_task_id,omitempty"`
	DelayDays     int         `json:"delay_days,omitempty"`
	PreviousDue   string      `json:"previous_due,omitempty"`
}

// Write must share the business mutation's TenantTx. Payloads are explicit DTOs,
// never raw requests, credentials, session records, invite/reset tokens or AI text.
func Write(ctx context.Context, tx database.TenantTx, object, actor uuid.UUID, eventType string, payload Payload) error {
	return write(ctx, tx, object, actor, "human", eventType, payload)
}
func WriteService(ctx context.Context, tx database.TenantTx, actor policy.Actor, object uuid.UUID, eventType string, payload Payload) error {
	if !policy.Can(ctx, actor, policy.RiskCompute, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return errors.New("service event denied")
	}
	actorID, err := uuid.Parse(actor.ID)
	if err != nil {
		return err
	}
	return write(ctx, tx, object, actorID, "service", eventType, payload)
}
func write(ctx context.Context, tx database.TenantTx, object, actor uuid.UUID, kind, eventType string, payload Payload) error {
	if object == uuid.Nil || actor == uuid.Nil {
		return errors.New("event subject and actor required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return errors.New("invalid event payload")
	}
	lower := strings.ToLower(string(data))
	for _, key := range []string{`"password"`, `"password_hash"`, `"token"`, `"token_hash"`, `"refresh_hash"`, `"secret"`, `"authorization"`} {
		if strings.Contains(lower, key) {
			return errors.New("secret field in event")
		}
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.events(id,workspace_id,object_id,actor_id,event_type,payload,version,actor_kind) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, tx.WorkspaceID(), object, actor, eventType, data, max(payload.Version, 1), kind)
	return err
}
