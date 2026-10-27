package meetings

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/events"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
	"github.com/slipgraph/slipgraph/internal/proposals"
	"io"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"
)

type Runner struct {
	Store       *Store
	Transcriber Transcriber
	Probe       Prober
	Proposals   *proposals.Store
	Logger      *slog.Logger
}

func (r *Runner) Process(ctx context.Context, service policy.Actor, id uuid.UUID) error {
	if r.Transcriber == nil || r.Probe == nil {
		return ErrTranscriber
	}
	if !policy.Can(ctx, service, policy.MeetingTranscribe, policy.Resource{WorkspaceID: service.WorkspaceID}) {
		return auth.ErrForbidden
	}
	var user uuid.UUID
	var key string
	var m Meeting
	exhausted := false
	e := r.Store.Auth.InConsumer(ctx, service, func(tx database.TenantTx) error {
		if e := auditLock(ctx, tx); e != nil {
			return e
		}
		var status string
		if e := tx.QueryRow(ctx, `SELECT m.status,o.created_by FROM app.meetings m JOIN app.objects o ON o.workspace_id=m.workspace_id AND o.id=m.id WHERE m.workspace_id=$1 AND m.id=$2 FOR UPDATE OF m`, tx.WorkspaceID(), id).Scan(&status, &user); e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return auth.ErrNotFound
			}
			return e
		}
		if status != "queued" {
			return auth.ErrConflict
		}
		_, memberErr := r.Store.Auth.JobMember(ctx, tx, user, policy.MeetingWrite)
		var getErr error
		m, key, getErr = get(ctx, tx, id)
		if memberErr != nil || getErr != nil {
			if _, e := tx.Exec(ctx, `UPDATE app.meetings SET status='failed' WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id); e != nil {
				return e
			}
			return events.WriteWorkerAudited(ctx, tx, service, id, "meeting.failed", events.Payload{Before: map[string]string{"status": "queued"}, After: map[string]string{"status": "failed"}})
		}
		if _, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,9407))`, tx.WorkspaceID().String()); e != nil {
			return e
		}
		var used int
		if e := tx.QueryRow(ctx, `SELECT coalesce(sum(reserved_minutes),0) FROM app.meetings WHERE workspace_id=$1 AND id<>$2 AND charged_at>clock_timestamp()-interval '24 hours'`, tx.WorkspaceID(), id).Scan(&used); e != nil {
			return e
		}
		if used+min(MaxMinutes, r.Store.DailyMinutes) > r.Store.DailyMinutes {
			exhausted = true
			_, e := tx.Exec(ctx, `UPDATE app.meetings SET status='quota_exhausted' WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id)
			if e != nil {
				return e
			}
			return events.WriteWorkerAudited(ctx, tx, service, id, "meeting.quota_exhausted", events.Payload{After: map[string]string{"status": "quota_exhausted"}})
		}
		_, e := tx.Exec(ctx, `UPDATE app.meetings SET status='transcribing',charged_at=clock_timestamp(),reserved_minutes=$3,lease_until=clock_timestamp()+interval '7 minutes' WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id, min(MaxMinutes, r.Store.DailyMinutes))
		if e != nil {
			return e
		}
		m.Status = "transcribing"
		return events.WriteWorkerAudited(ctx, tx, service, id, "meeting.transcribing", events.Payload{Before: map[string]string{"status": "queued"}, After: map[string]string{"status": "transcribing"}})
	})
	if e != nil {
		return e
	}
	if exhausted {
		return auth.ErrLimited
	}
	if m.Status != "transcribing" {
		return auth.ErrForbidden
	}
	work, cancel := context.WithTimeout(ctx, 6*time.Minute)
	defer cancel()
	workspace, e := uuid.Parse(service.WorkspaceID)
	if e != nil {
		return e
	}
	var segments []Segment
	var duration int64
	file, runErr := r.Store.Files.Open(work, workspace, key)
	if runErr == nil {
		header := make([]byte, 512)
		n, readErr := io.ReadFull(file, header)
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
			runErr = readErr
		} else {
			runErr = Magic(header[:n], m.MediaType)
		}
		if runErr == nil {
			_, runErr = file.Seek(0, io.SeekStart)
		}
		if runErr == nil {
			duration, runErr = r.Probe.Duration(work, file)
		}
		if runErr == nil && (duration < 1 || duration > int64(min(MaxMinutes, r.Store.DailyMinutes))*60000) {
			runErr = ErrMedia
		}
		if runErr == nil {
			runErr = r.Store.Auth.InConsumer(work, service, func(tx database.TenantTx) error {
				if _, e := r.Store.Auth.JobMember(work, tx, user, policy.MeetingWrite); e != nil {
					return e
				}
				_, e := tx.Exec(work, `UPDATE app.meetings SET duration_ms=$3,reserved_minutes=$4 WHERE workspace_id=$1 AND id=$2 AND status='transcribing'`, tx.WorkspaceID(), id, duration, (duration+59999)/60000)
				return e
			})
		}
		if runErr == nil {
			_, runErr = file.Seek(0, io.SeekStart)
		}
		if runErr == nil {
			segments, runErr = r.Transcriber.Transcribe(work, Recording{File: file, MediaType: m.MediaType, DurationMS: duration})
		}
		_ = file.Close()
	}
	if runErr == nil && work.Err() != nil {
		runErr = work.Err()
	}
	if runErr == nil {
		runErr = validateSegments(segments, duration)
	}
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	e = r.Store.Auth.InConsumer(finish, service, func(tx database.TenantTx) error {
		if e := auditLock(finish, tx); e != nil {
			return e
		}
		var status string
		if e := tx.QueryRow(finish, `SELECT status FROM app.meetings WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, tx.WorkspaceID(), id).Scan(&status); e != nil {
			return e
		}
		if status != "transcribing" {
			return auth.ErrConflict
		}
		human, memberErr := r.Store.Auth.JobMember(finish, tx, user, policy.MeetingWrite)
		if memberErr != nil {
			runErr = memberErr
		}
		if _, _, e := get(finish, tx, id); e != nil {
			runErr = e
		}
		if runErr == nil {
			if _, e := tx.Exec(finish, "SAVEPOINT transcript_results"); e != nil {
				return e
			}
			runErr = r.persist(finish, tx, service, human, id, segments)
			if runErr != nil {
				if _, e := tx.Exec(finish, "ROLLBACK TO SAVEPOINT transcript_results"); e != nil {
					return e
				}
			} else {
				if _, e := tx.Exec(finish, "RELEASE SAVEPOINT transcript_results"); e != nil {
					return e
				}
			}
		}
		status = "ready"
		event := "meeting.transcribed"
		if runErr != nil {
			status = "failed"
			event = "meeting.failed"
		}
		if _, e := tx.Exec(finish, `UPDATE app.meetings SET status=$3,lease_until=NULL WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id, status); e != nil {
			return e
		}
		return events.WriteWorkerAudited(finish, tx, service, id, event, events.Payload{Before: map[string]string{"status": "transcribing"}, After: map[string]interface{}{"status": status, "duration_ms": duration}})
	})
	if e != nil {
		return e
	}
	return runErr
}
func (r *Runner) persist(ctx context.Context, tx database.TenantTx, service, human policy.Actor, meeting uuid.UUID, segments []Segment) error {
	ids := []uuid.UUID{}
	ordinal := 0
	for _, segment := range segments {
		// Keep source blocks bounded while preserving the provider's time interval.
		text := segment.Text
		for len(text) > 0 {
			n := min(len(text), 1000)
			for !utf8.ValidString(text[:n]) {
				n--
			}
			piece := strings.TrimSpace(text[:n])
			text = text[n:]
			if piece == "" {
				continue
			}
			if ordinal >= 2000 {
				return ErrMedia
			}
			id, e := uuid.NewV7()
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO app.objects(id,workspace_id,type,created_by) VALUES($1,$2,'transcript_segment',$3)`, id, tx.WorkspaceID(), human.ID); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO app.transcript_segments(id,workspace_id,ordinal,start_ms,end_ms,speaker,text) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, tx.WorkspaceID(), ordinal, segment.StartMS, segment.EndMS, segment.Speaker, piece); e != nil {
				return e
			}
			if e = link(ctx, tx, id, meeting, "belongs_to"); e != nil {
				return e
			}
			ids = append(ids, id)
			ordinal++
		}
	}
	return r.Proposals.QueueMeetingExtraction(ctx, tx, service, human, ids)
}
func (r *Runner) Tick(ctx context.Context) error {
	actors, e := r.Store.Auth.ConsumerSessions(ctx)
	if e != nil {
		return e
	}
	var failures []error
	for _, a := range actors {
		var ids []uuid.UUID
		e := r.Store.Auth.InConsumer(ctx, a, func(tx database.TenantTx) error {
			if !policy.Can(ctx, a, policy.MeetingTranscribe, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
				return auth.ErrForbidden
			}
			if e := auditLock(ctx, tx); e != nil {
				return e
			}
			rows, e := tx.Query(ctx, `UPDATE app.meetings SET status='failed',lease_until=NULL WHERE workspace_id=$1 AND status='transcribing' AND lease_until<clock_timestamp() RETURNING id`, tx.WorkspaceID())
			if e != nil {
				return e
			}
			stale := []uuid.UUID{}
			for rows.Next() {
				var id uuid.UUID
				if e := rows.Scan(&id); e != nil {
					rows.Close()
					return e
				}
				stale = append(stale, id)
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return e
			}
			for _, id := range stale {
				if e := events.WriteWorkerAudited(ctx, tx, a, id, "meeting.failed", events.Payload{Before: map[string]string{"status": "transcribing"}, After: map[string]string{"status": "failed", "reason": "lease_expired"}}); e != nil {
					return e
				}
			}
			if r.Transcriber == nil {
				return nil
			}
			rows, e = tx.Query(ctx, `SELECT id FROM app.meetings WHERE workspace_id=$1 AND status='queued' ORDER BY id LIMIT 2`, tx.WorkspaceID())
			if e != nil {
				return e
			}
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				if e := rows.Scan(&id); e != nil {
					return e
				}
				ids = append(ids, id)
			}
			return rows.Err()
		})
		if e != nil {
			failures = append(failures, e)
			continue
		}
		for _, id := range ids {
			if e := r.Process(ctx, a, id); e != nil && r.Logger != nil && ctx.Err() == nil {
				r.Logger.WarnContext(ctx, "consumer failed", "error", e)
			}
		}
	}
	return errors.Join(failures...)
}
func (r *Runner) Run(ctx context.Context) {
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		if e := r.Tick(ctx); e != nil && r.Logger != nil && ctx.Err() == nil {
			r.Logger.ErrorContext(ctx, "consumer failed", "error", e)
		}
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
