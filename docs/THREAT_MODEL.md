# Threat model and security checklist

This preserves the Master Context security rules verbatim (line wrapping normalized).
Implemented = present and covered by current checks; Partial = only applicable foundation exists;
Pending = feature absent and must satisfy this rule before it is exposed. An absent
feature is not a claim of implemented security. Identity, project/task, and chat APIs are exposed only through the controls below.

| Area | Requirement | Status | Evidence / remaining work |
| --- | --- | --- | --- |
| Tenant isolation | [x] workspace_id comes ONLY from the authenticated session, never from body, query or URL. | Implemented | Session + verified membership; server-minted workspace creation and own-directory switch tickets. Two-workspace HTTP tests. |
| Tenant isolation | [x] Never fetch by id alone: always `id AND workspace_id`. | Implemented | Generated GetObject query filters both IDs; integration tests. |
| Tenant isolation | [x] App connects as a non-owner, non-superuser DB role. ENABLE and FORCE row level security on every tenant table. Set tenant with `SET LOCAL app.workspace_id` inside each transaction (never session-level SET; connection pools leak it). | Implemented | Bootstrap roles, runtime catalog checks, forced RLS, transaction helper, isolation tests. |
| Tenant isolation | [x] Vector search filters by workspace_id inside the SQL query, not after retrieval. | Implemented | Explicit predicates inside vector/FTS branches, live hash/parent joins, FORCE RLS, per-hit policy and two-workspace query/job tests. |
| Tenant isolation | [ ] Redis keys, websocket channels, cache entries, queue payloads and storage paths are all prefixed with the workspace id. Jobs re-verify tenant and permissions when they run. | Partial | Current query caches include workspace IDs; worker re-verifies service sessions and policy each batch. Workspace-prefixed Redis topics, leases, presence and flood budgets; chat fanout rechecks tenant policy. Private storage and search are tenant scoped. |
| Tenant isolation | [x] A cross-tenant test suite (two workspaces) must cover every endpoint, websocket subscription, search query and job type, and must run in CI. | Implemented | Docker suites cover identity privacy, every project/task/chat/proposal/brief/run/impact/meeting/search/ask endpoint and websocket subscription, unfiltered RLS, chat fanout, graph loader, proposal execution, agent generation, delay detection, create-plan/transcription/extraction jobs and hostile queued references. CI runs both suites without skips. |
| AI / agent safety | [x] All workspace content (chat, comments, transcripts, GitHub text, emails) is UNTRUSTED DATA. Put it in delimited data blocks in prompts; never in the instruction channel. | Implemented | Escaped delimited data blocks; fixed system instructions; hostile chat fixture cannot mutate tasks or approve proposals. |
| AI / agent safety | [x] Agents get minimum tools. Agent output is validated against a strict JSON schema and can only produce proposals. Approval is enforced in code, not in the prompt. | Implemented | No model tools or write dependencies; strict local schema and typed executors; generation creates only pending domain proposals plus code-controlled hypothetical assessments and immutable notices; no model tools or direct task mutation. |
| AI / agent safety | [x] Executor checks: proposal exists, status=approved, approver has permission, proposal not expired, payload hash unchanged since approval, and not already executed. | Implemented | Locked transaction checks status, expiry, review/approval hashes and execution marker; adversarial and concurrent execution tests. |
| AI / agent safety | [x] Execute with the approving user's permissions, never elevated agent permissions. | Implemented | Executor rechecks live approver membership and TaskWrite through policy.Can; revoked-role test. |
| AI / agent safety | [x] Sanitize rendered AI output: no external images or auto-loading links, escape HTML. | Implemented | Inbox strips HTML, renders React text and allow-lists internal source links; inert XSS test. |
| AI / agent safety | [x] Per-workspace LLM quotas, per-request token caps, max agent steps, timeouts. | Implemented | Persisted workspace quota reservations, bounded retries/steps, token caps, deadlines; quota exhaustion prevents provider call. |
| Auth and sessions | [x] argon2id password hashing. Sessions in httpOnly, Secure, SameSite=Lax cookies. CSRF token on every state-changing request. Short-lived access + revocable refresh. | Implemented | Argon2id, hashed opaque access/refresh, secure host cookies, exact Origin + CSRF, rotation/replay revocation; HTTP tests. |
| Auth and sessions | [ ] Rate limit and lock out login, signup, password reset, invite and AI endpoints. | Partial | Signup/login/invites and authenticated actions have shared budgets; persisted login lockout and bounded hashing. Internal AI enqueue has a shared per-user/workspace budget; plan requests and automatic message detection have author/workspace budgets; public reset delivery remains disabled. |
| Auth and sessions | [x] Invite and reset tokens: random 256-bit, stored hashed, single use, short expiry. | Implemented | 256-bit random hashes; conditional single-use and expiry. Invite tests; reset issuance remains an internal delivery boundary. |
| Auth and sessions | [ ] If JWT is used: pin the algorithm, verify exp/aud/iss. | Not applicable | Opaque hashed sessions; no JWT accepted. |
| Authorization | [x] Every route, websocket message and job passes through policy.Can(). | Implemented | Central public/human/service action matrix; transaction-time rechecks; consumer session reauthorization. Live session/policy checks on socket frames, outbound notifications and heartbeats. |
| Authorization | [x] Request DTOs use explicit allow-lists of updatable fields (no mass assignment; users can never set their own role or workspace_id). | Implemented | Generated typed DTOs plus strict unknown/required/null/body-size validation; self-role and injected workspace fields rejected in HTTP tests. |
| WebSockets | [x] Validate Origin. Authenticate on connect, then authorize every subscribe and every inbound message. Channel names include workspace id. Set read size limits, per-user connection caps and idle timeouts. | Implemented | Exact Origin/cookie auth; strict 4096-byte frames, shared per-user caps and flood budgets, heartbeat/idle deadlines; real Redis/Postgres adversarial tests. |
| Integrations | [ ] OAuth uses state + PKCE. Third-party tokens encrypted at rest (AES-GCM, key from env) and never logged. Minimal scopes. | Pending | Implement and test with the corresponding feature. |
| Integrations | [ ] Webhooks: verify HMAC signature with constant-time compare, check timestamp and reject replays (store delivery ids). | Pending | Implement and test with the corresponding feature. |
| Integrations | [ ] SSRF guard on any server-side fetch: block private, loopback, link-local and metadata IP ranges, re-check after redirects and DNS resolution. | Pending | Implement and test with the corresponding feature. |
| Audit | [x] Append-only audit_log: app role has INSERT only (REVOKE UPDATE, DELETE, TRUNCATE) plus a trigger that raises on UPDATE/DELETE. Each row stores a hash of the previous row. | Implemented | INSERT-only runtime grant; trigger blocks UPDATE/DELETE/TRUNCATE, scoped serialized SHA-256 chain and admin verification; real database tamper tests. |
| Audit | [ ] Log: logins, role changes, invites, exports, proposals created/approved/rejected/ executed, every agent action, with actor (human or agent), workspace, before/after. | Partial | Current identity/project/task/dependency/risk/chat events include actor kind, workspace and metadata before/after. Proposal transitions, agent completion and executed task changes write immediate audit rows with actor kind and metadata before/after; exports remain gated. |
| Audit | [x] Redact secrets and tokens from logs and audit payloads. | Implemented | slog allow-list; explicit event snapshots omit credentials and free text; secret-key payload checks; token hashes stay in auth. |
| Files and web | [x] Uploads: max size, allow-listed types, magic-byte check, random object names, tenant-prefixed storage keys, never served from the app origin with active content. | Implemented | Bounded streaming upload, MIME/signature allow-list, random tenant keys, private local storage, authenticated attachment downloads and inert blob player; adversarial two-workspace tests. |
| Files and web | [ ] Strict CSP, security headers (HSTS, X-Content-Type-Options, frame-ancestors none, Referrer-Policy). Sanitize any rich text with DOMPurify. sqlc/parameterized queries only. | Partial | API/NGINX headers and CSP; parameterized queries within TenantTx; DOMPurify strips chat HTML before React text rendering. Validated internal task cards only; XSS tests. |
| Files and web | [x] Secrets from environment only, never committed. CI runs govulncheck, npm audit, gosec, golangci-lint, and the tenant isolation suite. | Implemented | Random local secret files, ignored .env, CI scan jobs, isolation suite. Production secret manager required. |

