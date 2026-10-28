package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"
)

const EmbeddingDimensions = 384

// Embedder is a read-only provider boundary. Only authorized worker jobs call it.
type Embedder interface {
	Model() string
	Embed(context.Context, []string) ([][]float32, error)
}

// MockEmbedder uses normalized hashed word counts, not a semantic model.
type MockEmbedder struct{}

func (MockEmbedder) Model() string { return "mock-hash-v1-384" }
func (MockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		v := make([]float32, EmbeddingDimensions)
		for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
			h := sha256.Sum256([]byte(word))
			v[int(binary.LittleEndian.Uint32(h[:4]))%len(v)]++
		}
		var norm float64
		for _, x := range v {
			norm += float64(x * x)
		}
		if norm == 0 {
			v[0] = 1
		} else {
			for j := range v {
				v[j] /= float32(math.Sqrt(norm))
			}
		}
		out[i] = v
	}
	return out, nil
}

type OpenAIEmbedder struct {
	Key, ModelID string
	Client       *http.Client
}

func (p OpenAIEmbedder) Model() string { return p.ModelID + "-384" }
func (p OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) < 1 || len(texts) > 32 || p.Key == "" {
		return nil, ErrInvalid
	}
	for _, t := range texts {
		if len(t) < 1 || len(t) > 2400 {
			return nil, ErrInvalid
		}
	}
	body, e := json.Marshal(map[string]interface{}{"model": p.ModelID, "input": texts, "dimensions": EmbeddingDimensions, "encoding_format": "float"})
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/embeddings", bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+p.Key)
	req.Header.Set("Content-Type", "application/json")
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, e := client.Do(req)
	if e != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, ErrUnavailable
	}
	data, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil || len(data) >= 1<<20 {
		return nil, ErrInvalid
	}
	var result struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(data, &result) != nil || len(result.Data) != len(texts) {
		return nil, ErrInvalid
	}
	out := make([][]float32, len(texts))
	for _, d := range result.Data {
		if d.Index < 0 || d.Index >= len(out) || out[d.Index] != nil || !ValidVector(d.Embedding) {
			return nil, ErrInvalid
		}
		out[d.Index] = d.Embedding
	}
	return out, nil
}
func ValidVector(v []float32) bool {
	if len(v) != EmbeddingDimensions {
		return false
	}
	var norm float64
	for _, x := range v {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			return false
		}
		norm += float64(x) * float64(x)
	}
	return norm > 0
}
func EmbedderFromEnv() (Embedder, error) {
	switch os.Getenv("EMBEDDING_PROVIDER") {
	case "", "disabled":
		return nil, nil
	case "mock":
		return MockEmbedder{}, nil
	case "openai":
		model := os.Getenv("EMBEDDING_MODEL")
		if os.Getenv("OPENAI_API_KEY") == "" || (model != "text-embedding-3-small" && model != "text-embedding-3-large") {
			return nil, ErrInvalid
		}
		return OpenAIEmbedder{Key: os.Getenv("OPENAI_API_KEY"), ModelID: model}, nil
	default:
		return nil, ErrInvalid
	}
}
