# Threat model and security checklist

This preserves the Master Context security rules verbatim (line wrapping normalized).
Implemented = present and covered by current checks; Partial = only applicable foundation exists;
Pending = feature absent and must satisfy this rule before it is exposed. An absent
feature is not a claim of implemented security. Identity and project/task APIs are exposed only through the controls below.

| Area | Requirement | Status | Evidence / remaining work |
| --- | --- | --- | --- |
| Tenant isolation | [x] workspace_id comes ONLY from the authenticated session, never from body, query or URL. | Implemented | Session + verified membership; server-minted workspace creation and own-directory switch tickets. Two-workspace HTTP tests. |
| Tenant isolation | [x] Never fetch by id alone: always `id AND workspace_id`. | Implemented | Generated GetObject query filters both IDs; integration tests. |
| Tenant isolation | [x] App connects as a non-owner, non-superuser DB role. ENABLE and FORCE row level security on every tenant table. Set tenant with `SET LOCAL app.workspace_id` inside each transaction (never session-level SET; connection pools leak it). | Implemented | Bootstrap roles, runtime catalog checks, forced RLS, transaction helper, isolation tests. |
| Tenant isolation | [ ] Vector search filters by workspace_id inside the SQL query, not after retrieval. | Pending | No search implementation. |
| Tenant isolation | [ ] Redis keys, websocket channels, cache entries, queue payloads and storage paths are all prefixed with the workspace id. Jobs re-verify tenant and permissions when they run. | Partial | Current query caches include workspace IDs; worker re-verifies service sessions and policy each batch. Redis holds no domain data; channels/storage remain gated. |
| Tenant isolation | [x] A cross-tenant test suite (two workspaces) must cover every endpoint, websocket subscription, search query and job type, and must run in CI. | Implemented | Docker suites cover identity privacy, every project/task endpoint, unfiltered RLS, graph loader and hostile queued references. CI runs both suites without skips. |
| AI / agent safety | [ ] All workspace content (chat, comments, transcripts, GitHub text, emails) is UNTRUSTED DATA. Put it in delimited data blocks in prompts; never in the instruction channel. | Pending | Implement and test with the corresponding feature. |
| AI / agent safety | [ ] Agents get minimum tools. Agent output is validated against a strict JSON schema and can only produce proposals. Approval is enforced in code, not in the prompt. | Pending | Implement and test with the corresponding feature. |
| AI / agent safety | [ ] Executor checks: proposal exists, status=approved, approver has permission, proposal not expired, payload hash unchanged since approval, and not already executed. | Pending | Implement and test with the corresponding feature. |
| AI / agent safety | [ ] Execute with the approving user's permissions, never elevated agent permissions. | Pending | Implement and test with the corresponding feature. |
| AI / agent safety | [ ] Sanitize rendered AI output: no external images or auto-loading links, escape HTML. | Pending | Implement and test with the corresponding feature. |
| AI / agent safety | [ ] Per-workspace LLM quotas, per-request token caps, max agent steps, timeouts. | Pending | Implement and test with the corresponding feature. |
| Auth and sessions | [x] argon2id password hashing. Sessions in httpOnly, Secure, SameSite=Lax cookies. CSRF token on every state-changing request. Short-lived access + revocable refresh. | Implemented | Argon2id, hashed opaque access/refresh, secure host cookies, exact Origin + CSRF, rotation/replay revocation; HTTP tests. |
| Auth and sessions | [ ] Rate limit and lock out login, signup, password reset, invite and AI endpoints. | Partial | Signup/login/invites and authenticated actions have shared budgets; persisted login lockout and bounded hashing. Public password reset delivery and AI endpoints remain disabled. |
| Auth and sessions | [x] Invite and reset tokens: random 256-bit, stored hashed, single use, short expiry. | Implemented | 256-bit random hashes; conditional single-use and expiry. Invite tests; reset issuance remains an internal delivery boundary. |
| Auth and sessions | [ ] If JWT is used: pin the algorithm, verify exp/aud/iss. | Not applicable | Opaque hashed sessions; no JWT accepted. |
| Authorization | [x] Every route, websocket message and job passes through policy.Can(). | Implemented | Central public/human/service action matrix; transaction-time rechecks; consumer session reauthorization. No websocket endpoint exists. |
| Authorization | [x] Request DTOs use explicit allow-lists of updatable fields (no mass assignment; users can never set their own role or workspace_id). | Implemented | Generated typed DTOs plus strict unknown/required/null/body-size validation; self-role and injected workspace fields rejected in HTTP tests. |
| WebSockets | [ ] Validate Origin. Authenticate on connect, then authorize every subscribe and every inbound message. Channel names include workspace id. Set read size limits, per-user connection caps and idle timeouts. | Pending | Implement and test with the corresponding feature. |
| Integrations | [ ] OAuth uses state + PKCE. Third-party tokens encrypted at rest (AES-GCM, key from env) and never logged. Minimal scopes. | Pending | Implement and test with the corresponding feature. |
| Integrations | [ ] Webhooks: verify HMAC signature with constant-time compare, check timestamp and reject replays (store delivery ids). | Pending | Implement and test with the corresponding feature. |
| Integrations | [ ] SSRF guard on any server-side fetch: block private, loopback, link-local and metadata IP ranges, re-check after redirects and DNS resolution. | Pending | Implement and test with the corresponding feature. |
| Audit | [x] Append-only audit_log: app role has INSERT only (REVOKE UPDATE, DELETE, TRUNCATE) plus a trigger that raises on UPDATE/DELETE. Each row stores a hash of the previous row. | Implemented | INSERT-only runtime grant; trigger blocks UPDATE/DELETE/TRUNCATE, scoped serialized SHA-256 chain and admin verification; real database tamper tests. |
| Audit | [ ] Log: logins, role changes, invites, exports, proposals created/approved/rejected/ executed, every agent action, with actor (human or agent), workspace, before/after. | Partial | Current identity/project/task/dependency/risk events include actor kind, workspace and metadata before/after. Exports/proposals/agents remain absent and gated. |
| Audit | [x] Redact secrets and tokens from logs and audit payloads. | Implemented | slog allow-list; explicit event snapshots omit credentials and free text; secret-key payload checks; token hashes stay in auth. |
| Files and web | [ ] Uploads: max size, allow-listed types, magic-byte check, random object names, tenant-prefixed storage keys, never served from the app origin with active content. | Pending | Implement and test with the corresponding feature. |
| Files and web | [ ] Strict CSP, security headers (HSTS, X-Content-Type-Options, frame-ancestors none, Referrer-Policy). Sanitize any rich text with DOMPurify. sqlc/parameterized queries only. | Partial | API/NGINX headers and CSP; parameterized queries within TenantTx; React escapes text. No rich-text renderer; DOMPurify gates that feature. |
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
- Redis/queues/storage: no domain Redis/storage or external job broker yet. Tenant
  prefixes, authorization, and two-workspace tests gate future features.
- External content/LLMs: no provider enabled. Treat text as data; it cannot create
  executor authority. Free text is excluded from event/audit snapshots.
- Containers/local host: Docker administrators can read local credential volumes.
  Production requires managed secrets, HTTPS, rotation, restricted host access,
  verified database/Redis TLS, and an exact PUBLIC_ORIGIN.

## Scope gate

Agents, proposals, execution, uploads, OAuth, webhooks, outbound fetches, websockets,
River jobs, vector search, rich text, and public password-reset email delivery remain
absent. Pending checklist controls must be satisfied before exposing these features.
Invite links are manually shared; comments are explicitly a placeholder. Risk is a
calendar-based deterministic forecast and never automatically mutates downstream tasks.
