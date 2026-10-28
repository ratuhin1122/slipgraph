package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

var closesIssue = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+#([1-9][0-9]{0,9})\b`)
var taskIdentifier = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func item(ctx context.Context, tx database.TenantTx, connection uuid.UUID, kind string, number int64, creator string) (uuid.UUID, error) {
	var id uuid.UUID
	e := tx.QueryRow(ctx, `SELECT id FROM app.github_items WHERE workspace_id=$1 AND connection_id=$2 AND kind=$3 AND number=$4 FOR UPDATE`, tx.WorkspaceID(), connection, kind, number).Scan(&id)
	if e == nil {
		return id, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return id, e
	}
	if number < 1 {
		return id, auth.ErrInvalid
	}
	id, e = uuid.NewV7()
	if e != nil {
		return id, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO app.objects(workspace_id,id,type,created_by) VALUES($1,$2,'github_item',$3)`, tx.WorkspaceID(), id, creator); e != nil {
		return id, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO app.github_items(workspace_id,id,connection_id,number,kind) VALUES($1,$2,$3,$4,$5)`, tx.WorkspaceID(), id, connection, number, kind)
	return id, e
}
func link(ctx context.Context, tx database.TenantTx, from, to uuid.UUID, kind string) error {
	id, e := uuid.NewV7()
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO app.links(workspace_id,id,from_object,to_object,link_type) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, tx.WorkspaceID(), id, from, to, kind)
	return e
}
func taskVisible(ctx context.Context, tx database.TenantTx, id uuid.UUID) bool {
	var ok bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.tasks t JOIN app.objects o ON o.workspace_id=t.workspace_id AND o.id=t.id JOIN app.links l ON l.workspace_id=t.workspace_id AND l.from_object=t.id AND l.link_type='belongs_to' JOIN app.objects p ON p.workspace_id=l.workspace_id AND p.id=l.to_object AND p.type='project' WHERE t.workspace_id=$1 AND t.id=$2 AND o.deleted_at IS NULL AND p.deleted_at IS NULL)`, tx.WorkspaceID(), id).Scan(&ok)
	return e == nil && ok
}
func (g *GitHub) MapIssue(ctx context.Context, s auth.Session, number int64, task uuid.UUID) error {
	if number < 1 || task == uuid.Nil {
		return auth.ErrInvalid
	}
	return g.Auth.InTenant(ctx, s, policy.IntegrationManage, "", func(tx database.TenantTx, human policy.Actor) error {
		if !policy.Can(ctx, human, policy.TaskRead, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) || !taskVisible(ctx, tx, task) {
			return auth.ErrNotFound
		}
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9410))`, tx.WorkspaceID().String()); e != nil {
			return e
		}
		var connection uuid.UUID
		if e := tx.QueryRow(ctx, `SELECT id FROM app.github_connections WHERE workspace_id=$1 AND connected AND repository_id IS NOT NULL FOR SHARE`, tx.WorkspaceID()).Scan(&connection); e != nil {
			return auth.ErrNotFound
		}
		id, e := item(ctx, tx, connection, "issue", number, human.ID)
		if e != nil {
			return e
		}
		if e = link(ctx, tx, id, task, "references"); e != nil {
			return e
		}
		return events.WriteAudited(ctx, tx, id, s.User.ID, "integration.linked", events.Payload{After: map[string]string{"task_id": task.String()}})
	})
}
func (g *GitHub) Process(ctx context.Context, actor policy.Actor, eventID uuid.UUID) error {
	return g.Auth.InConsumer(ctx, actor, func(tx database.TenantTx) error {
		if !policy.Can(ctx, actor, policy.IntegrationReceive, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
			return auth.ErrForbidden
		}
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9410))`, tx.WorkspaceID().String()); e != nil {
			return e
		}
		var body []byte
		var event string
		var connection, user, channel uuid.UUID
		var days int
		var processed *time.Time
		e := tx.QueryRow(ctx, `SELECT e.body,e.event_type,e.connection_id,e.processed_at,c.requested_by,c.channel_id,c.delay_days FROM app.github_events e JOIN app.github_connections c ON c.workspace_id=e.workspace_id AND c.id=e.connection_id WHERE e.workspace_id=$1 AND e.id=$2 AND c.connected AND c.channel_id IS NOT NULL FOR UPDATE OF e FOR SHARE OF c`, tx.WorkspaceID(), eventID).Scan(&body, &event, &connection, &processed, &user, &channel, &days)
		if e != nil {
			return e
		}
		if processed != nil {
			return nil
		}
		human, e := g.Auth.JobMember(ctx, tx, user, policy.IntegrationManage)
		if errors.Is(e, auth.ErrForbidden) {
			if _, e = tx.Exec(ctx, `UPDATE app.github_events SET processed_at=clock_timestamp(),outcome='ignored',body='{}'::jsonb WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), eventID); e != nil {
				return e
			}
			return events.WriteWorkerAudited(ctx, tx, actor, eventID, "integration.processed", events.Payload{After: map[string]string{"outcome": "ignored", "reason": "connector_permission_revoked"}})
		}
		if e != nil {
			return e
		}
		var p githubPayload
		if e = json.Unmarshal(body, &p); e != nil {
			return e
		}
		outcome := "ignored"
		var remote uuid.UUID
		var signal bool
		if event == "pull_request" || event == "issues" {
			pr := p.PullRequest
			kind := "pr"
			if event == "issues" {
				pr = p.Issue
				kind = "issue"
			}
			if pr == nil {
				return auth.ErrInvalid
			}
			remote, e = item(ctx, tx, connection, kind, pr.Number, human.ID)
			if e != nil {
				return e
			}
			var previous time.Time
			if e = tx.QueryRow(ctx, `SELECT updated_at FROM app.github_items WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), remote).Scan(&previous); e != nil {
				return e
			}
			// A newly-created item's server timestamp must not suppress its first event.
			var oldTitle string
			if e = tx.QueryRow(ctx, `SELECT title FROM app.github_items WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), remote).Scan(&oldTitle); e != nil {
				return e
			}
			if oldTitle == "" || !pr.UpdatedAt.Before(previous) {
				if _, e = tx.Exec(ctx, `UPDATE app.github_items SET title=$3,branch=$4,state=$5,updated_at=$6 WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), remote, limitText(pr.Title, 500), limitText(pr.Head.Ref, 200), limitText(pr.State, 20), pr.UpdatedAt); e != nil {
					return e
				}
				for _, value := range taskIdentifier.FindAllString(limitText(pr.Head.Ref, 200), 8) {
					task, e := uuid.Parse(value)
					if e == nil && taskVisible(ctx, tx, task) {
						if e = link(ctx, tx, remote, task, "references"); e != nil {
							return e
						}
					}
				}
				for _, match := range closesIssue.FindAllStringSubmatch(pr.Body, 20) {
					number, _ := strconv.ParseInt(match[1], 10, 64)
					issue, e := item(ctx, tx, connection, "issue", number, human.ID)
					if e != nil {
						return e
					}
					if e = link(ctx, tx, remote, issue, "closes"); e != nil {
						return e
					}
				}
				signal = pr.State == "open" && (pr.MergeableState == "blocked" || pr.MergeableState == "dirty")
				for _, label := range pr.Labels {
					if strings.EqualFold(label.Name, "blocked") && pr.State == "open" {
						signal = true
					}
				}
				outcome = "mapped"
			}
		} else if event == "check_run" && p.CheckRun != nil {
			switch p.CheckRun.Conclusion {
			case "failure", "timed_out", "action_required":
				signal = true
			}
			for _, pr := range p.CheckRun.PullRequests {
				if signal {
					remote, e = item(ctx, tx, connection, "pr", pr.Number, human.ID)
					if e != nil {
						return e
					}
					if e = g.signal(ctx, tx, actor, human, remote, channel, days, "failing check"); e != nil {
						return e
					}
				}
			}
			if signal {
				outcome = "signal"
			}
			signal = false
		}
		if signal {
			if e = g.signal(ctx, tx, actor, human, remote, channel, days, "blocked pull request"); e != nil {
				return e
			}
			outcome = "signal"
		}
		if _, e = tx.Exec(ctx, `UPDATE app.github_events SET processed_at=clock_timestamp(),outcome=$3,body='{}'::jsonb WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), eventID, outcome); e != nil {
			return e
		}
		return events.WriteWorkerAudited(ctx, tx, actor, eventID, "integration.processed", events.Payload{After: map[string]string{"outcome": outcome}})
	})
}
func limitText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func (g *GitHub) signal(ctx context.Context, tx database.TenantTx, service, human policy.Actor, remote, channel uuid.UUID, days int, reason string) error {
	for _, action := range []policy.Action{policy.MessagePost, policy.AgentRequest, policy.TaskRead} {
		if !policy.Can(ctx, human, action, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
			return auth.ErrForbidden
		}
	}
	var channelID uuid.UUID
	if e := tx.QueryRow(ctx, `SELECT c.id FROM app.channels c JOIN app.objects o ON o.workspace_id=c.workspace_id AND o.id=c.id WHERE c.workspace_id=$1 AND c.id=$2 AND o.deleted_at IS NULL`, tx.WorkspaceID(), channel).Scan(&channelID); e != nil {
		return auth.ErrNotFound
	}
	// A recent signal is a scenario, not accumulated delay. Rate-bound repeat notifications.
	var title, state string
	var number int64
	var last *time.Time
	if e := tx.QueryRow(ctx, `SELECT title,state,number,last_signal_at FROM app.github_items WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, tx.WorkspaceID(), remote).Scan(&title, &state, &number, &last); e != nil {
		return e
	}
	if state == "closed" {
		return nil
	}
	if last != nil && last.After(time.Now().Add(-24*time.Hour)) {
		return nil
	}
	rows, e := tx.Query(ctx, `SELECT DISTINCT l.to_object FROM app.links l WHERE l.workspace_id=$1 AND l.link_type='references' AND (l.from_object=$2 OR l.from_object IN (SELECT to_object FROM app.links WHERE workspace_id=$1 AND from_object=$2 AND link_type='closes')) LIMIT 8`, tx.WorkspaceID(), remote)
	if e != nil {
		return e
	}
	var tasks []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		tasks = append(tasks, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, task := range tasks {
		if !taskVisible(ctx, tx, task) {
			continue
		}
		id, e := uuid.NewV7()
		if e != nil {
			return e
		}
		client, e := uuid.NewV7()
		if e != nil {
			return e
		}
		var taskTitle string
		if e = tx.QueryRow(ctx, `SELECT title FROM app.tasks WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), task).Scan(&taskTitle); e != nil {
			return e
		}
		body := fmt.Sprintf("GitHub risk signal (untrusted external evidence; configured estimate, human approval required): %s; %s will slip %d days. slipgraph://task/%s PR/issue #%d: %s", reason, limitText(taskTitle, 100), days, task, number, limitText(title, 500))
		if _, e = tx.Exec(ctx, `INSERT INTO app.objects(workspace_id,id,type,created_by) VALUES($1,$2,'message',$3)`, tx.WorkspaceID(), id, human.ID); e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO app.messages(workspace_id,id,body,client_id) VALUES($1,$2,$3,$4)`, tx.WorkspaceID(), id, body, client); e != nil {
			return e
		}
		for _, l := range []struct {
			to   uuid.UUID
			kind string
		}{{channel, "belongs_to"}, {task, "references"}, {remote, "source"}} {
			if e = link(ctx, tx, id, l.to, l.kind); e != nil {
				return e
			}
		}
		if e = events.WriteWorkerAudited(ctx, tx, service, id, "message.created", events.Payload{Version: 1, After: map[string]interface{}{"channel_id": channel, "body_bytes": len(body), "origin": "github"}}); e != nil {
			return e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE app.github_items SET last_signal_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), remote)
	return e
}

// Sweep processes durable ingress and checks known open PRs for staleness. No API polling or model calls.
func (g *GitHub) Sweep(ctx context.Context, actor policy.Actor) error {
	if !g.Config.enabled() && !g.Config.worker {
		return nil
	}
	var ids []uuid.UUID
	e := g.Auth.InConsumer(ctx, actor, func(tx database.TenantTx) error {
		if !policy.Can(ctx, actor, policy.IntegrationReceive, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
			return auth.ErrForbidden
		}
		rows, e := tx.Query(ctx, `SELECT id FROM app.github_events WHERE workspace_id=$1 AND processed_at IS NULL ORDER BY id LIMIT 20`, tx.WorkspaceID())
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if e = rows.Scan(&id); e != nil {
				return e
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = g.Process(ctx, actor, id); e != nil {
			return e
		}
	}
	ids = nil
	e = g.Auth.InConsumer(ctx, actor, func(tx database.TenantTx) error {
		rows, e := tx.Query(ctx, `SELECT i.id FROM app.github_items i JOIN app.github_connections c ON c.workspace_id=i.workspace_id AND c.id=i.connection_id WHERE i.workspace_id=$1 AND c.connected AND i.kind='pr' AND i.state='open' AND i.updated_at<clock_timestamp()-make_interval(days=>c.stale_days) AND (i.last_signal_at IS NULL OR i.last_signal_at<clock_timestamp()-interval '24 hours') ORDER BY i.id LIMIT 20`, tx.WorkspaceID())
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id uuid.UUID
			if e = rows.Scan(&id); e != nil {
				return e
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if e != nil {
		return e
	}
	for _, id := range ids {
		if e = g.Stale(ctx, actor, id); e != nil {
			return e
		}
	}
	return nil
}
func (g *GitHub) Stale(ctx context.Context, actor policy.Actor, id uuid.UUID) error {
	return g.Auth.InConsumer(ctx, actor, func(tx database.TenantTx) error {
		if !policy.Can(ctx, actor, policy.IntegrationReceive, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
			return auth.ErrForbidden
		}
		var user, channel uuid.UUID
		var days int
		var stale bool
		e := tx.QueryRow(ctx, `SELECT c.requested_by,c.channel_id,c.delay_days,(i.kind='pr' AND i.state='open' AND i.updated_at<clock_timestamp()-make_interval(days=>c.stale_days)) FROM app.github_items i JOIN app.github_connections c ON c.workspace_id=i.workspace_id AND c.id=i.connection_id WHERE i.workspace_id=$1 AND i.id=$2 AND c.connected FOR UPDATE OF i FOR SHARE OF c`, tx.WorkspaceID(), id).Scan(&user, &channel, &days, &stale)
		if e != nil {
			return e
		}
		if !stale {
			return nil
		}
		human, e := g.Auth.JobMember(ctx, tx, user, policy.IntegrationManage)
		if e != nil {
			return e
		}
		return g.signal(ctx, tx, actor, human, id, channel, days, "stale pull request")
	})
}

func (g *GitHub) Run(ctx context.Context) {
	if !g.Config.enabled() && !g.Config.worker {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			actors, e := g.Auth.ConsumerSessions(ctx)
			if e != nil {
				continue
			}
			for _, a := range actors {
				_ = g.Sweep(ctx, a)
			}
		}
	}
}