## Approved global identity exception

The user explicitly approved exactly six non-RLS tables in `identity`: users,
credentials, sessions, memberships (own-user directory), invites, password_resets.
Only internal/auth accesses them; grants are minimal, and no tenant business or AI
content belongs there. Global directory access always filters the authenticated
user. Tenant member administration uses the separate forced-RLS app.members
projection. Internal consumer identity and shared rate metadata use sessions.
The migration version table remains inaccessible infrastructure metadata.

## Boundaries and threats

- Browser to API: authenticated opaque cookies, exact Origin/CSRF, bounded strict
  DTOs, server-derived scope, optimistic versions, fail-closed role matrix.
- API/worker to Postgres: role/ownership checks, sealed transaction capability,
  forced RLS, explicit workspace filters, composite references, and architecture
  tests. Database administrators and a compromised application process holding
  the runtime credential remain trust boundaries; RLS relies on trusted binding.
- Worker: only authenticated service scopes; recheck revocation/expiry/policy per
  batch. Untrusted event references cannot select another workspace. Receipt,
  effects, and outbox publication share a transaction; retries are idempotent.
- Migration/administration: owner/admin secrets never mounted in runtime containers.
  Append-only triggers resist ordinary writes, not a malicious database administrator
  able to disable triggers or rewrite trusted audit heads. Export trusted heads to
  independent storage when external tamper evidence is required.
