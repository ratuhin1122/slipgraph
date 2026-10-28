package search

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/slipgraph/slipgraph/internal/auth"
	"github.com/slipgraph/slipgraph/internal/llm"
	"github.com/slipgraph/slipgraph/internal/platform/database"
	"github.com/slipgraph/slipgraph/internal/policy"
)

type Hit struct {
	ObjectID    uuid.UUID `json:"object_id"`
	Type        string    `json:"type"`
	Title       string    `json:"title"`
	Snippet     string    `json:"snippet"`
	URL         string    `json:"url"`
	ContentHash string    `json:"content_hash"`
}
type Document struct {
	Hit
	Content string
}

func canRead(ctx context.Context, a policy.Actor, kind string) bool {
	var action policy.Action
	switch kind {
	case "task":
		action = policy.TaskRead
	case "message":
		action = policy.ChannelRead
	case "transcript_segment":
		action = policy.MeetingRead
	default:
		return false
	}
	if a.Role == policy.Consumer {
		return policy.Can(ctx, a, policy.SearchIndex, policy.Resource{WorkspaceID: a.WorkspaceID})
	}
	return policy.Can(ctx, a, action, policy.Resource{WorkspaceID: a.WorkspaceID})
}
func load(ctx context.Context, tx database.TenantTx, a policy.Actor, id uuid.UUID) (Document, error) {
	var d Document
	e := tx.QueryRow(ctx, `SELECT id,type,title,content,url,content_hash FROM app.search_documents WHERE workspace_id=$1 AND id=$2`, tx.WorkspaceID(), id).Scan(&d.ObjectID, &d.Type, &d.Title, &d.Content, &d.URL, &d.ContentHash)
	if errors.Is(e, pgx.ErrNoRows) {
		return d, auth.ErrNotFound
	}
	if e != nil {
		return d, e
	}
	if !canRead(ctx, a, d.Type) {
		return Document{}, auth.ErrForbidden
	}
	return d, nil
}

// Chunks are deterministic, UTF-8 safe, bounded by bytes; no model controls SQL or scope.
func Chunks(text string) []string {
	out := []string{}
	for len(text) > 0 {
		n := min(2400, len(text))
		for n > 0 && !utf8.ValidString(text[:n]) {
			n--
		}
		if n == 0 {
			break
		}
		part := text[:n]
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
		text = text[n:]
	}
	return out
}
func vectorLiteral(v []float32) (string, error) {
	if !llm.ValidVector(v) {
		return "", llm.ErrInvalid
	}
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(float64(x), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}

// HybridSQL filters each candidate branch by workspace INSIDE SQL. Reciprocal-rank
// fusion combines lexical and cosine rankings; live document hashes suppress stale chunks.
const HybridSQL = `WITH semantic AS MATERIALIZED (
 SELECT object_id,chunk_index,row_number() OVER (ORDER BY vector <=> $3::public.vector,object_id,chunk_index) AS rank
 FROM (SELECT object_id,chunk_index,vector FROM app.embeddings WHERE workspace_id=$1 AND model=$4 ORDER BY vector <=> $3::public.vector LIMIT 100) s
), lexical AS MATERIALIZED (
 SELECT object_id,chunk_index,row_number() OVER (ORDER BY ts_rank_cd(document,websearch_to_tsquery('english',$2)) DESC,object_id,chunk_index) AS rank
 FROM app.embeddings WHERE workspace_id=$1 AND model=$4 AND document @@ websearch_to_tsquery('english',$2)
 ORDER BY ts_rank_cd(document,websearch_to_tsquery('english',$2)) DESC,object_id,chunk_index LIMIT 100
), fused AS MATERIALIZED (
 SELECT object_id,chunk_index,sum(score) AS score FROM (
 SELECT object_id,chunk_index,1.0/(60+rank) AS score FROM semantic UNION ALL
 SELECT object_id,chunk_index,1.0/(60+rank) FROM lexical) r GROUP BY object_id,chunk_index
), candidates AS MATERIALIZED (
 SELECT f.object_id,f.chunk_index,f.score,e.chunk,e.content_hash
 FROM fused f CROSS JOIN LATERAL (
 SELECT chunk,content_hash FROM app.embeddings WHERE workspace_id=$1 AND object_id=f.object_id AND chunk_index=f.chunk_index AND model=$4 OFFSET 0
 ) e
), ranked AS (
 SELECT d.id,d.type,d.title,c.chunk,d.url,d.content_hash,c.score,
 row_number() OVER(PARTITION BY d.id ORDER BY c.score DESC,c.chunk_index) AS choice
 FROM candidates c CROSS JOIN LATERAL (
 SELECT id,type,title,url,content_hash FROM app.search_documents
 WHERE workspace_id=$1 AND id=c.object_id AND content_hash=c.content_hash OFFSET 0
 ) d)
 SELECT id,type,title,left(chunk,600),url,content_hash FROM ranked WHERE choice=1 ORDER BY score DESC,id LIMIT 20`

func Retrieve(ctx context.Context, tx database.TenantTx, a policy.Actor, query, model string, v []float32) ([]Hit, error) {
	if !policy.Can(ctx, a, policy.SearchRead, policy.Resource{WorkspaceID: tx.WorkspaceID().String()}) {
		return nil, auth.ErrForbidden
	}
	if _, e := tx.Exec(ctx, `SELECT set_config('hnsw.iterative_scan','strict_order',true),set_config('hnsw.ef_search','100',true)`); e != nil {
		return nil, e
	}
	literal, e := vectorLiteral(v)
	if e != nil {
		return nil, e
	}
	rows, e := tx.Query(ctx, HybridSQL, tx.WorkspaceID(), query, literal, model)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Hit{}
	for rows.Next() {
		var h Hit
		if e := rows.Scan(&h.ObjectID, &h.Type, &h.Title, &h.Snippet, &h.URL, &h.ContentHash); e != nil {
			return nil, e
		}
		h.Snippet = snippet(h.Snippet)
		if canRead(ctx, a, h.Type) {
			out = append(out, h)
		}
	}
	return out, rows.Err()
}

type Claim struct {
	Text      string      `json:"text"`
	ObjectIDs []uuid.UUID `json:"object_ids"`
}
type Answer struct {
	Claims []Claim `json:"claims"`
}

var AnswerSchema = json.RawMessage(`{"type":"object","additionalProperties":false,"required":["claims"],"properties":{"claims":{"type":"array","maxItems":8,"items":{"type":"object","additionalProperties":false,"required":["text","object_ids"],"properties":{"text":{"type":"string","minLength":1,"maxLength":1000},"object_ids":{"type":"array","minItems":1,"maxItems":5,"uniqueItems":true,"items":{"type":"string","format":"uuid"}}}}}}}`)

func ValidateAnswer(data []byte, hits []Hit) (Answer, error) {
	var a Answer
	if llm.Validate(AnswerSchema, data) != nil || json.Unmarshal(data, &a) != nil {
		return a, llm.ErrInvalid
	}
	allowed := map[uuid.UUID]bool{}
	for _, h := range hits {
		allowed[h.ObjectID] = true
	}
	for _, c := range a.Claims {
		for _, id := range c.ObjectIDs {
			if !allowed[id] {
				return Answer{}, llm.ErrInvalid
			}
		}
	}
	return a, nil
}

// snippet bounds both JSON/prompt bytes and Unicode correctness.
func snippet(s string) string {
	if len(s) <= 600 {
		return s
	}
	n := 600
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return s[:n]
}
