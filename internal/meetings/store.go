package meetings

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/filestore"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/tasks"
	"io"
	"strings"
	"time"
)

type Store struct {
	Auth         *auth.Service
	Files        filestore.Store
	DailyMinutes int
}
type Meeting struct {
	ID               uuid.UUID `json:"id"`
	ProjectID        uuid.UUID `json:"project_id"`
	Title            string    `json:"title"`
	MediaType        string    `json:"media_type"`
	SizeBytes        int64     `json:"size_bytes"`
	Status           string    `json:"status"`
	DurationMS       int64     `json:"duration_ms"`
	CreatedAt        time.Time `json:"created_at"`
	ExtractionStatus string    `json:"extraction_status"`
}
type TranscriptSegment struct {
	Segment
	ID      uuid.UUID `json:"id"`
	Ordinal int       `json:"ordinal"`
}
type SegmentPage struct {
	Items      []TranscriptSegment `json:"items"`
	NextCursor *int                `json:"next_cursor"`
}
type Suggestion struct {
	ID          uuid.UUID `json:"id"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	Title       string    `json:"title"`
	Version     int64     `json:"version"`
	PayloadHash string    `json:"payload_hash"`
	SegmentID   uuid.UUID `json:"segment_id"`
	StartMS     int64     `json:"start_ms"`
}
type Detail struct {
	Meeting
	Suggestions []Suggestion `json:"suggestions"`
}

const meetingSelect = `SELECT m.id,l.to_object,m.title,m.media_type,m.size_bytes,m.status,m.duration_ms,m.created_at,m.storage_key FROM app.meetings m JOIN app.objects o ON o.workspace_id=m.workspace_id AND o.id=m.id JOIN app.links l ON l.workspace_id=m.workspace_id AND l.from_object=m.id AND l.link_type='belongs_to' JOIN app.objects p ON p.workspace_id=l.workspace_id AND p.id=l.to_object WHERE m.workspace_id=$1 AND o.deleted_at IS NULL AND p.deleted_at IS NULL`

func scanMeeting(row pgx.Row) (Meeting, string, error) {
	var m Meeting
	var key string
	e := row.Scan(&m.ID, &m.ProjectID, &m.Title, &m.MediaType, &m.SizeBytes, &m.Status, &m.DurationMS, &m.CreatedAt, &key)
	if errors.Is(e, pgx.ErrNoRows) {
		e = auth.ErrNotFound
	}
	return m, key, e
}
func get(ctx context.Context, tx database.TenantTx, id uuid.UUID) (Meeting, string, error) {
	return scanMeeting(tx.QueryRow(ctx, meetingSelect+` AND m.id=$2`, tx.WorkspaceID(), id))
}
func auditLock(ctx context.Context, tx database.TenantTx) error {
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9401))`, tx.WorkspaceID().String())
	return e
}
func link(ctx context.Context, tx database.TenantTx, from, to uuid.UUID, kind string) error {
	id, e := uuid.NewV7()
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `INSERT INTO app.links(id,workspace_id,from_object,to_object,link_type) VALUES($1,$2,$3,$4,$5)`, id, tx.WorkspaceID(), from, to, kind)
	return e
}
func (s *Store) Accept(ctx context.Context, session auth.Session, id, project uuid.UUID, title, mime, key string, size int64) (Meeting, error) {
	var result Meeting
	if !validText(title, 200) || size < 1 || size > MaxBytes || Extension(mime) == "" || !strings.HasPrefix(key, session.WorkspaceID.String()+"/") || s.DailyMinutes < 1 || s.DailyMinutes > 100000 {
		return result, auth.ErrInvalid
	}
	e := s.Auth.InTenant(ctx, session, policy.MeetingWrite, "", func(tx database.TenantTx, h policy.Actor) error {
		if e := auditLock(ctx, tx); e != nil {
			return e
		}
		if _, e := tasks.ProjectTx(ctx, tx, h, project); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9407))`, tx.WorkspaceID().String()); e != nil {
			return e
		}
		var used int
		if e := tx.QueryRow(ctx, `SELECT coalesce(sum(reserved_minutes),0) FROM app.meetings WHERE workspace_id=$1 AND charged_at>clock_timestamp()-interval '24 hours'`, tx.WorkspaceID()).Scan(&used); e != nil {
			return e
		}
		if used+min(MaxMinutes, s.DailyMinutes) > s.DailyMinutes {
			return auth.ErrLimited
		}
		if _, e := tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'meeting',$3)`, id, tx.WorkspaceID(), h.ID); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO app.meetings(id,workspace_id,title,storage_key,media_type,size_bytes,status,reserved_minutes) VALUES($1,$2,$3,$4,$5,$6,'queued',$7)`, id, tx.WorkspaceID(), title, key, mime, size, min(MaxMinutes, s.DailyMinutes)); e != nil {
			return e
		}
		if e := link(ctx, tx, id, project, "belongs_to"); e != nil {
			return e
		}
		if e := events.WriteAudited(ctx, tx, id, session.User.ID, "meeting.uploaded", events.Payload{ProjectID: project.String(), After: map[string]interface{}{"status": "queued", "size_bytes": size}}); e != nil {
			return e
		}
		var e error
		result, _, e = get(ctx, tx, id)
		return e
	})
	return result, e
}
func (s *Store) List(ctx context.Context, session auth.Session) ([]Meeting, error) {
	out := []Meeting{}
	e := s.Auth.InTenant(ctx, session, policy.MeetingRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		rows, e := tx.Query(ctx, meetingSelect+` ORDER BY m.id DESC LIMIT 100`, tx.WorkspaceID())
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			m, _, e := scanMeeting(rows)
			if e != nil {
				return e
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, e
}
func (s *Store) Get(ctx context.Context, session auth.Session, id uuid.UUID) (Detail, error) {
	out := Detail{Suggestions: []Suggestion{}}
	e := s.Auth.InTenant(ctx, session, policy.MeetingRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		var e error
		out.Meeting, _, e = get(ctx, tx, id)
		if e != nil {
			return e
		}
		var pending, failed int
		if e = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE r.status IN ('queued','running')),count(*) FILTER(WHERE r.status IN ('failed','quota_exhausted')) FROM app.agent_runs r WHERE r.workspace_id=$1 AND r.kind='meeting_extraction' AND EXISTS(SELECT 1 FROM app.links src JOIN app.links parent ON parent.workspace_id=src.workspace_id AND parent.from_object=src.to_object AND parent.link_type='belongs_to' WHERE src.workspace_id=r.workspace_id AND src.from_object=r.id AND src.link_type='source' AND parent.to_object=$2)`, tx.WorkspaceID(), id).Scan(&pending, &failed); e != nil {
			return e
		}
		out.ExtractionStatus = "complete"
		if out.Status != "ready" {
			out.ExtractionStatus = "pending"
		} else if pending > 0 {
			out.ExtractionStatus = "processing"
		} else if failed > 0 {
			out.ExtractionStatus = "failed"
		}
		rows, e := tx.Query(ctx, `SELECT DISTINCT p.id,p.kind,p.status,coalesce(p.payload->'task'->>'title',p.payload->>'title'),p.version,p.payload_hash,segment.id,segment.start_ms FROM app.proposals p JOIN app.links src ON src.workspace_id=p.workspace_id AND src.from_object=p.id AND src.link_type='source' JOIN app.transcript_segments segment ON segment.workspace_id=src.workspace_id AND segment.id=src.to_object JOIN app.links parent ON parent.workspace_id=segment.workspace_id AND parent.from_object=segment.id AND parent.link_type='belongs_to' WHERE p.workspace_id=$1 AND parent.to_object=$2 AND p.kind IN ('meeting_task','record_decision') ORDER BY p.id`, tx.WorkspaceID(), id)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v Suggestion
			if e := rows.Scan(&v.ID, &v.Kind, &v.Status, &v.Title, &v.Version, &v.PayloadHash, &v.SegmentID, &v.StartMS); e != nil {
				return e
			}
			out.Suggestions = append(out.Suggestions, v)
		}
		return rows.Err()
	})
	return out, e
}
func (s *Store) Transcript(ctx context.Context, session auth.Session, id uuid.UUID, query string, after int) (SegmentPage, error) {
	out := SegmentPage{Items: []TranscriptSegment{}}
	if len(query) > 200 || after < -1 {
		return out, auth.ErrInvalid
	}
	e := s.Auth.InTenant(ctx, session, policy.MeetingRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		if _, _, e := get(ctx, tx, id); e != nil {
			return e
		}
		rows, e := tx.Query(ctx, `SELECT t.id,t.ordinal,t.start_ms,t.end_ms,t.speaker,t.text FROM app.transcript_segments t JOIN app.links l ON l.workspace_id=t.workspace_id AND l.from_object=t.id AND l.link_type='belongs_to' WHERE t.workspace_id=$1 AND l.to_object=$2 AND t.ordinal>$3 AND ($4='' OR strpos(lower(t.text),lower($4))>0 OR strpos(lower(t.speaker),lower($4))>0) ORDER BY t.ordinal LIMIT 201`, tx.WorkspaceID(), id, after, query)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v TranscriptSegment
			if e := rows.Scan(&v.ID, &v.Ordinal, &v.StartMS, &v.EndMS, &v.Speaker, &v.Text); e != nil {
				return e
			}
			out.Items = append(out.Items, v)
		}
		if e := rows.Err(); e != nil {
			return e
		}
		if len(out.Items) > 200 {
			out.Items = out.Items[:200]
			n := out.Items[199].Ordinal
			out.NextCursor = &n
		}
		return nil
	})
	return out, e
}
func (s *Store) Segment(ctx context.Context, session auth.Session, meeting, id uuid.UUID) (TranscriptSegment, error) {
	var out TranscriptSegment
	e := s.Auth.InTenant(ctx, session, policy.MeetingRead, "", func(tx database.TenantTx, _ policy.Actor) error {
		if _, _, e := get(ctx, tx, meeting); e != nil {
			return e
		}
		e := tx.QueryRow(ctx, `SELECT t.id,t.ordinal,t.start_ms,t.end_ms,t.speaker,t.text FROM app.transcript_segments t JOIN app.links l ON l.workspace_id=t.workspace_id AND l.from_object=t.id AND l.link_type='belongs_to' WHERE t.workspace_id=$1 AND l.to_object=$2 AND t.id=$3`, tx.WorkspaceID(), meeting, id).Scan(&out.ID, &out.Ordinal, &out.StartMS, &out.EndMS, &out.Speaker, &out.Text)
		if errors.Is(e, pgx.ErrNoRows) {
			return auth.ErrNotFound
		}
		return e
	})
	return out, e
}
func (s *Store) Media(ctx context.Context, session auth.Session, id uuid.UUID) (Meeting, filestore.File, error) {
	var m Meeting
	var key string
	e := s.Auth.InTenant(ctx, session, policy.MeetingRead, "", func(tx database.TenantTx, _ policy.Actor) error { var e error; m, key, e = get(ctx, tx, id); return e })
	if e != nil {
		return m, nil, e
	}
	f, e := s.Files.Open(ctx, session.WorkspaceID, key)
	return m, f, e
}

// WriteUpload streams and counts bytes; a failed database registration removes the random object.
func (s *Store) WriteUpload(ctx context.Context, session auth.Session, id uuid.UUID, r io.Reader) (string, int64, error) {
	counter := &countReader{r: io.LimitReader(r, MaxBytes+1)}
	key, e := s.Files.Put(ctx, session.WorkspaceID, id, counter)
	if e == nil && (counter.n < 1 || counter.n > MaxBytes) {
		e = ErrMedia
	}
	if e != nil && key != "" {
		_ = s.Files.Delete(context.WithoutCancel(ctx), session.WorkspaceID, key)
	}
	return key, counter.n, e
}

type countReader struct {
	r io.Reader
	n int64
}

func (r *countReader) Read(p []byte) (int, error) { n, e := r.r.Read(p); r.n += int64(n); return n, e }
