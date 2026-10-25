# Threat model and security checklist

This preserves the Master Context security rules verbatim (line wrapping normalized).
Implemented = present in this scaffold; Partial = only applicable foundation exists;
Pending = feature absent and must satisfy this rule before it is exposed. An absent
feature is not a claim of implemented security. No tenant business APIs are exposed.

| Area | Requirement | Status | Evidence / remaining work |
| --- | --- | --- | --- |
| Tenant isolation | [ ] workspace_id comes ONLY from the authenticated session, never from body, query or URL. | Pending | No session resolver or tenant endpoints yet. |
| Tenant isolation | [x] Never fetch by id alone: always `id AND workspace_id`. | Implemented | Generated GetObject query filters both IDs; integration tests. |
| Tenant isolation | [x] App connects as a non-owner, non-superuser DB role. ENABLE and FORCE row level security on every tenant table. Set tenant with `SET LOCAL app.workspace_id` inside each transaction (never session-level SET; connection pools leak it). | Implemented | Bootstrap roles, runtime catalog checks, forced RLS, transaction helper, isolation tests. |
| Tenant isolation | [ ] Vector search filters by workspace_id inside the SQL query, not after retrieval. | Pending | No search implementation. |
| Tenant isolation | [ ] Redis keys, websocket channels, cache entries, queue payloads and storage paths are all prefixed with the workspace id. Jobs re-verify tenant and permissions when they run. | Pending | No domain keys/channels/jobs/storage yet; only infrastructure PING. |
| Tenant isolation | [ ] A cross-tenant test suite (two workspaces) must cover every endpoint, websocket subscription, search query and job type, and must run in CI. | Partial | CI tests current tables and public health; extend for every new endpoint/job/search/subscription. |
| AI / agent safety | [ ] All workspace content (chat, comments, transcripts, GitHub text, emails) is UNTRUSTED DATA. Put it in delimited data blocks in prompts; never in the instruction channel. | Pending | Implement and test with the corresponding feature. |
| AI / agent safety | [ ] Agents get minimum tools. Agent output is validated against a strict JSON schema and can only produce proposals. Approval is enforced in code, not in the prompt. | Pending | Implement and test with the corresponding feature. |
| AI / agent safety | [ ] Executor checks: proposal exists, status=approved, approver has permission, proposal not expired, payload hash unchanged since approval, and not already executed. | Pending | Implement and test with the corresponding feature. |
| AI / agent safety | [ ] Execute with the approving user's permissions, never elevated agent permissions. | Pending | Implement and test with the corresponding feature. |
| AI / agent safety | [ ] Sanitize rendered AI output: no external images or auto-loading links, escape HTML. | Pending | Implement and test with the corresponding feature. |
| AI / agent safety | [ ] Per-workspace LLM quotas, per-request token caps, max agent steps, timeouts. | Pending | Implement and test with the corresponding feature. |
| Auth and sessions | [ ] argon2id password hashing. Sessions in httpOnly, Secure, SameSite=Lax cookies. CSRF token on every state-changing request. Short-lived access + revocable refresh. | Pending | Implement and test with the corresponding feature. |
| Auth and sessions | [ ] Rate limit and lock out login, signup, password reset, invite and AI endpoints. | Pending | Implement and test with the corresponding feature. |
| Auth and sessions | [ ] Invite and reset tokens: random 256-bit, stored hashed, single use, short expiry. | Pending | Implement and test with the corresponding feature. |
| Auth and sessions | [ ] If JWT is used: pin the algorithm, verify exp/aud/iss. | Pending | Implement and test with the corresponding feature. |
| Authorization | [ ] Every route, websocket message and job passes through policy.Can(). | Partial | All existing health handlers call policy.Can; future job/websocket types must follow. |
| Authorization | [ ] Request DTOs use explicit allow-lists of updatable fields (no mass assignment; users can never set their own role or workspace_id). | Pending | Implement and test with the corresponding feature. |
| WebSockets | [ ] Validate Origin. Authenticate on connect, then authorize every subscribe and every inbound message. Channel names include workspace id. Set read size limits, per-user connection caps and idle timeouts. | Pending | Implement and test with the corresponding feature. |
| Integrations | [ ] OAuth uses state + PKCE. Third-party tokens encrypted at rest (AES-GCM, key from env) and never logged. Minimal scopes. | Pending | Implement and test with the corresponding feature. |
| Integrations | [ ] Webhooks: verify HMAC signature with constant-time compare, check timestamp and reject replays (store delivery ids). | Pending | Implement and test with the corresponding feature. |
| Integrations | [ ] SSRF guard on any server-side fetch: block private, loopback, link-local and metadata IP ranges, re-check after redirects and DNS resolution. | Pending | Implement and test with the corresponding feature. |
| Audit | [ ] Append-only audit_log: app role has INSERT only (REVOKE UPDATE, DELETE, TRUNCATE) plus a trigger that raises on UPDATE/DELETE. Each row stores a hash of the previous row. | Pending | Implement and test with the corresponding feature. |
| Audit | [ ] Log: logins, role changes, invites, exports, proposals created/approved/rejected/ executed, every agent action, with actor (human or agent), workspace, before/after. | Pending | Implement and test with the corresponding feature. |
| Audit | [ ] Redact secrets and tokens from logs and audit payloads. | Partial | slog allow-list redaction tested; audit not implemented. |
| Files and web | [ ] Uploads: max size, allow-listed types, magic-byte check, random object names, tenant-prefixed storage keys, never served from the app origin with active content. | Pending | Implement and test with the corresponding feature. |
| Files and web | [ ] Strict CSP, security headers (HSTS, X-Content-Type-Options, frame-ancestors none, Referrer-Policy). Sanitize any rich text with DOMPurify. sqlc/parameterized queries only. | Partial | API/NGINX headers implemented; SQL generated; rich text absent, DOMPurify required before enabling it. |
| Files and web | [x] Secrets from environment only, never committed. CI runs govulncheck, npm audit, gosec, golangci-lint, and the tenant isolation suite. | Implemented | Random local secret files, ignored .env, CI scan jobs, isolation suite. Production secret manager required. |

## Boundaries and threats

- Browser → API: hostile IDs, content, credentials, and sessions; authentication/CSRF remain pending. Only health is exposed.
- API/worker → Postgres: fail-closed role verification plus forced RLS; compromise of the runtime process remains inside its granted capabilities. RLS assumes trusted session-to-transaction binding.
- Migration/administration: privileged control plane; owner credentials never mounted in runtime containers. Migration bookkeeping is isolated infrastructure metadata.
- Redis/queues/storage: no domain data yet. Tenant prefixes and job re-authorization are mandatory when enabled.
- External content/LLMs: no providers enabled. Prompt injection must never create executable authority.
- Containers/local host: local development credentials are volume files readable by their assigned containers and Docker administrators. Production requires managed secrets, TLS, rotation, and restricted host access.

## Scope gate

No authentication, agent, proposal, executor, upload, audit writer, webhook, outbound fetch, websocket, River job, or vector search implementation is included. Each must update this checklist and add the required security tests before activation.