- Redis: workspace-prefixed ephemeral IDs, TTL presence and shared flood/connection
  budgets. Authorized notifications carry no text. Reconnect reloads REST state;
  pub/sub is transient and duplicate invalidations are harmless. External job brokers remain gated.
- External content/LLMs: providers default disabled. Fixed HTTPS provider endpoints
  have no redirects or model tools. Worker-only keys and explicit price ceilings
  are required; output is validated data and cannot create executor authority.
  Free text is excluded from event/audit snapshots. Quotas reserve conservatively
  under tenant locks; provider costs remain estimates, not billing reconciliation.
- Containers/local host: Docker administrators can read local credential volumes.
  Production requires managed secrets, HTTPS, rotation, restricted host access,
  verified database/Redis TLS, and an exact PUBLIC_ORIGIN.

## Scope gate

Other agent features, OAuth, webhooks, arbitrary outbound fetches,
River jobs, rich HTML, and public password-reset email delivery remain
absent. Pending checklist controls must be satisfied before exposing these features.
Invite links are manually shared; comments are explicitly a placeholder. Risk is a
calendar-based deterministic forecast and never automatically mutates downstream tasks.

## Proposal safety boundary

Proposal approval is an explicit human action protected by CSRF, current version,
and a hash binding scope, payload, preview and sources. Model output cannot supply
an approver. The worker revalidates live permissions and references; all task effects,
outbox records and before/after audit rows commit with the execution marker.
Terminal failure does not partially apply changes. Source IDs are represented by
RLS-protected generic links; both run and proposal queries use TenantTx.

Prompt delimiting does not prove that a model will ignore injected text. The tested
security boundary is that even hostile model output has no write capability: valid
output produces only a pending proposal, invalid output fails, and execution requires
independent human approval plus code checks. Administrators controlling provider
configuration, runtime code or the database remain trusted deployment boundaries.

## Delay detection and brief planning

- New endpoints retain session-derived tenancy, central policy, strict allow-lists and
  CSRF for mutations. Briefs use FORCE RLS, composite object keys and minimal grants.
- The model never chooses a workspace, author, approver, system message body, or SQL.
  The persisted message/version supplies identity and evidence; resolution requires
  accessible active tasks. A foreign candidate UUID resolves to nothing.
