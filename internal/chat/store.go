package chat

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

type Store struct{ Auth *auth.Service }
type Channel struct {
	ID      uuid.UUID `json:"id"`
	Name    string    `json:"name"`
	Version int64     `json:"version"`
}
type Mention struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}
type TaskLink struct {
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"project_id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
}
type Message struct {
	ID         uuid.UUID  `json:"id"`
	ChannelID  uuid.UUID  `json:"channel_id"`
	AuthorID   uuid.UUID  `json:"author_id"`
	AuthorName string     `json:"author_name"`
	Body       string     `json:"body"`
	ThreadID   *uuid.UUID `json:"thread_id"`
	ClientID   uuid.UUID  `json:"client_id"`
	Version    int64      `json:"version"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Deleted    bool       `json:"deleted"`
	ReplyCount int        `json:"reply_count"`
	Mentions   []Mention  `json:"mentions"`
	TaskLinks  []TaskLink `json:"task_links"`
}
type Page struct {
	Items      []Message  `json:"items"`
	NextCursor *uuid.UUID `json:"next_cursor"`
}
type Input struct {
	Body       string
	MentionIDs []uuid.UUID
	ThreadID   *uuid.UUID
	ClientID   uuid.UUID
}
type OnlineMember struct {
	UserID uuid.UUID `json:"user_id"`
	Name   string    `json:"name"`
	Typing bool      `json:"typing"`
}

func conflict(err error) error {
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		return auth.ErrConflict
	}
	return err
}
func channel(ctx context.Context, tx database.TenantTx, id uuid.UUID) (Channel, error) {
	var c Channel
	err := tx.QueryRow(ctx, `SELECT c.id,c.name,c.version FROM app.channels c JOIN app.objects o ON o.workspace_id=c.workspace_id AND o.id=c.id WHERE c.workspace_id=$1 AND c.id=$2 AND o.deleted_at IS NULL`, tx.WorkspaceID(), id).Scan(&c.ID, &c.Name, &c.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	return c, err
}

// WithChannel rechecks the live session and policy before resolving a scoped resource.
func (s *Store) WithChannel(ctx context.Context, session auth.Session, id uuid.UUID, action policy.Action, fn func(database.TenantTx, policy.Actor) error) error {
	return s.Auth.InTenant(ctx, session, action, "", func(tx database.TenantTx, actor policy.Actor) error {
		if _, err := channel(ctx, tx, id); err != nil {
			return err
		}
		return fn(tx, actor)
	})
}
func (s *Store) Channel(ctx context.Context, session auth.Session, id uuid.UUID) (Channel, error) {
	var c Channel
	err := s.Auth.InTenant(ctx, session, policy.ChannelRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		var err error
		c, err = channel(ctx, tx, id)
		return err
	})
	return c, err
}
func (s *Store) Channels(ctx context.Context, session auth.Session) ([]Channel, error) {
	result := make([]Channel, 0)
	err := s.Auth.InTenant(ctx, session, policy.ChannelRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		rows, err := tx.Query(ctx, `SELECT c.id,c.name,c.version FROM app.channels c JOIN app.objects o ON o.workspace_id=c.workspace_id AND o.id=c.id WHERE c.workspace_id=$1 AND o.deleted_at IS NULL ORDER BY c.name,c.id LIMIT 500`, tx.WorkspaceID())
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Channel
			if err := rows.Scan(&c.ID, &c.Name, &c.Version); err != nil {
				return err
			}
			result = append(result, c)
		}
		return rows.Err()
	})
	return result, err
}
func (s *Store) CreateChannel(ctx context.Context, session auth.Session, name string) (Channel, error) {
	name = strings.TrimSpace(name)
	var result Channel
	if len(name) == 0 || len(name) > 80 || strings.ContainsAny(name, "\r\n\x00") {
		return result, auth.ErrInvalid
	}
	if err := s.Auth.Rate(ctx, "chat:channel:"+session.WorkspaceID.String(), session.User.ID.String(), 20, time.Hour); err != nil {
		return result, err
	}
	err := s.Auth.InTenant(ctx, session, policy.ChannelCreate, "", func(tx database.TenantTx, actor policy.Actor) error {
		// Bound the directory so clients can always load all channels.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9404))`, tx.WorkspaceID().String()); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.channels WHERE workspace_id=$1`, tx.WorkspaceID()).Scan(&count); err != nil {
			return err
		}
		if count >= 500 {
			return auth.ErrLimited
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'channel',$3)`, id, tx.WorkspaceID(), actor.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO app.channels(id,workspace_id,name) VALUES($1,$2,$3)`, id, tx.WorkspaceID(), name); err != nil {
			return conflict(err)
		}
		result = Channel{ID: id, Name: name, Version: 1}
		return events.Write(ctx, tx, id, session.User.ID, "channel.created", events.Payload{Version: 1, After: map[string]string{"channel_id": id.String()}})
	})
	return result, err
}

var taskURI = regexp.MustCompile(`slipgraph://task/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})`)

