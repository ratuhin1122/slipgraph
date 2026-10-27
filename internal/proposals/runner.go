package proposals

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/llm"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Limits struct {
	DailyTokens, DailyCostMicroUSD, InputMicroUSD, OutputMicroUSD int64
	MaxTokens                                                     int
	Attempts                                                      int
	Timeout                                                       time.Duration
}

func DefaultLimits() Limits {
	return Limits{DailyTokens: 100000, DailyCostMicroUSD: 1000000, InputMicroUSD: 10, OutputMicroUSD: 30, MaxTokens: 2048, Attempts: 3, Timeout: 30 * time.Second}
}
func LimitsFromEnv() (Limits, error) {
	l := DefaultLimits()
	for key, target := range map[string]*int64{"LLM_DAILY_TOKENS": &l.DailyTokens, "LLM_DAILY_COST_MICROUSD": &l.DailyCostMicroUSD, "LLM_INPUT_MICROUSD_PER_TOKEN": &l.InputMicroUSD, "LLM_OUTPUT_MICROUSD_PER_TOKEN": &l.OutputMicroUSD} {
		if v := os.Getenv(key); v != "" {
			n, e := strconv.ParseInt(v, 10, 64)
			if e != nil || n < 1 || n > 1000000000 {
				return l, auth.ErrInvalid
			}
			*target = n
		}
	}
	if os.Getenv("LLM_PROVIDER") != "" && os.Getenv("LLM_PROVIDER") != "disabled" && (os.Getenv("LLM_INPUT_MICROUSD_PER_TOKEN") == "" || os.Getenv("LLM_OUTPUT_MICROUSD_PER_TOKEN") == "") {
		return l, auth.ErrInvalid
	}
	return l, nil
}

type Runner struct {
	Store    *Store
	Provider llm.Provider
	Limits   Limits
	Logger   *slog.Logger
}

