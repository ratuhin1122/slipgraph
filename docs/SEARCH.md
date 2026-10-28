# Search and Ask

Use **Ctrl+K** (Cmd+K on macOS) or the Search / Ask button. Results are grouped
into tasks, messages and transcript segments. Search returns up to 20 objects;
Ask uses up to eight snippets and returns independently cited claims. Snippets
are plain text. Citations prove membership in the retrieved set, not truth.

## Configuration

The default is disabled. Set `EMBEDDING_PROVIDER=mock` and `LLM_PROVIDER=mock`
for an offline demonstration. The embedding fixture uses normalized hashed word
counts, not semantic understanding; the answer fixture quotes a retrieved excerpt.
Both are labeled in the UI. Restart API and worker after changing provider modes.

For real embeddings, set worker-only `EMBEDDING_PROVIDER=openai`,
`EMBEDDING_MODEL=text-embedding-3-small` or `text-embedding-3-large`, and
`OPENAI_API_KEY`. The API receives only the provider mode. Vectors use an explicit
384 dimensions and float encoding, following the official
[embedding API contract](https://developers.openai.com/api/reference/resources/embeddings/methods/create).
Ask uses the existing opt-in structured-output LLM provider and its worker-only key.
Conservative `LLM_INPUT_MICROUSD_PER_TOKEN` and `LLM_OUTPUT_MICROUSD_PER_TOKEN`
ceilings are required for any real embedding provider, even if Ask is disabled.

`SEARCH_DAILY_TOKENS` defaults to 1,000,000, capped by `LLM_DAILY_TOKENS`.
Search/indexing and agents share the rolling token/cost ledger and advisory lock;
neither can bypass the other. Byte-based reservations include overhead/output,
retain uncertain charges, and are estimates rather than billing reconciliation.
Indexing has a 30-second call limit and one-hour retry backoff. Query embedding and
answer calls each have 30-second limits; request leases expire after two minutes.
Crashed requests fail instead of automatically rebilling. Each user/workspace may
queue 30 requests/minute. Queries are at most 500 bytes, chunks at most 2400 bytes,
and an indexed object at most 32 chunks. There are no write tools.

## Contracts and consistency

- `POST /search` or `POST /ask` accepts exactly `{ "query": "..." }` with CSRF.
  Returns 202 plus a request ID; no workspace ID is accepted.
- Poll `GET /search/{requestId}` for queued/running/complete/failed. Only the
  authenticated requesting user can read the job, including within one workspace.
  Results are accessible for 24 hours. Record retention is operational cleanup;
  the read boundary does not rely on physical deletion.
- Remote embedding/generation happens only in authorized worker jobs. An outbox
  consumer queues task/message changes and meeting transcript completion atomically
  with its delivery receipt. Scoped backfill discovers pre-existing objects.
  This is eventually consistent; freshly changed content may be temporarily absent.
- The worker rereads canonical tenant objects, never event text. Stable UTF-8
  chunks use whole-content SHA-256 hashes. Replayed unchanged content skips calls.
  A generation lease and a second hash check prevent stale workers replacing newer
  content. Model changes enqueue replacement vectors; models are never mixed.
- Hybrid retrieval fuses the top 100 cosine and lexical candidates using reciprocal
  ranks, deduplicating by object. SQL filters workspace in **both** candidate branches
  and all joins, with FORCE RLS underneath a security-invoker document view.
  HNSW iterative scans improve filtered recall; ANN remains approximate. Full text
  uses PostgreSQL English web-search parsing and a GIN index.
- Live parent/object visibility and matching content hashes suppress deleted,
  parent-deleted and stale indexed rows before they reach prompts. Central policy
  checks the type of every hit. Current object read permissions are workspace-wide;
  unknown types and roles fail closed. Future private-object policy must extend the
  same boundary before exposure.
- All questions and snippets stay inside escaped untrusted data blocks. Strict
  schemas reject additional fields. Every claim must cite at least one real object
  from the supplied retrieved set; foreign/fabricated IDs reject the whole answer.
  Source URLs are code-built. Completion and every result read recheck membership,
  policy, live content hashes and parent visibility. Any changed retrieved source
  invalidates the saved answer, while still-visible unchanged search hits remain.
- Task links open the task drawer, chat links resolve the exact cited message, including older messages, and transcript
  links resolve the real segment timestamp. No external URLs or raw HTML render.

Run `python scripts/search_benchmark.py` for an isolated 100,000-object benchmark.
It writes measured results to README and `docs/SEARCH_BENCHMARK.json`; it never
uses a production database. The fixture has 100 synthetic vector buckets and
reports warm serial SQL/authorization latency, excluding queue and provider time.
