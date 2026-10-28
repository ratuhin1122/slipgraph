package llm

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestEmbeddingMockDeterminismAndBounds(t *testing.T) {
	p := MockEmbedder{}
	a, e := p.Embed(context.Background(), []string{"launch API"})
	if e != nil {
		t.Fatal(e)
	}
	b, e := p.Embed(context.Background(), []string{"API launch"})
	if e != nil || !reflect.DeepEqual(a, b) {
		t.Fatal("unstable mock")
	}
	if !ValidVector(a[0]) || ValidVector(make([]float32, 384)) || ValidVector([]float32{1}) {
		t.Fatal("vector validation")
	}
	a[0][0] = float32(math.Inf(1))
	if ValidVector(a[0]) {
		t.Fatal("nonfinite vector accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = p.Embed(ctx, []string{"x"}); e == nil {
		t.Fatal("ignored cancellation")
	}
}
func TestOpenAIEmbeddingContract(t *testing.T) {
	vector := make([]float32, 384)
	vector[0] = 1
	p := OpenAIEmbedder{Key: "fixture-key-not-a-secret", ModelID: "text-embedding-3-small", Client: &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != "https://api.openai.com/v1/embeddings" {
			t.Fatal("untrusted endpoint")
		}
		var body struct {
			Input      []string `json:"input"`
			Model      string   `json:"model"`
			Dimensions int      `json:"dimensions"`
			Format     string   `json:"encoding_format"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model != "text-embedding-3-small" || body.Dimensions != 384 || body.Format != "float" || len(body.Input) != 1 {
			t.Fatal("bad provider contract")
		}
		data, _ := json.Marshal(map[string]interface{}{"data": []interface{}{map[string]interface{}{"index": 0, "embedding": vector}}})
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
	})}}
	v, e := p.Embed(context.Background(), []string{"untrusted transcript"})
	if e != nil || len(v) != 1 || !ValidVector(v[0]) {
		t.Fatalf("invalid adapter result %v", e)
	}
	if _, e = p.Embed(context.Background(), []string{strings.Repeat("x", 2401)}); e == nil {
		t.Fatal("unbounded request")
	}
}
