package llm

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Demo is a deterministic, offline fixture provider, explicitly selected as mock.
// It has no domain imports, tools, network, or write capability.
type Demo struct{}

var slipPattern = regexp.MustCompile(`(?i)([a-z][a-z ]{0,100}) will slip ([0-9]{1,3}) days`)
var datePattern = regexp.MustCompile(`[0-9]{4}-[0-9]{2}-[0-9]{2}`)

func (Demo) Complete(ctx context.Context, r Request) (Response, error) {
	if ctx.Err() != nil {
		return Response{}, ctx.Err()
	}
	var out interface{}
	if strings.Contains(string(r.Schema), "\"claims\"") && strings.Contains(string(r.Schema), "object_ids") {
		raw := strings.TrimSuffix(strings.TrimPrefix(r.Prompt.Data, "<UNTRUSTED_DATA>\n"), "\n</UNTRUSTED_DATA>")
		var blocks []string
		if json.Unmarshal([]byte(raw), &blocks) != nil {
			return Response{}, ErrInvalid
		}
		claims := []interface{}{}
		for _, block := range blocks[1:] {
			var h struct {
				ObjectID string `json:"object_id"`
				Snippet  string `json:"snippet"`
			}
			if json.Unmarshal([]byte(block), &h) == nil && h.ObjectID != "" {
				claims = append(claims, map[string]interface{}{"text": "Demo retrieved excerpt: " + h.Snippet, "object_ids": []string{h.ObjectID}})
				break
			}
		}
		out = map[string]interface{}{"claims": claims}
	} else if strings.Contains(string(r.Schema), "is_delay") {
		// Decode escaped data so evidence is an exact quote, not prompt syntax.
		raw := strings.TrimSuffix(strings.TrimPrefix(r.Prompt.Data, "<UNTRUSTED_DATA>\n"), "\n</UNTRUSTED_DATA>")
		var content []string
		if json.Unmarshal([]byte(raw), &content) != nil {
			return Response{}, ErrInvalid
		}
		text := strings.Join(content, " ")
		matches := slipPattern.FindStringSubmatch(text)
		days := 0
		quote := ""
		names := []string{}
		delay := false
		confidence := 0.99
		if len(matches) > 0 && !strings.Contains(strings.ToLower(text), "ignore instructions") && !strings.Contains(strings.ToLower(text), "delete all tasks") {
			days, _ = strconv.Atoi(matches[2])
			quote = matches[0]
			names = append(names, strings.TrimSpace(matches[1]))
			delay = true
			if strings.Contains(strings.ToLower(text), "maybe") {
				confidence = 0.4
				names = []string{"backend API"}
			}
		}
		out = map[string]interface{}{"is_delay": delay, "task_candidates": names, "days": days, "confidence": confidence, "evidence_quote": quote}
	} else if strings.Contains(string(r.Schema), "decisions") && strings.Contains(string(r.Schema), "segment_id") {
		raw := strings.TrimSuffix(strings.TrimPrefix(r.Prompt.Data, "<UNTRUSTED_DATA>\n"), "\n</UNTRUSTED_DATA>")
		var blocks []string
		if json.Unmarshal([]byte(raw), &blocks) != nil {
			return Response{}, ErrInvalid
		}
		decisions := []interface{}{}
		actions := []interface{}{}
		for _, block := range blocks {
			_, data, ok := strings.Cut(block, " ")
			if !ok {
				return Response{}, ErrInvalid
			}
			var segment struct {
				ID    string    `json:"segment_id"`
				Text  string    `json:"text"`
				Start time.Time `json:"project_start"`
			}
			if json.Unmarshal([]byte(data), &segment) != nil {
				return Response{}, ErrInvalid
			}
			if strings.Contains(strings.ToLower(segment.Text), "ignore instructions") {
				continue
			}
			if strings.Contains(segment.Text, "Decision: use the stable API.") {
				decisions = append(decisions, map[string]interface{}{"segment_id": segment.ID, "quote": "Decision: use the stable API.", "title": "Use the stable API", "details": "Decision from the meeting."})
			}
			if strings.Contains(segment.Text, "Action: prepare the release checklist.") {
				actions = append(actions, map[string]interface{}{"segment_id": segment.ID, "quote": "Action: prepare the release checklist.", "title": "Prepare the release checklist", "description": "Action from the meeting.", "start": segment.Start, "due": segment.Start.Add(24 * time.Hour), "estimate_minutes": 60, "milestone": false})
			}
			if len(decisions)+len(actions) > 10 {
				break
			}
		}
		out = map[string]interface{}{"decisions": decisions, "actions": actions}
	} else if strings.Contains(string(r.Schema), "task_key") {
		start := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		if date := datePattern.FindString(r.Prompt.Data); date != "" {
			if parsed, e := time.Parse("2006-01-02", date); e == nil {
				start = parsed
			}
		}
		tasks := []interface{}{}
		for i, title := range []string{"Backend API", "Frontend integration", "Launch"} {
			milestone := i == 2
			estimate := 1440
			if milestone {
				estimate = 0
			}
			tasks = append(tasks, map[string]interface{}{"key": []string{"backend", "frontend", "launch"}[i], "title": title, "description": "Offline demo fixture; review before approval.", "start": start.Add(time.Duration(i) * 24 * time.Hour), "due": start.Add(time.Duration(min(i+1, 2)) * 24 * time.Hour), "estimate_minutes": estimate, "milestone": milestone})
		}
		out = map[string]interface{}{"name": "Demo launch plan", "description": "Deterministic mock plan from a brief. Review all details.", "start": start, "launch_due": start.Add(48 * time.Hour), "tasks": tasks, "dependencies": []interface{}{map[string]string{"task_key": "frontend", "blocker_key": "backend"}, map[string]string{"task_key": "launch", "blocker_key": "frontend"}}}
	} else {
		return Response{}, ErrInvalid
	}
	b, e := json.Marshal(out)
	return Response{JSON: b, InputTokens: 200, OutputTokens: 600}, e
}