// Enqueue is an internal worker integration seam, deliberately absent from HTTP.
// An authenticated human session binds scope and requester; sources cannot supply authority.
func (s *Store) Enqueue(ctx context.Context, session auth.Session, kind string, ids []uuid.UUID) (uuid.UUID, error) {
	var run uuid.UUID
	if _, e := Schema(kind); e != nil {
		return run, e
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			return run, auth.ErrInvalid
		}
		seen[id] = true
	}
	if err := s.Auth.Rate(ctx, "agent:request:"+session.WorkspaceID.String(), session.User.ID.String(), 10, time.Hour); err != nil {
		return run, err
	}
	err := s.Auth.InTenant(ctx, session, policy.AgentRequest, "", func(tx database.TenantTx, a policy.Actor) error {
		if err := lockAudit(ctx, tx); err != nil {
			return err
		}
		content, err := sourceContent(ctx, tx, a, ids)
		if err != nil {
			return err
		}
		if _, err := llm.Assemble(content); err != nil {
			return auth.ErrInvalid
		}
		run, err = uuid.NewV7()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'agent_run',$3)`, run, tx.WorkspaceID(), a.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.agent_runs(id,workspace_id,requested_by_user,kind,status) VALUES($1,$2,$3,$4,'queued')`, run, tx.WorkspaceID(), session.User.ID, kind); err != nil {
			return err
		}
		for _, source := range ids {
			if err := linkSource(ctx, tx, run, source); err != nil {
				return err
			}
		}
		return events.WriteAudited(ctx, tx, run, session.User.ID, "agent.queued", events.Payload{After: map[string]string{"status": "queued"}})
	})
	return run, err
}
func (r *Runner) Process(ctx context.Context, service policy.Actor, run uuid.UUID) error {
	l := r.Limits
	if r.Provider == nil {
		return llm.ErrUnavailable
	}
	if l.Attempts < 1 || l.Attempts > 3 || l.MaxTokens < 1 || l.MaxTokens > 4096 || l.DailyTokens < 1 || l.DailyTokens > 1000000000 || l.DailyCostMicroUSD < 1 || l.InputMicroUSD < 1 || l.OutputMicroUSD < 1 || l.InputMicroUSD > 1000000000 || l.OutputMicroUSD > 1000000000 || l.Timeout <= 0 || l.Timeout > time.Minute {
		return auth.ErrInvalid
	}
	if !policy.Can(ctx, service, policy.AgentRun, policy.Resource{WorkspaceID: service.WorkspaceID}) {
		return auth.ErrForbidden
	}
	var requester uuid.UUID
	var kind string
	var ids []uuid.UUID
	var request llm.Request
	err := r.Store.Auth.InConsumer(ctx, service, func(tx database.TenantTx) error {
		if err := lockAudit(ctx, tx); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, `SELECT requested_by_user,kind,status FROM app.agent_runs WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, tx.WorkspaceID(), run).Scan(&requester, &kind, &status); err != nil {
			return auth.ErrNotFound
		}
		if status != "queued" {
			return auth.ErrConflict
		}
		human, err := r.Store.Auth.JobMember(ctx, tx, requester, policy.AgentRequest)
		if err != nil {
			return err
		}
		ids, err = sources(ctx, tx, run)
		if err != nil {
			return err
		}
		content, err := sourceContent(ctx, tx, human, ids)
		if err != nil {
			return err
		}
		request.Prompt, err = llm.Assemble(content)
		if err != nil {
			return err
		}
		request.Schema, err = Schema(kind)
		if err != nil {
			return err
		}
		request.MaxTokens = l.MaxTokens
		_, err = tx.Exec(ctx, `UPDATE app.agent_runs SET status='running',started_at=clock_timestamp(),lease_until=clock_timestamp()+interval '5 minutes' WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), run)
		return err
	})
	if err != nil {
		return err
	}
	var response llm.Response
	var runErr error
	for attempt := 0; attempt < l.Attempts; attempt++ {
		// Byte-count plus schema and overhead is a conservative token reservation;
		// ambiguous timeouts keep their full reservation, never refund unknown usage.
		reserve := int64(len(request.Prompt.System) + len(request.Prompt.Data) + len(request.Schema) + 4096 + l.MaxTokens)
		reserveCost := reserve * max(l.InputMicroUSD, l.OutputMicroUSD)
		exhausted := false
		err = r.Store.Auth.InConsumer(ctx, service, func(tx database.TenantTx) error {
			if err := lockAudit(ctx, tx); err != nil {
				return err
			}
			if _, err := r.Store.Auth.JobMember(ctx, tx, requester, policy.AgentRequest); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9406))`, tx.WorkspaceID().String()); err != nil {
				return err
			}
			var used, cost int64
			if err := tx.QueryRow(ctx, `SELECT coalesce(sum(reserved_tokens),0),coalesce(sum(reserved_cost_microusd),0) FROM app.agent_runs WHERE workspace_id=$1 AND started_at>=clock_timestamp()-interval '24 hours'`, tx.WorkspaceID()).Scan(&used, &cost); err != nil {
				return err
			}
			if used+reserve > l.DailyTokens || cost+reserveCost > l.DailyCostMicroUSD {
				exhausted = true
				_, err := tx.Exec(ctx, `UPDATE app.agent_runs SET status='quota_exhausted',finished_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), run)
				return err
			}
			tag, err := tx.Exec(ctx, `UPDATE app.agent_runs SET steps=steps+1,reserved_tokens=reserved_tokens+$3,reserved_cost_microusd=reserved_cost_microusd+$4 WHERE workspace_id=$1 AND id=$2 AND status='running' AND steps<$5 AND lease_until>clock_timestamp()`, tx.WorkspaceID(), run, reserve, reserveCost, l.Attempts)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return auth.ErrConflict
			}
			return events.WriteAgentAudited(ctx, tx, service, run, run, "agent.step_reserved", events.Payload{Before: map[string]int{"steps": attempt}, After: map[string]interface{}{"steps": attempt + 1, "reserved_tokens": reserve, "reserved_cost_microusd": reserveCost}})
		})
		if err != nil {
			runErr = err
			break
		}
		if exhausted {
			runErr = auth.ErrLimited
			break
		}
		response, runErr = llm.Attempt(ctx, r.Provider, request, l.Timeout, r.Logger)
		input, output := response.InputTokens, response.OutputTokens
		if input < 0 || input > reserve || output < 0 || output > int64(l.MaxTokens) || input+output > reserve {
			runErr = llm.ErrInvalid
			input = 0
			output = 0
		}
		cost := input*l.InputMicroUSD + output*l.OutputMicroUSD
		if err := r.Store.Auth.InConsumer(ctx, service, func(tx database.TenantTx) error {
			_, err := tx.Exec(ctx, `UPDATE app.agent_runs SET tokens=tokens+$3,cost_microusd=cost_microusd+$4 WHERE workspace_id=$1 AND id=$2 AND status='running'`, tx.WorkspaceID(), run, input+output, cost)
			return err
		}); err != nil {
			runErr = err
			break
		}
		if r.Logger != nil {
			r.Logger.InfoContext(ctx, "model usage", "input_tokens", input, "output_tokens", output, "cost_microusd", cost, "workspace_id", service.WorkspaceID, "run_id", run.String())
		}
		var retry llm.Retryable
		if runErr == nil || !errors.As(runErr, &retry) {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			runErr = ctx.Err()
		case <-timer.C:
		}
		if ctx.Err() != nil {
			break
		}
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	finishErr := r.Store.Auth.InConsumer(finishCtx, service, func(tx database.TenantTx) error {
		if err := lockAudit(finishCtx, tx); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(finishCtx, `SELECT status FROM app.agent_runs WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, tx.WorkspaceID(), run).Scan(&status); err != nil {
			return err
		}
		if status != "running" && status != "quota_exhausted" {
			return auth.ErrConflict
		}
		if runErr == nil {
			human, e := r.Store.Auth.JobMember(finishCtx, tx, requester, policy.AgentRequest)
			if e != nil {
				runErr = e
			} else {
				_, _, e = Decode(kind, response.JSON)
				if e != nil {
					runErr = e
				} else {
					if _, e = tx.Exec(finishCtx, "SAVEPOINT proposal_creation"); e != nil {
						return e
					}
					_, e = create(finishCtx, tx, human, service, run, kind, response.JSON, ids)
					if e != nil {
						if _, rollback := tx.Exec(finishCtx, "ROLLBACK TO SAVEPOINT proposal_creation"); rollback != nil {
							return rollback
						}
						runErr = e
					} else {
						if _, e = tx.Exec(finishCtx, "RELEASE SAVEPOINT proposal_creation"); e != nil {
							return e
						}
					}
				}
			}
		}
		if status != "quota_exhausted" {
			status = "failed"
			if runErr == nil {
				status = "completed"
			}
		}
		if _, err := tx.Exec(finishCtx, `UPDATE app.agent_runs SET status=$3,finished_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), run, status); err != nil {
			return err
		}
		return events.WriteAgentAudited(finishCtx, tx, service, run, run, "agent.finished", events.Payload{After: map[string]string{"status": status}})
	})
	if finishErr != nil {
		return finishErr
	}
	return runErr
}