func taskIDs(body string) ([]uuid.UUID, error) {
	ids := make([]uuid.UUID, 0)
	seen := map[uuid.UUID]bool{}
	for _, match := range taskURI.FindAllStringSubmatch(body, -1) {
		id, err := uuid.Parse(match[1])
		if err != nil || id == uuid.Nil {
			return nil, auth.ErrInvalid
		}
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	if len(ids) > 20 {
		return nil, auth.ErrInvalid
	}
	return ids, nil
}
func validBody(body string, mentions []uuid.UUID) bool {
	return utf8.ValidString(body) && len(body) <= 8000 && strings.TrimSpace(body) != "" && !strings.ContainsRune(body, 0) && len(mentions) <= 20
}
func addLink(ctx context.Context, tx database.TenantTx, from, to uuid.UUID, kind string) error {
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO app.links(id,workspace_id,from_object,to_object,link_type) VALUES($1,$2,$3,$4,$5)`, id, tx.WorkspaceID(), from, to, kind)
	return err
}
func references(ctx context.Context, tx database.TenantTx, actor policy.Actor, id uuid.UUID, body string, mentions []uuid.UUID) error {
	taskRefs, err := taskIDs(body)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app.links WHERE workspace_id=$1 AND from_object=$2 AND link_type IN ('mentions','references')`, tx.WorkspaceID(), id); err != nil {
		return err
	}
	seen := map[uuid.UUID]bool{}
	for _, member := range mentions {
		if member == uuid.Nil {
			return auth.ErrInvalid
		}
		if seen[member] {
			continue
		}
		seen[member] = true
		var found uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT m.id FROM app.members m JOIN app.objects o ON o.id=m.id AND o.workspace_id=m.workspace_id WHERE m.workspace_id=$1 AND m.id=$2 AND o.deleted_at IS NULL`, tx.WorkspaceID(), member).Scan(&found); err != nil {
			return auth.ErrNotFound
		}
		if err := addLink(ctx, tx, id, member, "mentions"); err != nil {
			return err
		}
	}
	for _, task := range taskRefs {
		if !policy.Can(ctx, actor, policy.TaskRead, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
			return auth.ErrForbidden
		}
		var found uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT t.id FROM app.tasks t JOIN app.objects o ON o.id=t.id AND o.workspace_id=t.workspace_id JOIN app.links l ON l.from_object=t.id AND l.workspace_id=t.workspace_id AND l.link_type='belongs_to' JOIN app.projects p ON p.id=l.to_object AND p.workspace_id=t.workspace_id JOIN app.objects po ON po.id=p.id AND po.workspace_id=p.workspace_id WHERE t.workspace_id=$1 AND t.id=$2 AND o.deleted_at IS NULL AND po.deleted_at IS NULL`, tx.WorkspaceID(), task).Scan(&found); err != nil {
			return auth.ErrNotFound
		}
		if err := addLink(ctx, tx, id, task, "references"); err != nil {
			return err
		}
	}
	return nil
}

const messageSelect = `SELECT m.id,l.to_object,o.created_by,a.display_name,CASE WHEN o.deleted_at IS NULL THEN m.body ELSE '' END,m.thread_id,m.client_id,m.version,o.created_at,o.updated_at,o.deleted_at IS NOT NULL,(SELECT count(*) FROM app.messages reply JOIN app.objects ro ON ro.id=reply.id AND ro.workspace_id=reply.workspace_id WHERE reply.workspace_id=m.workspace_id AND reply.thread_id=m.id AND ro.deleted_at IS NULL) FROM app.messages m JOIN app.objects o ON o.id=m.id AND o.workspace_id=m.workspace_id JOIN app.members a ON a.user_id=o.created_by AND a.workspace_id=m.workspace_id JOIN app.links l ON l.from_object=m.id AND l.workspace_id=m.workspace_id AND l.link_type='belongs_to' JOIN app.channels c ON c.id=l.to_object AND c.workspace_id=m.workspace_id JOIN app.objects co ON co.id=c.id AND co.workspace_id=c.workspace_id WHERE m.workspace_id=$1 AND co.deleted_at IS NULL`

