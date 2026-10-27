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
	return write(ctx, tx, object, actor, "human", eventType, payload, false)
}
func WriteService(ctx context.Context, tx database.TenantTx, actor policy.Actor, object uuid.UUID, eventType string, payload Payload) error {
	if !policy.Can(ctx, actor, policy.RiskCompute, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return errors.New("service event denied")
	}
	actorID, err := uuid.Parse(actor.ID)
	if err != nil {
		return err
	}
	return write(ctx, tx, object, actorID, "service", eventType, payload, false)
}
func write(ctx context.Context, tx database.TenantTx, object, actor uuid.UUID, kind, eventType string, payload Payload, audited bool) error {
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

	if err != nil || !audited {
		return err
	}
	auditID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.audit_log(id,workspace_id,sequence,event_id,actor_id,actor_kind,event_type,payload,created_at,previous_hash,entry_hash) SELECT $1,workspace_id,0,id,actor_id,actor_kind,event_type,payload,created_at,decode(repeat('00',32),'hex'),decode(repeat('00',32),'hex') FROM app.events WHERE workspace_id=$2 AND id=$3`, auditID, tx.WorkspaceID(), id)
	return err
}

// WriteAudited commits the exact event and its audit row with the mutation.
func WriteAudited(ctx context.Context, tx database.TenantTx, object, actor uuid.UUID, eventType string, payload Payload) error {
	return write(ctx, tx, object, actor, "human", eventType, payload, true)
}

func WriteAgentAudited(ctx context.Context, tx database.TenantTx, service policy.Actor, object, run uuid.UUID, eventType string, payload Payload) error {
	if !policy.Can(ctx, service, policy.AgentRun, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return errors.New("agent event denied")
	}
	return write(ctx, tx, object, run, "agent", eventType, payload, true)
}
func WriteWorkerAudited(ctx context.Context, tx database.TenantTx, service policy.Actor, object uuid.UUID, eventType string, payload Payload) error {
	if !policy.Can(ctx, service, policy.EventConsume, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return errors.New("worker event denied")
	}
	actor, err := uuid.Parse(service.ID)
	if err != nil {
		return err
	}
	return write(ctx, tx, object, actor, "service", eventType, payload, true)
}
