package audit

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

type Result struct {
	Valid   bool   `json:"valid"`
	Entries int64  `json:"entries"`
	Head    string `json:"head"`
}

func Verify(ctx context.Context, tx database.TenantTx, actor policy.Actor) (Result, error) {
	var result Result
	if !policy.Can(ctx, actor, policy.AuditVerify, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return result, errors.New("not permitted")
	}
	err := tx.QueryRow(ctx, `SELECT valid,entries,head FROM app.verify_audit_chain($1)`, tx.WorkspaceID()).Scan(&result.Valid, &result.Entries, &result.Head)
	return result, err
}

// Consume is idempotent per source event. Hashes are calculated by the append
// trigger under a per-workspace advisory lock; the app role cannot read the ledger.
func Consume(ctx context.Context, tx database.TenantTx, actor policy.Actor, eventID uuid.UUID) error {
	if !policy.Can(ctx, actor, policy.AuditAppend, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return errors.New("not permitted")
	}
	auditID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.audit_log(id,workspace_id,sequence,event_id,actor_id,actor_kind,event_type,payload,created_at,previous_hash,entry_hash) SELECT $1,workspace_id,0,id,actor_id,actor_kind,event_type,payload,created_at,decode(repeat('00',32),'hex'),decode(repeat('00',32),'hex') FROM app.events WHERE id=$2 AND workspace_id=$3`, auditID, eventID, tx.WorkspaceID())
	return err
}
func Encode(result Result) ([]byte, error) { return json.Marshal(result) }