func scanMessage(row pgx.Row) (Message, error) {
	m := Message{Mentions: []Mention{}, TaskLinks: []TaskLink{}}
	err := row.Scan(&m.ID, &m.ChannelID, &m.AuthorID, &m.AuthorName, &m.Body, &m.ThreadID, &m.ClientID, &m.Version, &m.CreatedAt, &m.UpdatedAt, &m.Deleted, &m.ReplyCount)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	return m, err
}
func enrich(ctx context.Context, tx database.TenantTx, m *Message) error {
	if m.Deleted {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT a.id,a.display_name FROM app.links l JOIN app.members a ON a.id=l.to_object AND a.workspace_id=l.workspace_id JOIN app.objects o ON o.id=a.id AND o.workspace_id=a.workspace_id WHERE l.workspace_id=$1 AND l.from_object=$2 AND l.link_type='mentions' AND o.deleted_at IS NULL ORDER BY a.id`, tx.WorkspaceID(), m.ID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var a Mention
		if err := rows.Scan(&a.ID, &a.Name); err != nil {
			rows.Close()
			return err
		}
		m.Mentions = append(m.Mentions, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT t.id,parent.to_object,t.title,t.status FROM app.links l JOIN app.tasks t ON t.id=l.to_object AND t.workspace_id=l.workspace_id JOIN app.objects o ON o.id=t.id AND o.workspace_id=t.workspace_id JOIN app.links parent ON parent.from_object=t.id AND parent.workspace_id=t.workspace_id AND parent.link_type='belongs_to' JOIN app.objects po ON po.id=parent.to_object AND po.workspace_id=t.workspace_id WHERE l.workspace_id=$1 AND l.from_object=$2 AND l.link_type='references' AND o.deleted_at IS NULL AND po.deleted_at IS NULL ORDER BY t.id`, tx.WorkspaceID(), m.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var link TaskLink
		if err := rows.Scan(&link.ID, &link.ProjectID, &link.Title, &link.Status); err != nil {
			return err
		}
		m.TaskLinks = append(m.TaskLinks, link)
	}
	return rows.Err()
}
func message(ctx context.Context, tx database.TenantTx, id uuid.UUID) (Message, error) {
	m, err := scanMessage(tx.QueryRow(ctx, messageSelect+` AND m.id=$2`, tx.WorkspaceID(), id))
	if err == nil {
		err = enrich(ctx, tx, &m)
	}
	return m, err
}
func (s *Store) Messages(ctx context.Context, session auth.Session, channelID uuid.UUID, thread, cursor *uuid.UUID, limit int) (Page, error) {
	result := Page{Items: []Message{}}
	if limit < 1 || limit > 100 {
		return result, auth.ErrInvalid
	}
	err := s.WithChannel(ctx, session, channelID, policy.ChannelRead, func(tx database.TenantTx, _ policy.Actor) error {
		if thread != nil {
			root, err := message(ctx, tx, *thread)
			if err != nil {
				return err
			}
			if root.ChannelID != channelID || root.ThreadID != nil {
				return auth.ErrNotFound
			}
		}
		rows, err := tx.Query(ctx, messageSelect+` AND l.to_object=$2 AND m.thread_id IS NOT DISTINCT FROM $3::uuid AND ($4::uuid IS NULL OR m.id<$4) ORDER BY m.id DESC LIMIT $5`, tx.WorkspaceID(), channelID, thread, cursor, limit+1)
		if err != nil {
			return err
		}
		for rows.Next() {
			m, err := scanMessage(rows)
			if err != nil {
				rows.Close()
				return err
			}
			result.Items = append(result.Items, m)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(result.Items) > limit {
			result.Items = result.Items[:limit]
			id := result.Items[limit-1].ID
			result.NextCursor = &id
		}
		for i := range result.Items {
			if err := enrich(ctx, tx, &result.Items[i]); err != nil {
				return err
			}
		}
		for i, j := 0, len(result.Items)-1; i < j; i, j = i+1, j-1 {
			result.Items[i], result.Items[j] = result.Items[j], result.Items[i]
		}
		return nil
	})
	return result, err
}
func (s *Store) Post(ctx context.Context, session auth.Session, channelID uuid.UUID, input Input) (Message, error) {
	var result Message
	if !validBody(input.Body, input.MentionIDs) || input.ClientID == uuid.Nil {
		return result, auth.ErrInvalid
	}
	if err := s.Auth.Rate(ctx, "chat:post:"+session.WorkspaceID.String(), session.User.ID.String(), 30, time.Minute); err != nil {
		return result, err
	}
	err := s.WithChannel(ctx, session, channelID, policy.MessagePost, func(tx database.TenantTx, actor policy.Actor) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9405))`, tx.WorkspaceID().String()+":"+input.ClientID.String()); err != nil {
			return err
		}
		var oldID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM app.messages WHERE workspace_id=$1 AND client_id=$2`, tx.WorkspaceID(), input.ClientID).Scan(&oldID)
		if err == nil {
			old, err := message(ctx, tx, oldID)
			if err != nil {
				return err
			}
			if old.AuthorID != session.User.ID || old.ChannelID != channelID {
				return auth.ErrConflict
			}
			result = old
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if input.ThreadID != nil {
			root, err := message(ctx, tx, *input.ThreadID)
			if err != nil {
				return err
			}
			if root.ChannelID != channelID || root.ThreadID != nil || root.Deleted {
				return auth.ErrNotFound
			}
		}
		id, err := uuid.NewV7()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'message',$3)`, id, tx.WorkspaceID(), actor.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.messages(id,workspace_id,body,thread_id,client_id) VALUES($1,$2,$3,$4,$5)`, id, tx.WorkspaceID(), input.Body, input.ThreadID, input.ClientID); err != nil {
			return err
		}
		if err := addLink(ctx, tx, id, channelID, "belongs_to"); err != nil {
			return err
		}
		if err := references(ctx, tx, actor, id, input.Body, input.MentionIDs); err != nil {
			return err
		}
		result, err = message(ctx, tx, id)
		if err != nil {
			return err
		}
		return events.Write(ctx, tx, id, session.User.ID, "message.created", events.Payload{Version: 1, After: map[string]interface{}{"channel_id": channelID, "thread_id": input.ThreadID, "body_bytes": len(input.Body)}})
	})
	return result, err
}
func (s *Store) Change(ctx context.Context, session auth.Session, id uuid.UUID, version int64, body string, mentions []uuid.UUID, remove bool) (Message, error) {
	var result Message
	if version < 1 || (!remove && !validBody(body, mentions)) {
		return result, auth.ErrInvalid
	}
	action := policy.MessageEdit
	if remove {
		action = policy.MessageDelete
	}
	err := s.Auth.InTenant(ctx, session, policy.ChannelRead, "", func(tx database.TenantTx, actor policy.Actor) error {
		old, err := message(ctx, tx, id)
		if err != nil {
			return err
		}
		if !policy.Can(ctx, actor, action, policy.Resource{WorkspaceID: tx.WorkspaceID().String(), OwnerID: old.AuthorID.String()}) {
			return auth.ErrForbidden
		}
		if old.Deleted {
			return auth.ErrNotFound
		}
		if old.Version != version {
			return auth.ErrConflict
		}
		if remove {
			body = ""
		}
		tag, err := tx.Exec(ctx, `UPDATE app.messages SET body=$3,version=version+1 WHERE workspace_id=$1 AND id=$2 AND version=$4`, tx.WorkspaceID(), id, body, version)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return auth.ErrConflict
		}
		if _, err := tx.Exec(ctx, `UPDATE app.objects SET updated_at=now(),deleted_at=CASE WHEN $3 THEN now() ELSE deleted_at END WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id, remove); err != nil {
			return err
		}
		if remove {
			mentions = nil
		}
		if err := references(ctx, tx, actor, id, body, mentions); err != nil {
			return err
		}
		result, err = message(ctx, tx, id)
		if err != nil {
			return err
		}
		kind := "message.updated"
		if remove {
			kind = "message.deleted"
		}
		return events.Write(ctx, tx, id, session.User.ID, kind, events.Payload{Version: version + 1, Before: map[string]interface{}{"version": version, "body_bytes": len(old.Body)}, After: map[string]interface{}{"channel_id": old.ChannelID, "thread_id": old.ThreadID, "body_bytes": len(body), "deleted": remove}})
	})
	return result, err
}
func (s *Store) PresenceMembers(ctx context.Context, session auth.Session, channelID uuid.UUID, online, typing map[uuid.UUID]bool) ([]OnlineMember, error) {
	result := []OnlineMember{}
	ids := make([]uuid.UUID, 0, len(online))
	for id := range online {
		ids = append(ids, id)
	}
	err := s.WithChannel(ctx, session, channelID, policy.PresenceRead, func(tx database.TenantTx, _ policy.Actor) error {
		rows, err := tx.Query(ctx, `SELECT m.user_id,m.display_name FROM app.members m JOIN app.objects o ON o.id=m.id AND o.workspace_id=m.workspace_id WHERE m.workspace_id=$1 AND m.user_id=ANY($2::uuid[]) AND o.deleted_at IS NULL ORDER BY m.display_name,m.user_id LIMIT 100`, tx.WorkspaceID(), ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item OnlineMember
			if err := rows.Scan(&item.UserID, &item.Name); err != nil {
				return err
			}
			item.Typing = typing[item.UserID]
			result = append(result, item)
		}
		return rows.Err()
	})
	return result, err
}
