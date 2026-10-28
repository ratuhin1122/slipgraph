package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

type GitHub struct {
	Auth   *auth.Service
	Config GitHubConfig
	Client GitHubClient
}

func NewGitHub(a *auth.Service, c GitHubConfig) *GitHub {
	return &GitHub{Auth: a, Config: c, Client: newGitHubClient(c)}
}

type GitHubStatus struct {
	Enabled    bool       `json:"enabled"`
	Connected  bool       `json:"connected"`
	Repository string     `json:"repository"`
	ChannelID  *uuid.UUID `json:"channel_id"`
	StaleDays  int        `json:"stale_days"`
	DelayDays  int        `json:"delay_days"`
}
type GitHubSetup struct {
	Repository string    `json:"repository"`
	ChannelID  uuid.UUID `json:"channel_id"`
	StaleDays  int       `json:"stale_days"`
	DelayDays  int       `json:"delay_days"`
}
type GitHubHook struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}
type GitHubLog struct {
	ID         uuid.UUID `json:"id"`
	EventType  string    `json:"event_type"`
	Outcome    string    `json:"outcome"`
	ReceivedAt time.Time `json:"received_at"`
}

func (g *GitHub) Status(ctx context.Context, s auth.Session) (GitHubStatus, error) {
	out := GitHubStatus{Enabled: g.Config.enabled(), StaleDays: 7, DelayDays: 1}
	e := g.Auth.InTenant(ctx, s, policy.IntegrationRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		e := tx.QueryRow(ctx, `SELECT connected,repository,channel_id,stale_days,delay_days FROM app.github_connections WHERE workspace_id=$1`, tx.WorkspaceID()).Scan(&out.Connected, &out.Repository, &out.ChannelID, &out.StaleDays, &out.DelayDays)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		return e
	})
	return out, e
}
func (g *GitHub) StartOAuth(ctx context.Context, s auth.Session) (string, error) {
	if !g.Config.enabled() {
		return "", ErrGitHub
	}
	state, verifier := opaque(), opaque()
	redirect := g.Config.Origin + "/api/integrations/github/callback"
	e := g.Auth.InTenant(ctx, s, policy.IntegrationManage, "", func(tx database.TenantTx, _ policy.Actor) error {
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9410))`, tx.WorkspaceID().String()); e != nil {
			return e
		}
		var id uuid.UUID
		e := tx.QueryRow(ctx, `SELECT id FROM app.github_connections WHERE workspace_id=$1 FOR UPDATE`, tx.WorkspaceID()).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			id, e = uuid.NewV7()
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO app.objects(workspace_id,id,type,created_by) VALUES($1,$2,'integration',$3)`, tx.WorkspaceID(), id, s.User.ID); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO app.github_connections(workspace_id,id,requested_by) VALUES($1,$2,$3)`, tx.WorkspaceID(), id, s.User.ID); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		encrypted, e := seal(g.Config.Key, binding(tx.WorkspaceID(), id, "pkce:"+s.ID.String()), []byte(verifier))
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE app.github_connections SET state_hash=$2,verifier_cipher=$3,oauth_session=$4,oauth_expires_at=clock_timestamp()+interval '10 minutes' WHERE workspace_id=$1`, tx.WorkspaceID(), digest(state), encrypted, s.ID)
		return e
	})
	if e != nil {
		return "", e
	}
	// No requested OAuth scopes: public repositories only. Previously broader grants are rejected.
	u := url.URL{Scheme: "https", Host: "github.com", Path: "/login/oauth/authorize"}
	q := url.Values{"client_id": {g.Config.ClientID}, "redirect_uri": {redirect}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hashBytes(verifier))}, "code_challenge_method": {"S256"}, "allow_signup": {"false"}}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func (g *GitHub) Complete(ctx context.Context, s auth.Session, state, code string) error {
	if !g.Config.enabled() || len(state) != 43 || len(code) < 1 || len(code) > 1024 {
		return auth.ErrInvalid
	}
	var id uuid.UUID
	var encrypted []byte
	e := g.Auth.InTenant(ctx, s, policy.IntegrationManage, "", func(tx database.TenantTx, _ policy.Actor) error {
		e := tx.QueryRow(ctx, `SELECT id,verifier_cipher FROM app.github_connections WHERE workspace_id=$1 AND state_hash=$2 AND oauth_session=$3 AND oauth_expires_at>clock_timestamp() FOR UPDATE`, tx.WorkspaceID(), digest(state), s.ID).Scan(&id, &encrypted)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE app.github_connections SET state_hash=$2,verifier_cipher=NULL,oauth_expires_at=NULL WHERE workspace_id=$1`, tx.WorkspaceID(), digest("claimed:"+state))
		return e
	})
	if e != nil {
		return auth.ErrInvalid
	}
	verifier, e := unseal(g.Config.Key, binding(s.WorkspaceID, id, "pkce:"+s.ID.String()), encrypted)
	if e != nil {
		return e
	}
	token, e := g.Client.Exchange(ctx, code, string(verifier), g.Config.Origin+"/api/integrations/github/callback")
	if e != nil {
		return ErrGitHub
	}
	cipher, e := seal(g.Config.Key, binding(s.WorkspaceID, id, "oauth"), []byte(token))
	if e != nil {
		return e
	}
	return g.Auth.InTenant(ctx, s, policy.IntegrationManage, "", func(tx database.TenantTx, _ policy.Actor) error {
		tag, e := tx.Exec(ctx, `UPDATE app.github_connections SET connected=true,access_cipher=$3,requested_by=$4,state_hash=NULL,oauth_session=NULL WHERE workspace_id=$1 AND id=$2 AND oauth_session=$5 AND state_hash=$6`, tx.WorkspaceID(), id, cipher, s.User.ID, s.ID, digest("claimed:"+state))
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrConflict
		}
		return events.WriteAudited(ctx, tx, id, s.User.ID, "integration.connected", events.Payload{After: map[string]string{"provider": "github"}})
	})
}
func (g *GitHub) Configure(ctx context.Context, s auth.Session, in GitHubSetup) (GitHubHook, error) {
	out := GitHubHook{}
	if !g.Config.enabled() || !repositoryName.MatchString(in.Repository) || in.ChannelID == uuid.Nil || in.StaleDays < 1 || in.StaleDays > 90 || in.DelayDays < 1 || in.DelayDays > 365 {
		return out, auth.ErrInvalid
	}
	var id uuid.UUID
	var encrypted []byte
	e := g.Auth.InTenant(ctx, s, policy.IntegrationManage, "", func(tx database.TenantTx, _ policy.Actor) error {
		return tx.QueryRow(ctx, `SELECT id,access_cipher FROM app.github_connections WHERE workspace_id=$1 AND connected FOR SHARE`, tx.WorkspaceID()).Scan(&id, &encrypted)
	})
	if errors.Is(e, pgx.ErrNoRows) {
		return out, auth.ErrNotFound
	}
	if e != nil {
		return out, e
	}
	token, e := unseal(g.Config.Key, binding(s.WorkspaceID, id, "oauth"), encrypted)
	if e != nil {
		return out, e
	}
	repo, e := g.Client.Repository(ctx, string(token), in.Repository)
	if e != nil {
		return out, ErrGitHub
	}
	actors, e := g.Auth.ConsumerSessions(ctx)
	if e != nil {
		return out, e
	}
	var actor policy.Actor
	for _, a := range actors {
		if a.WorkspaceID == s.WorkspaceID.String() {
			actor = a
			break
		}
	}
	if actor.ID == "" {
		return out, auth.ErrForbidden
	}
	routeData, _ := json.Marshal(route{Workspace: s.WorkspaceID, Connection: id, Consumer: uuid.MustParse(actor.ID), Nonce: opaque()})
	routeCipher, e := seal(g.Config.Key, "slipgraph:github:route:v1", routeData)
	if e != nil {
		return out, e
	}
	routing := base64.RawURLEncoding.EncodeToString(routeCipher)
	secret := opaque()
	encryptedSecret, e := seal(g.Config.Key, binding(s.WorkspaceID, id, "webhook"), []byte(secret))
	if e != nil {
		return out, e
	}
	e = g.Auth.InTenant(ctx, s, policy.IntegrationManage, "", func(tx database.TenantTx, _ policy.Actor) error {
		var channel uuid.UUID
		if e := tx.QueryRow(ctx, `SELECT c.id FROM app.channels c JOIN app.objects o ON o.workspace_id=c.workspace_id AND o.id=c.id WHERE c.workspace_id=$1 AND c.id=$2 AND o.deleted_at IS NULL`, tx.WorkspaceID(), in.ChannelID).Scan(&channel); e != nil {
			return auth.ErrNotFound
		}
		tag, e := tx.Exec(ctx, `UPDATE app.github_connections SET repository=$3,repository_id=$4,channel_id=$5,stale_days=$6,delay_days=$7,route_hash=$8,webhook_cipher=$9,requested_by=$10 WHERE workspace_id=$1 AND id=$2 AND connected AND access_cipher=$11 AND (repository_id IS NULL OR repository_id=$4)`, tx.WorkspaceID(), id, in.Repository, repo, in.ChannelID, in.StaleDays, in.DelayDays, digest(routing), encryptedSecret, s.User.ID, encrypted)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrConflict
		}
		return events.WriteAudited(ctx, tx, id, s.User.ID, "integration.configured", events.Payload{After: map[string]string{"provider": "github"}})
	})
	if e != nil {
		return out, e
	}
	return GitHubHook{URL: g.Config.Origin + "/api/hooks/github/" + routing, Secret: secret}, nil
}
func (g *GitHub) Disconnect(ctx context.Context, s auth.Session) error {
	return g.Auth.InTenant(ctx, s, policy.IntegrationManage, "", func(tx database.TenantTx, _ policy.Actor) error {
		if _, e := tx.Exec(ctx, `UPDATE app.github_events SET processed_at=clock_timestamp(),outcome='ignored',body='{}'::jsonb WHERE workspace_id=$1 AND processed_at IS NULL`, tx.WorkspaceID()); e != nil {
			return e
		}
		var id uuid.UUID
		e := tx.QueryRow(ctx, `UPDATE app.github_connections SET connected=false,access_cipher=NULL,webhook_cipher=NULL,route_hash=NULL,state_hash=NULL,verifier_cipher=NULL,oauth_session=NULL,oauth_expires_at=NULL WHERE workspace_id=$1 RETURNING id`, tx.WorkspaceID()).Scan(&id)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		return events.WriteAudited(ctx, tx, id, s.User.ID, "integration.disconnected", events.Payload{After: map[string]string{"provider": "github"}})
	})
}
func (g *GitHub) Logs(ctx context.Context, s auth.Session) ([]GitHubLog, error) {
	out := []GitHubLog{}
	e := g.Auth.InTenant(ctx, s, policy.IntegrationRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		rows, e := tx.Query(ctx, `SELECT id,event_type,outcome,received_at FROM app.github_events WHERE workspace_id=$1 ORDER BY id DESC LIMIT 100`, tx.WorkspaceID())
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var x GitHubLog
			if e = rows.Scan(&x.ID, &x.EventType, &x.Outcome, &x.ReceivedAt); e != nil {
				return e
			}
			out = append(out, x)
		}
		return rows.Err()
	})
	return out, e
}

// NewGitHubWorker has no OAuth client or encryption key; it only processes RLS-scoped ingress.
func NewGitHubWorker(a *auth.Service) *GitHub {
	return &GitHub{Auth: a, Config: GitHubConfig{worker: true}}
}