// Tick loads jobs only from authenticated service scopes. No queue payload selects a tenant.
func (r *Runner) Tick(ctx context.Context) error {
	actors, err := r.Store.Auth.ConsumerSessions(ctx)
	if err != nil {
		return err
	}
	for _, actor := range actors {
		var ids []uuid.UUID
		err := r.Store.Auth.InConsumer(ctx, actor, func(tx database.TenantTx) error {
			if !policy.Can(ctx, actor, policy.AgentRun, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
				return auth.ErrForbidden
			}
			if err := lockAudit(ctx, tx); err != nil {
				return err
			}
			// Expiry and crashed runs are terminal; unknown provider charges remain reserved.
			crashedRows, err := tx.Query(ctx, `UPDATE app.agent_runs SET status='failed',finished_at=clock_timestamp() WHERE workspace_id=$1 AND status='running' AND lease_until<clock_timestamp() RETURNING id`, tx.WorkspaceID())
			if err != nil {
				return err
			}
			var crashed []uuid.UUID
			for crashedRows.Next() {
				var id uuid.UUID
				if err := crashedRows.Scan(&id); err != nil {
					crashedRows.Close()
					return err
				}
				crashed = append(crashed, id)
			}
			err = crashedRows.Err()
			crashedRows.Close()
			if err != nil {
				return err
			}
			for _, id := range crashed {
				if err := events.WriteWorkerAudited(ctx, tx, actor, id, "agent.finished", events.Payload{Before: map[string]string{"status": "running"}, After: map[string]string{"status": "failed", "reason": "lease_expired"}}); err != nil {
					return err
				}
			}
			rows, err := tx.Query(ctx, `SELECT id FROM app.proposals WHERE workspace_id=$1 AND status IN ('pending','approved') AND expires_at<=clock_timestamp() ORDER BY id LIMIT 50 FOR UPDATE`, tx.WorkspaceID())
			if err != nil {
				return err
			}
			expired := []uuid.UUID{}
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return err
				}
				expired = append(expired, id)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			for _, id := range expired {
				p, err := get(ctx, tx, id, false)
				if err != nil {
					return err
				}
				old := p.Status
				p.Status = "expired"
				p.Version++
				if _, err := tx.Exec(ctx, `UPDATE app.proposals SET status='expired',version=$3 WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id, p.Version); err != nil {
					return err
				}
				if err := events.WriteWorkerAudited(ctx, tx, actor, p.ID, "proposal.expired", events.Payload{Version: p.Version, Before: map[string]string{"status": old}, After: map[string]string{"status": "expired"}}); err != nil {
					return err
				}
			}
			if r.Provider == nil {
				return nil
			}
			rows, err = tx.Query(ctx, `SELECT id FROM app.agent_runs WHERE workspace_id=$1 AND status='queued' ORDER BY id LIMIT 5`, tx.WorkspaceID())
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					return err
				}
				ids = append(ids, id)
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := r.Process(ctx, actor, id); err != nil && ctx.Err() == nil && r.Logger != nil {
				r.Logger.WarnContext(ctx, "consumer failed", "error", err)
			}
		}
	}
	return nil
}
func (r *Runner) Run(ctx context.Context) {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		if err := r.Tick(ctx); err != nil && r.Logger != nil && ctx.Err() == nil {
			r.Logger.ErrorContext(ctx, "consumer failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
