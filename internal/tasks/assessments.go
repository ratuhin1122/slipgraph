package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/risk"
	"time"
)

type RiskEnvelope struct {
	Status       string           `json:"status"`
	Version      int64            `json:"version"`
	GraphVersion int64            `json:"graph_version"`
	Stale        bool             `json:"stale"`
	AssessedAt   *time.Time       `json:"assessed_at"`
	Assessment   *risk.Assessment `json:"assessment"`
}

func (s *Store) Risk(ctx context.Context, session auth.Session, project uuid.UUID) (RiskEnvelope, error) {
	result := RiskEnvelope{Status: "pending", Stale: true}
	err := s.Auth.InTenant(ctx, session, policy.RiskRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		p, err := getProject(ctx, tx, project)
		if err != nil {
			return err
		}
		result.GraphVersion = p.GraphVersion
		var data []byte
		var at time.Time
		err = tx.QueryRow(ctx, `SELECT r.version,r.assessed_at,r.assessment FROM app.risk_assessments r JOIN app.links l ON l.from_object=r.id AND l.workspace_id=r.workspace_id AND l.link_type='belongs_to' WHERE r.workspace_id=$1 AND l.to_object=$2 ORDER BY r.version DESC,r.assessed_at DESC,r.id DESC LIMIT 1`, tx.WorkspaceID(), project).Scan(&result.Version, &at, &data)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var assessment risk.Assessment
		if err := json.Unmarshal(data, &assessment); err != nil {
			return err
		}
		result.Status = "ready"
		result.Stale = result.Version != p.GraphVersion
		result.AssessedAt = &at
		result.Assessment = &assessment
		return nil
	})
	return result, err
}
