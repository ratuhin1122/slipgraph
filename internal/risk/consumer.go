package risk

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

func Consume(ctx context.Context, tx database.TenantTx, actor policy.Actor, eventID uuid.UUID) error {
	if !policy.Can(ctx, actor, policy.RiskCompute, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return errors.New("not permitted")
	}
	var eventType string
	var data []byte
	if err := tx.QueryRow(ctx, `SELECT event_type,payload FROM app.events WHERE id=$1 AND workspace_id=$2`, eventID, tx.WorkspaceID()).Scan(&eventType, &data); err != nil {
		return err
	}
	switch eventType {
	case "project.created", "project.updated", "task.created", "task.updated", "task.deleted", "delay.reported", "dependency.added", "dependency.removed":
	default:
		return nil
	}
	var signal events.Payload
	if err := json.Unmarshal(data, &signal); err != nil {
		return errors.New("invalid risk event")
	}
	project, err := uuid.Parse(signal.ProjectID)
	if err != nil {
		return nil
	}
	loaded, err := Load(ctx, tx, actor, project, signal.ChangedTaskID, true)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// Coalesce superseded events. The audit consumer still records every change;
	// no historical scenario is falsely labeled as the current graph snapshot.
	if signal.GraphVersion != loaded.Version {
		return nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.risk_assessments r JOIN app.links l ON l.from_object=r.id AND l.workspace_id=r.workspace_id AND l.link_type='belongs_to' WHERE r.scenario_run_id IS NULL AND r.workspace_id=$1 AND l.to_object=$2 AND r.version=$3 AND r.algorithm_version=$4)`, tx.WorkspaceID(), project, loaded.Version, AlgorithmVersion).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	assessment, err := Analyze(loaded.Plan, nil)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(assessment)
	if err != nil {
		return err
	}
	assessmentID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'risk_assessment',$3)`, assessmentID, tx.WorkspaceID(), actor.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.risk_assessments(id,workspace_id,version,algorithm_version,source_event_id,assessment) VALUES($1,$2,$3,$4,$5,$6)`, assessmentID, tx.WorkspaceID(), loaded.Version, assessment.AlgorithmVersion, eventID, encoded); err != nil {
		return err
	}
	linkID, err := uuid.NewV7()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.links(id,workspace_id,from_object,to_object,link_type) VALUES($1,$2,$3,$4,'belongs_to')`, linkID, tx.WorkspaceID(), assessmentID, project); err != nil {
		return err
	}
	return events.WriteService(ctx, tx, actor, assessmentID, "risk.assessed", events.Payload{ProjectID: project.String(), GraphVersion: loaded.Version, Version: loaded.Version, After: map[string]string{"assessment_id": assessmentID.String(), "algorithm_version": assessment.AlgorithmVersion}})
}
