package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

type route struct {
	Workspace, Connection, Consumer uuid.UUID
	Nonce                           string
}
type githubRepository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}
type githubPR struct {
	Number         int64     `json:"number"`
	Title          string    `json:"title"`
	Body           string    `json:"body"`
	State          string    `json:"state"`
	UpdatedAt      time.Time `json:"updated_at"`
	MergeableState string    `json:"mergeable_state"`
	Draft          bool      `json:"draft"`
	Head           struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
}
type githubPayload struct {
	Repository  githubRepository `json:"repository"`
	Action      string           `json:"action"`
	Zen         string           `json:"zen"`
	PullRequest *githubPR        `json:"pull_request"`
	Issue       *githubPR        `json:"issue"`
	CheckRun    *struct {
		Conclusion   string    `json:"conclusion"`
		CompletedAt  time.Time `json:"completed_at"`
		StartedAt    time.Time `json:"started_at"`
		PullRequests []struct {
			Number int64 `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
}

func (g *GitHub) Receive(ctx context.Context, routing, delivery, event, signature string, body []byte) error {
	if !g.Config.enabled() || len(routing) > 1000 || len(routing) < 100 || len(body) > 1<<20 {
		return auth.ErrNotFound
	}
	encrypted, e := base64.RawURLEncoding.DecodeString(routing)
	if e != nil {
		return auth.ErrNotFound
	}
	data, e := unseal(g.Config.Key, "slipgraph:github:route:v1", encrypted)
	if e != nil {
		return auth.ErrNotFound
	}
	var target route
	if json.Unmarshal(data, &target) != nil || target.Workspace == uuid.Nil || target.Connection == uuid.Nil || target.Consumer == uuid.Nil || len(target.Nonce) != 43 {
		return auth.ErrNotFound
	}
	actor, e := g.Auth.BoundConsumer(ctx, target.Workspace, target.Consumer)
	if e != nil {
		return auth.ErrNotFound
	}
	if !policy.Can(ctx, actor, policy.IntegrationReceive, policy.Resource{WorkspaceID: target.Workspace.String()}) {
		return auth.ErrForbidden
	}
	if e = g.Auth.Rate(ctx, "github:hook:"+actor.WorkspaceID, target.Connection.String(), 120, time.Minute); e != nil {
		return e
	}
	deliveryID, e := uuid.Parse(delivery)
	if e != nil || deliveryID == uuid.Nil {
		return ErrWebhook
	}
	if event != "ping" && event != "pull_request" && event != "issues" && event != "check_run" {
		return ErrWebhook
	}
	return g.Auth.InConsumer(ctx, actor, func(tx database.TenantTx) error {
		var secretCipher []byte
		var repoID int64
		var repoName string
		if e := tx.QueryRow(ctx, `SELECT webhook_cipher,repository_id,repository FROM app.github_connections WHERE workspace_id=$1 AND id=$2 AND connected AND route_hash=$3 FOR SHARE`, tx.WorkspaceID(), target.Connection, digest(routing)).Scan(&secretCipher, &repoID, &repoName); e != nil {
			return auth.ErrNotFound
		}
		secret, e := unseal(g.Config.Key, binding(tx.WorkspaceID(), target.Connection, "webhook"), secretCipher)
		if e != nil {
			return ErrWebhook
		}
		if e = VerifyGitHubSignature(string(secret), signature, body); e != nil {
			return e
		}
		var payload githubPayload
		if json.Unmarshal(body, &payload) != nil || payload.Repository.ID != repoID || !strings.EqualFold(payload.Repository.FullName, repoName) {
			return ErrWebhook
		}
		var stamp time.Time
		switch event {
		case "pull_request":
			if payload.PullRequest == nil {
				return ErrWebhook
			}
			if payload.PullRequest.Number < 1 || (payload.PullRequest.State != "open" && payload.PullRequest.State != "closed") {
				return ErrWebhook
			}
			stamp = payload.PullRequest.UpdatedAt
		case "issues":
			if payload.Issue == nil {
				return ErrWebhook
			}
			if payload.Issue.Number < 1 || (payload.Issue.State != "open" && payload.Issue.State != "closed") {
				return ErrWebhook
			}
			stamp = payload.Issue.UpdatedAt
		case "check_run":
			if payload.CheckRun == nil {
				return ErrWebhook
			}
			if len(payload.CheckRun.PullRequests) > 100 {
				return ErrWebhook
			}
			stamp = payload.CheckRun.CompletedAt
			if stamp.IsZero() {
				stamp = payload.CheckRun.StartedAt
			}
		case "ping":
			if payload.Zen == "" {
				return ErrWebhook
			}
		}
		if event != "ping" && (stamp.IsZero() || stamp.Before(time.Now().Add(-7*24*time.Hour)) || stamp.After(time.Now().Add(30*time.Second))) {
			return ErrWebhook
		}

		// Retain only modeled evidence. In particular ping.hook.config can echo
		// the private callback URL; routing credentials must never enter JSONB.
		stored, e := json.Marshal(payload)
		if e != nil {
			return ErrWebhook
		}
		if event == "ping" {
			stored = []byte(`{}`)
		}
		// Delivery headers are unsigned: also reject the same signed body under a changed ID.
		var duplicate bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.github_events WHERE workspace_id=$1 AND connection_id=$2 AND (delivery_id=$3 OR body_hash=$4))`, tx.WorkspaceID(), target.Connection, deliveryID, digest(string(body))).Scan(&duplicate); e != nil {
			return e
		}
		if duplicate {
			return ErrWebhook
		}
		id, e := uuid.NewV7()
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO app.objects(workspace_id,id,type,created_by) VALUES($1,$2,'integration_event',$3)`, tx.WorkspaceID(), id, actor.ID); e != nil {
			return e
		}
		tag, e := tx.Exec(ctx, `INSERT INTO app.github_events(workspace_id,id,connection_id,delivery_id,body_hash,event_type,body) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, tx.WorkspaceID(), id, target.Connection, deliveryID, digest(string(body)), event, stored)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return ErrWebhook
		}
		return events.WriteWorkerAudited(ctx, tx, actor, id, "integration.received", events.Payload{After: map[string]string{"provider": "github", "kind": event}})
	})
}
func webhookError(e error) error {
	if errors.Is(e, ErrWebhook) {
		return auth.ErrForbidden
	}
	return e
}
