package meetings

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

type Segment struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}
type Recording struct {
	File       io.Reader
	MediaType  string
	DurationMS int64
}
type Transcriber interface {
	Transcribe(context.Context, Recording) ([]Segment, error)
}
type MockTranscriber struct{ Segments []Segment }

func (m MockTranscriber) Transcribe(ctx context.Context, in Recording) ([]Segment, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if m.Segments != nil {
		return append([]Segment{}, m.Segments...), nil
	}
	return []Segment{{StartMS: 0, EndMS: in.DurationMS, Speaker: "Demo speaker", Text: "Demo transcript fixture. Decision: use the stable API. Action: prepare the release checklist."}}, nil
}
func validateSegments(segments []Segment, duration int64) error {
	if len(segments) > 2000 {
		return ErrMedia
	}
	previous := int64(-1)
	for _, s := range segments {
		if s.StartMS < 0 || s.StartMS < previous || s.EndMS <= s.StartMS || s.EndMS > duration || !validText(s.Speaker, 80) || !validText(s.Text, 4000) || !utf8.ValidString(s.Text) || !utf8.ValidString(s.Speaker) {
			return ErrMedia
		}
		previous = s.StartMS
	}
	return nil
}

var ErrTranscriber = errors.New("transcription unavailable")

type OpenAITranscriber struct {
	Key, Model, Format string
	Client             *http.Client
}

func TranscriberFromEnv() (Transcriber, error) {
	switch os.Getenv("TRANSCRIPTION_PROVIDER") {
	case "", "disabled":
		return nil, nil
	case "mock":
		return MockTranscriber{}, nil
	case "openai":
		key, model, format := os.Getenv("OPENAI_API_KEY"), os.Getenv("TRANSCRIPTION_MODEL"), os.Getenv("TRANSCRIPTION_FORMAT")
		if key == "" || model == "" || (format != "json" && format != "diarized_json" && format != "verbose_json") {
			return nil, ErrTranscriber
		}
		return OpenAITranscriber{Key: key, Model: model, Format: format}, nil
	}
	return nil, ErrTranscriber
}
func (p OpenAITranscriber) Transcribe(ctx context.Context, in Recording) ([]Segment, error) {
	if p.Key == "" || p.Model == "" || strings.ContainsAny(p.Key, "\r\n") {
		return nil, ErrTranscriber
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, e := writer.CreateFormFile("file", "recording"+Extension(in.MediaType))
	if e != nil {
		return nil, e
	}
	n, e := io.Copy(part, io.LimitReader(in.File, MaxBytes+1))
	if e != nil || n > MaxBytes {
		return nil, ErrMedia
	}
	for key, value := range map[string]string{"model": p.Model, "response_format": p.Format} {
		if e = writer.WriteField(key, value); e != nil {
			return nil, e
		}
	}
	if p.Format == "diarized_json" {
		if e = writer.WriteField("chunking_strategy", "auto"); e != nil {
			return nil, e
		}
	}
	if p.Format == "verbose_json" {
		if e = writer.WriteField("timestamp_granularities[]", "segment"); e != nil {
			return nil, e
		}
	}
	if e = writer.Close(); e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/audio/transcriptions", &body)
	if e != nil {
		return nil, ErrTranscriber
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, e := client.Do(req)
	if e != nil {
		return nil, ErrTranscriber
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != 200 {
		return nil, ErrTranscriber
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, 8<<20+1))
	if e != nil || len(data) > 8<<20 {
		return nil, ErrTranscriber
	}
	var result struct {
		Text     string `json:"text"`
		Segments []struct {
			Start   float64 `json:"start"`
			End     float64 `json:"end"`
			Speaker string  `json:"speaker"`
			Text    string  `json:"text"`
		} `json:"segments"`
	}
	if json.Unmarshal(data, &result) != nil {
		return nil, ErrTranscriber
	}
	out := []Segment{}
	for _, s := range result.Segments {
		if math.IsNaN(s.Start) || math.IsNaN(s.End) || s.Start < 0 || s.End > float64(in.DurationMS)/1000+0.25 {
			return nil, ErrMedia
		}
		speaker := s.Speaker
		if speaker == "" {
			speaker = "Unknown speaker"
		}
		out = append(out, Segment{StartMS: int64(math.Round(s.Start * 1000)), EndMS: min(in.DurationMS, int64(math.Round(s.End*1000))), Speaker: speaker, Text: s.Text})
	}
	if len(out) == 0 && strings.TrimSpace(result.Text) != "" {
		out = append(out, Segment{StartMS: 0, EndMS: in.DurationMS, Speaker: "Unknown speaker", Text: result.Text})
	}
	normalized := []Segment{}
	for _, s := range out {
		if !utf8.ValidString(s.Text) || strings.ContainsRune(s.Text, 0) {
			return nil, ErrMedia
		}
		for len(s.Text) > 0 {
			n := min(len(s.Text), 4000)
			for !utf8.ValidString(s.Text[:n]) {
				n--
			}
			piece := s
			piece.Text = strings.TrimSpace(s.Text[:n])
			s.Text = s.Text[n:]
			if piece.Text != "" {
				normalized = append(normalized, piece)
			}
			if len(normalized) > 2000 {
				return nil, ErrMedia
			}
		}
	}
	out = normalized
	if e := validateSegments(out, in.DurationMS); e != nil {
		return nil, e
	}
	return out, nil
}
