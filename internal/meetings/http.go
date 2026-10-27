package meetings

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/filestore"
	"github.com/slipgraph/slipgraph/internal/platform/contract"
	"github.com/slipgraph/slipgraph/internal/policy"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Root         string
	DailyMinutes int
}

func LoadConfig() (Config, error) {
	c := Config{Root: os.Getenv("MEDIA_ROOT"), DailyMinutes: 600}
	if c.Root == "" {
		c.Root = filepath.Join(os.TempDir(), "slipgraph-media")
	}
	if !filepath.IsAbs(c.Root) {
		return c, auth.ErrInvalid
	}
	if raw := os.Getenv("MEETING_DAILY_MINUTES"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100000 {
			return c, auth.ErrInvalid
		}
		c.DailyMinutes = n
	}
	return c, nil
}
func DefaultStore(identity *auth.Service) *Store {
	c, e := LoadConfig()
	if e != nil {
		return &Store{Auth: identity}
	}
	return &Store{Auth: identity, Files: filestore.Local{Root: c.Root}, DailyMinutes: c.DailyMinutes}
}

type HTTP struct {
	Store    *Store
	Identity *auth.HTTP
	Uploads  chan struct{}
}

func enabled() bool {
	t := os.Getenv("TRANSCRIPTION_PROVIDER")
	l := os.Getenv("LLM_PROVIDER")
	return (t == "mock" || t == "openai") && (l == "mock" || l == "openai" || l == "anthropic")
}
func (h *HTTP) GetMeetingSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.Identity.Require(w, r, policy.MeetingRead); !ok {
		return
	}
	auth.JSON(w, 200, map[string]interface{}{"enabled": enabled(), "mock": os.Getenv("TRANSCRIPTION_PROVIDER") == "mock", "max_bytes": MaxBytes, "max_minutes": min(MaxMinutes, h.Store.DailyMinutes), "daily_minutes": h.Store.DailyMinutes})
}
func (h *HTTP) UploadMeeting(w http.ResponseWriter, r *http.Request) {
	session, ok := h.Identity.Require(w, r, policy.MeetingWrite)
	if !ok {
		return
	}
	if !enabled() || h.Store.Files == nil {
		auth.JSON(w, 503, map[string]string{"error": "Meeting providers are disabled."})
		return
	}
	if h.Uploads != nil {
		select {
		case h.Uploads <- struct{}{}:
			defer func() { <-h.Uploads }()
		default:
			auth.Error(w, auth.ErrLimited)
			return
		}
	}
	if e := h.Store.Auth.Rate(r.Context(), "meeting:upload:"+session.WorkspaceID.String(), session.User.ID.String(), 10, time.Hour); e != nil {
		auth.Error(w, e)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxBytes+16384)
	reader, e := r.MultipartReader()
	if e != nil {
		auth.Error(w, auth.ErrInvalid)
		return
	}
	id, e := uuid.NewV7()
	if e != nil {
		auth.Error(w, e)
		return
	}
	var key, mediaType string
	var size int64
	fields := map[string]string{}
	success := false
	defer func() {
		if !success && key != "" {
			_ = h.Store.Files.Delete(context.WithoutCancel(r.Context()), session.WorkspaceID, key)
		}
	}()
	for count := 0; ; count++ {
		part, e := reader.NextPart()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			auth.JSON(w, 413, map[string]string{"error": "Invalid or oversized upload."})
			return
		}
		if count >= 3 {
			_ = part.Close()
			auth.Error(w, auth.ErrInvalid)
			return
		}
		name := part.FormName()
		if name == "file" {
			if key != "" {
				_ = part.Close()
				auth.Error(w, auth.ErrInvalid)
				return
			}
			mediaType, _, e = mime.ParseMediaType(part.Header.Get("Content-Type"))
			if mediaType == "audio/x-wav" {
				mediaType = "audio/wav"
			}
			if mediaType == "audio/mp3" {
				mediaType = "audio/mpeg"
			}
			if e != nil || Extension(mediaType) == "" {
				_ = part.Close()
				auth.JSON(w, 415, map[string]string{"error": "Only WAV, MP3, MP4 and WebM recordings are accepted."})
				return
			}
			header := make([]byte, 512)
			n, readErr := io.ReadFull(part, header)
			if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
				_ = part.Close()
				auth.JSON(w, 413, map[string]string{"error": "Invalid or oversized upload."})
				return
			}
			if Magic(header[:n], mediaType) != nil {
				_ = part.Close()
				auth.JSON(w, 415, map[string]string{"error": "File content does not match an allowed recording type."})
				return
			}
			key, size, e = h.Store.WriteUpload(r.Context(), session, id, io.MultiReader(strings.NewReader(string(header[:n])), part))
			_ = part.Close()
			if e != nil {
				auth.JSON(w, 413, map[string]string{"error": "Recording exceeds the size limit or could not be stored."})
				return
			}
		} else {
			_, duplicate := fields[name]
			if (name != "title" && name != "project_id") || duplicate {
				_ = part.Close()
				auth.Error(w, auth.ErrInvalid)
				return
			}
			data, e := io.ReadAll(io.LimitReader(part, 1025))
			_ = part.Close()
			if e != nil || len(data) > 1024 {
				auth.Error(w, auth.ErrInvalid)
				return
			}
			fields[name] = string(data)
		}
	}
	project, e := uuid.Parse(fields["project_id"])
	if e != nil || key == "" {
		auth.Error(w, auth.ErrInvalid)
		return
	}
	meeting, e := h.Store.Accept(r.Context(), session, id, project, fields["title"], mediaType, key, size)
	if errors.Is(e, auth.ErrLimited) {
		auth.JSON(w, 429, map[string]string{"error": "Workspace transcription minute budget is exhausted."})
		return
	}
	if e != nil {
		auth.Error(w, e)
		return
	}
	success = true
	auth.JSON(w, 200, meeting)
}
func (h *HTTP) ListMeetings(w http.ResponseWriter, r *http.Request) {
	s, ok := h.Identity.Require(w, r, policy.MeetingRead)
	if !ok {
		return
	}
	v, e := h.Store.List(r.Context(), s)
	if e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 200, v)
}
func (h *HTTP) GetMeeting(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s, ok := h.Identity.Require(w, r, policy.MeetingRead)
	if !ok {
		return
	}
	v, e := h.Store.Get(r.Context(), s, id)
	if e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 200, v)
}
func (h *HTTP) GetMeetingTranscript(w http.ResponseWriter, r *http.Request, id uuid.UUID, p contract.GetMeetingTranscriptParams) {
	s, ok := h.Identity.Require(w, r, policy.MeetingRead)
	if !ok {
		return
	}
	q := ""
	after := -1
	if p.Q != nil {
		q = *p.Q
	}
	if p.After != nil {
		after = *p.After
	}
	v, e := h.Store.Transcript(r.Context(), s, id, q, after)
	if e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 200, v)
}
func (h *HTTP) GetTranscriptSegment(w http.ResponseWriter, r *http.Request, meeting, id uuid.UUID) {
	s, ok := h.Identity.Require(w, r, policy.MeetingRead)
	if !ok {
		return
	}
	v, e := h.Store.Segment(r.Context(), s, meeting, id)
	if e != nil {
		auth.Error(w, e)
		return
	}
	auth.JSON(w, 200, v)
}
func (h *HTTP) GetMeetingMedia(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s, ok := h.Identity.Require(w, r, policy.MeetingRead)
	if !ok {
		return
	}
	m, f, e := h.Store.Media(r.Context(), s, id)
	if e != nil {
		auth.Error(w, e)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="recording`+Extension(m.MediaType)+`"`)
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, "recording"+Extension(m.MediaType), m.CreatedAt, f)
}
