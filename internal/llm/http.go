package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/slipgraph/slipgraph/pkg/safehttp"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type remote struct {
	vendor, key, model string
	client             *http.Client
}

func NewOpenAI(key, model string) Provider    { return newRemote("openai", key, model) }
func NewAnthropic(key, model string) Provider { return newRemote("anthropic", key, model) }
func newRemote(vendor, key, model string) *remote {
	return &remote{vendor: vendor, key: key, model: model, client: safehttp.NewClient(45*time.Second, false)}
}

// FromEnv is disabled unless explicitly configured; keys belong in worker-only secrets.
func FromEnv() (Provider, error) {
	vendor := os.Getenv("LLM_PROVIDER")
	if vendor == "" || vendor == "disabled" {
		return nil, nil
	}
	if vendor == "mock" {
		return Demo{}, nil
	}
	model := os.Getenv("LLM_MODEL")
	if model == "" {
		return nil, ErrInvalid
	}
	switch vendor {
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, ErrInvalid
		}
		return NewOpenAI(key, model), nil
	case "anthropic":
		key := os.Getenv("ANTHROPIC_API_KEY")
		if key == "" {
			return nil, ErrInvalid
		}
		return NewAnthropic(key, model), nil
	}
	return nil, ErrInvalid
}
func (p *remote) Complete(ctx context.Context, r Request) (Response, error) {
	if p.key == "" || p.model == "" || strings.ContainsAny(p.key, "\r\n") {
		return Response{}, ErrUnavailable
	}
	schema := r.Schema
	var schemaErr error
	switch p.vendor {
	case "anthropic":
		schema, schemaErr = anthropicSchema(schema)
	case "openai":
		schema, schemaErr = openaiSchema(schema)
	}
	if schemaErr != nil {
		return Response{}, schemaErr
	}

	var body interface{}
	endpoint := "https://api.openai.com/v1/responses"
	if p.vendor == "openai" {
		body = map[string]interface{}{"model": p.model, "store": false, "instructions": r.Prompt.System, "input": []interface{}{map[string]string{"role": "user", "content": r.Prompt.Data}}, "max_output_tokens": r.MaxTokens, "text": map[string]interface{}{"format": map[string]interface{}{"type": "json_schema", "name": "proposal", "strict": true, "schema": schema}}}
	} else {
		endpoint = "https://api.anthropic.com/v1/messages"
		body = map[string]interface{}{"model": p.model, "max_tokens": r.MaxTokens, "system": r.Prompt.System, "messages": []interface{}{map[string]string{"role": "user", "content": r.Prompt.Data}}, "output_config": map[string]interface{}{"format": map[string]interface{}{"type": "json_schema", "schema": schema}}}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Response{}, ErrInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return Response{}, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	if p.vendor == "openai" {
		req.Header.Set("Authorization", "Bearer "+p.key)
	} else {
		req.Header.Set("x-api-key", p.key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	res, err := p.client.Do(req)
	if err != nil {
		return Response{}, ErrUnavailable
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == 429 || res.StatusCode >= 500 {
		return Response{}, Retryable{}
	}
	if res.StatusCode != 200 {
		return Response{}, ErrUnavailable
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, 131073))
	if err != nil || len(data) > 131072 {
		return Response{}, ErrInvalid
	}
	var envelope struct {
		Status  string `json:"status"`
		Stop    string `json:"stop_reason"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage struct {
			Input       int64 `json:"input_tokens"`
			Output      int64 `json:"output_tokens"`
			CacheRead   int64 `json:"cache_read_input_tokens"`
			CacheCreate int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(data, &envelope) != nil {
		return Response{}, ErrInvalid
	}
	result := Response{InputTokens: envelope.Usage.Input + envelope.Usage.CacheRead + envelope.Usage.CacheCreate, OutputTokens: envelope.Usage.Output}
	count := 0
	if p.vendor == "openai" {
		if envelope.Status != "completed" {
			return result, ErrInvalid
		}
		for _, item := range envelope.Output {
			if item.Type == "reasoning" {
				continue
			}
			if item.Type != "message" {
				return result, ErrInvalid
			}
			for _, part := range item.Content {
				if part.Type != "output_text" {
					return result, ErrInvalid
				}
				result.JSON = json.RawMessage(part.Text)
				count++
			}
		}
	} else {
		if envelope.Stop != "end_turn" {
			return result, ErrInvalid
		}
		for _, part := range envelope.Content {
			if part.Type != "text" {
				return result, ErrInvalid
			}
			result.JSON = json.RawMessage(part.Text)
			count++
		}
	}
	if count != 1 {
		return result, ErrInvalid
	}
	return result, nil
}

// Anthropic accepts a subset of JSON Schema. Keep the original schema for local
// validation; describe unsupported bounds in the provider-facing copy only.
func anthropicSchema(raw json.RawMessage) (json.RawMessage, error) {
	return providerSchema(raw, []string{"minimum", "maximum", "minLength", "maxLength", "maxItems"})
}

// OpenAI documents pattern/format rather than string length bounds for its
// structured schema subset; the complete original schema is still enforced locally.
func openaiSchema(raw json.RawMessage) (json.RawMessage, error) {
	return providerSchema(raw, []string{"minLength", "maxLength"})
}
func providerSchema(raw json.RawMessage, unsupported []string) (json.RawMessage, error) {
	var schema interface{}
	if json.Unmarshal(raw, &schema) != nil {
		return nil, ErrInvalid
	}
	var visit func(interface{})
	visit = func(value interface{}) {
		switch v := value.(type) {
		case map[string]interface{}:
			for _, key := range unsupported {
				if constraint, ok := v[key]; ok {
					b, _ := json.Marshal(map[string]interface{}{key: constraint})
					description, _ := v["description"].(string)
					v["description"] = description + " " + string(b)
					delete(v, key)
				}
			}
			for _, child := range v {
				visit(child)
			}
		case []interface{}:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(schema)
	out, err := json.Marshal(schema)
	return out, err
}