- Low confidence, duplicate titles and multiple task links require author confirmation.
  Confirmation is separate from execution approval; dates remain unchanged until approved.
- System messages carry a tenant-bound run reference and use fixed text. Human edit/delete
  and recursive detection are denied. Evidence and model-generated plan text render as
  sanitized React text, with only server-generated internal links.
- Risk scenarios are separate from committed forecasts. Multi-proposal output generation
  and single-plan execution are transactional; any failure rolls back partial effects.
- Injection tests cover hostile chat content and a provider returning an unauthorized SQL
  field. Other tests cover cross-workspace candidate resolution, endpoints and jobs,
  changed input, false positives, confirmation replay, plan cycles and approval idempotency.
- Providers are disabled by default. Mock mode is explicitly labeled deterministic demo
  behavior. Enabling a real adapter intentionally sends bounded workspace content to the
  deployment-selected provider; runtime keys remain worker-only and outside logs.

## Meeting upload, transcription and evidence

- Allowed media only: streaming byte/overhead limits, signature checks, random names,
  exclusive private writes and authenticated resource lookup. Original filenames never
  become paths. Storage refuses foreign prefixes and traversal before touching a file.
- FFmpeg/ffprobe receive fixed arguments and a private temporary file, have deadlines,
  limited output/allocation and local-only protocols. Decoded sample counts set duration;
  container metadata alone cannot undercount usage. Only one audio track is accepted.
  Media parsing remains a deployment boundary: keep FFmpeg and the base image patched.
- Minute reservations serialize by workspace and renew at job start. Failures preserve
  uncertain usage. Live identity/policy is checked before external transcription and
  before committing results; no client-supplied duration or workspace is authoritative.
- Transcripts are immutable untrusted data, including speaker labels. Extraction has
  strict schemas, supplied-segment allow-lists and literal quote checks. Timestamps and
  project associations are resolved in code. Foreign/fabricated evidence fails closed.
- Evidence is rechecked during approval/execution. Approval cannot turn a segment from
  another meeting/workspace into authority. Decision/task creation and audit are atomic.
- Player content is an authenticated attachment fetched into a media-only Blob, never
  inline active HTML or an external URL. DOMPurify and React text rendering apply to
  titles, transcripts, speaker labels, evidence and extracted suggestions.
- Mock transcription is labeled as a fixture. Real-provider calls are opt-in and send
  recording content to the configured provider. No live keys or calls are needed in CI.

## Search security checklist

| Rule | Status | Evidence |
| --- | --- | --- |
| FORCE RLS, explicit workspace predicates inside both hybrid branches, composite keys and grants | Implemented | embeddings/jobs/requests/usage; security-invoker view; real two-workspace tests |
| Per-object policy and live parent/deletion visibility before retrieval and read | Implemented | fail-closed type mapping; hash-bound index joins and cached result revalidation |
| Index workers reverify service scope and canonical source content | Implemented | leased generation/hash commits, idempotent event receipts and unchanged-content skip |
| Questions and retrieved content remain untrusted data; no tools | Implemented | escaped data blocks, strict answer schema and hostile-content fixture |
| Citation IDs must exist in the authorized retrieved set | Implemented | foreign/fabricated/duplicate/empty citation tests; source change invalidates answer |
| Own-user job lookup, CSRF, bounded requests, shared quotas and deadlines | Implemented | scope from sessions only, 30/minute requests, shared reserved token/cost ledger |
| Safe snippets, generated internal links and no raw model HTML | Implemented | DOMPurify plus React text; frontend inert XSS/citation test |

Indexing and Ask intentionally send bounded content to the selected real provider
when enabled. Keys stay worker-only; logs contain only usage metadata. HNSW is
approximate and cannot guarantee recall. Citation validation does not establish
truth or defeat all prompt injection; read-only output has no business-write
capability. Soft-deleted content is excluded immediately through live joins even
before physical index cleanup. Retention/administrative database access remains
a deployment responsibility. Search starts disabled and mock mode is labeled.
