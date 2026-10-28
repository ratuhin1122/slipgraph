# Threat model and security checklist

This preserves the Master Context security rules verbatim (line wrapping normalized).
Implemented = present and covered by current checks; Partial = only applicable foundation exists;
Pending = feature absent and must satisfy this rule before it is exposed. An absent
feature is not a claim of implemented security. Identity, project/task, and chat APIs are exposed only through the controls below.

| Area | Requirement | Status | Evidence / remaining work |
| --- | --- | --- | --- |
| Tenant isolation | [x] workspace_id comes ONLY from the authenticated session, never from body, query or URL. | Implemented | Session + verified membership; creation uses server UUIDs and switching uses own-directory tickets. `TestOpenAPITenantMatrix`, `TestDatabaseBoundaries`, `TestGuardRejectsBypass`. Source audit below. |
| Tenant isolation | [x] Never fetch by id alone: always `id AND workspace_id`. | Implemented | Explicit object/workspace predicates plus FORCE RLS. `TestEveryTenantTableRLSWithoutApplicationFilters` and `TestOpenAPITenantMatrix`, repeated with `rlstest`. |
| Tenant isolation | [x] App connects as a non-owner, non-superuser DB role. ENABLE and FORCE row level security on every tenant table. Set tenant with `SET LOCAL app.workspace_id` inside each transaction (never session-level SET; connection pools leak it). | Implemented | `TestTenantIsolationAndRoleSafety`, `TestEveryTenantTableRLSWithoutApplicationFilters`, `TestProductionSQLFiltersRemainIntact`. |
| Tenant isolation | [x] Vector search filters by workspace_id inside the SQL query, not after retrieval. | Implemented | Both SQL branches filter scope, live hashes/parents and per-hit policy are checked. `TestSearchIsolationAndCitations`, `TestOpenAPITenantMatrix/requestSearch` and `requestAnswer`, normal and filter-removal builds. |
| Tenant isolation | [ ] Redis keys, websocket channels, cache entries, queue payloads and storage paths are all prefixed with the workspace id. Jobs re-verify tenant and permissions when they run. | Partial | Current query caches include workspace IDs; worker re-verifies service sessions and policy each batch. Workspace-prefixed Redis topics, leases, presence and flood budgets; chat fanout rechecks tenant policy. Private storage and search are tenant scoped. |
| Tenant isolation | [x] A cross-tenant test suite (two workspaces) must cover every endpoint, websocket subscription, search query and job type, and must run in CI. | Implemented | `TestEveryOpenAPIOperationHasSecurityCase`, `TestRegisteredRoutesMatchOpenAPI`, `TestOpenAPITenantMatrix` (68 operations), `TestEveryWorkerJobRejectsForeignScope`, `TestRegisteredJobsHaveExecutedSecurityCases`, and `TestChatRealtimeIsolation`. CI repeats the complete integration suite with business SQL filters removed. |
| AI / agent safety | [x] All workspace content (chat, comments, transcripts, GitHub text, emails) is UNTRUSTED DATA. Put it in delimited data blocks in prompts; never in the instruction channel. | Implemented | Escaped data blocks and fixed instructions. `TestPromptAndSchemaBoundary`, `TestAgentSafetyFoundation`, `TestSignatureWorkflows`, `TestMeetingsSecurityAndPipeline`, `TestMockAnswerTreatsInjectionAsData`. |
| AI / agent safety | [x] Agents get minimum tools. Agent output is validated against a strict JSON schema and can only produce proposals. Approval is enforced in code, not in the prompt. | Implemented | No model tools/write dependencies; schema-validated pending proposals only, with read-only cited answers separate. `TestLLMHasNoDomainWriteDependencies`, `TestAgentSafetyFoundation`, `TestCitationsFailClosed`. |
| AI / agent safety | [x] Executor checks: proposal exists, status=approved, approver has permission, proposal not expired, payload hash unchanged since approval, and not already executed. | Implemented | Locked status/expiry/hash/permission/idempotency checks. `TestAgentSafetyFoundation`, `TestEveryWorkerJobRejectsForeignScope/executor`, `TestOpenAPITenantMatrix/approveProposal` and `rejectProposal`. |
| AI / agent safety | [x] Execute with the approving user's permissions, never elevated agent permissions. | Implemented | Live approver membership and policy rechecked. `TestAgentSafetyFoundation`, `TestProposalMatrix`, `TestHandlerPreflightDoesNotGrantOwnership`. |
| AI / agent safety | [x] Sanitize rendered AI output: no external images or auto-loading links, escape HTML. | Implemented | DOMPurify and React text; internal source allow-list. `web/src/Proposals.test.tsx`, `web/src/Search.test.tsx`, `web/src/Meetings.test.tsx` and `web/src/Chat.test.tsx`. |
| AI / agent safety | [x] Per-workspace LLM quotas, per-request token caps, max agent steps, timeouts. | Implemented | Persistent shared budgets, token/step caps and deadlines. `TestCapsTimeoutAndMock`, `TestAgentSafetyFoundation`, `TestSearchIsolationAndCitations`, `TestMeetingsSecurityAndPipeline`. |
| Auth and sessions | [x] argon2id password hashing. Sessions in httpOnly, Secure, SameSite=Lax cookies. CSRF token on every state-changing request. Short-lived access + revocable refresh. | Implemented | `TestPasswordHash`, `TestCSRF`, `TestIdentitySecurity`, `TestOpenAPITenantMatrix` (missing/foreign CSRF and hostile Origin for every unsafe operation); browser helper validates every cookie flag. |
| Auth and sessions | [ ] Rate limit and lock out login, signup, password reset, invite and AI endpoints. | Partial | Shared public/pre-auth/per-action budgets, workspace AI budgets, bounded global limiter and proxy rate zone; persistent login lockout. `TestIdentitySecurity`, `TestChatRealtimeIsolation`, `TestGlobalRateBudgetBoundedAndRefills`, `TestSearchIsolationAndCitations`. Public password-reset delivery remains gated. |
| Auth and sessions | [x] Invite and reset tokens: random 256-bit, stored hashed, single use, short expiry. | Implemented | `TestTokens`, `TestIdentitySecurity`, `TestOpenAPITenantMatrix/acceptInvite`. Reset issuance remains internal; public delivery is not enabled. |
| Auth and sessions | [ ] If JWT is used: pin the algorithm, verify exp/aud/iss. | Not applicable | Opaque hashed sessions; no JWT accepted. |
| Authorization | [x] Every route, websocket message and job passes through policy.Can(). | Implemented | Role/action preflight rejects unknown actions; actual resource policy still runs in transactions. `TestDenyByDefault`, policy matrix tests, `TestEveryWorkerJobRejectsForeignScope`, `TestChatRealtimeIsolation`. |
| Authorization | [x] Request DTOs use explicit allow-lists of updatable fields (no mass assignment; users can never set their own role or workspace_id). | Implemented | Generated DTOs and strict decoding. `TestIdentitySecurity`, `TestOpenAPITenantMatrix`, `TestHandlerPreflightDoesNotGrantOwnership`. |
| WebSockets | [x] Validate Origin. Authenticate on connect, then authorize every subscribe and every inbound message. Channel names include workspace id. Set read size limits, per-user connection caps and idle timeouts. | Implemented | `TestChatRealtimeIsolation` covers cross-site handshakes, foreign subscribe/unsubscribe/typing, flood/size/connection limits and revocation. `TestFramesRejectCallerSelectedWorkspace` covers forged heartbeat/scope fields. |
| Integrations | [x] OAuth uses state + PKCE. Third-party tokens encrypted at rest (AES-GCM, key from env) and never logged. Minimal scopes. | Implemented for GitHub | Session-bound single-use state, S256, tenant/purpose-bound AES-GCM, no requested scopes; GitHub tests below. Other vendors remain gated. |
| Integrations | [x] Webhooks: verify HMAC signature with constant-time compare, check timestamp and reject replays (store delivery ids). | Implemented for GitHub with vendor limitation | Exact body HMAC, delivery plus body-digest deduplication, signed event timestamp freshness. GitHub does not sign a delivery timestamp header; see GitHub checklist. Generic verifier remains separate. |
| Integrations | [x] SSRF guard on any server-side fetch: block private, loopback, link-local and metadata IP ranges, re-check after redirects and DNS resolution. | Implemented | All real provider defaults use `pkg/safehttp`: public-IP allow checks, pinned DNS dials, proxy disabled, HTTPS/443 only, redirects refused. `TestSSRFBlockedRanges`, `TestSSRFDNSPinningAndMixedAnswers`, `TestSSRFRedirectsAndProtocols`. Arbitrary fetch features remain absent. |
| Audit | [x] Append-only audit_log: app role has INSERT only (REVOKE UPDATE, DELETE, TRUNCATE) plus a trigger that raises on UPDATE/DELETE. Each row stores a hash of the previous row. | Implemented | `TestTenantIsolationAndRoleSafety`, `TestIdentitySecurity`; authenticated `slipgraph audit verify` runs in CI after smoke tests via `scripts/audit_ci.py`. |
| Audit | [ ] Log: logins, role changes, invites, exports, proposals created/approved/rejected/ executed, every agent action, with actor (human or agent), workspace, before/after. | Partial | Current identity/project/task/dependency/risk/chat events include actor kind, workspace and metadata before/after. Proposal transitions, agent completion and executed task changes write immediate audit rows with actor kind and metadata before/after; exports remain gated. |
| Audit | [x] Redact secrets and tokens from logs and audit payloads. | Implemented | Typed slog allow-list and extended secret-key audit rejection. `TestRedaction`, `TestAllowedKeysCannotSmuggleSecretValues`, `TestPanicIsSanitized`, `TestIdentitySecurity`. |
| Files and web | [x] Uploads: max size, allow-listed types, magic-byte check, random object names, tenant-prefixed storage keys, never served from the app origin with active content. | Implemented | `TestMagicAndTranscriptValidation`, `TestDecodedSampleLimit`, `TestLocalScope`, `TestCancelledWriteLeavesNoFile`, `TestMeetingsSecurityAndPipeline`, `TestOpenAPITenantMatrix/getMeetingMedia`, `scripts/security_smoke.py`. |
| Files and web | [x] Strict CSP, security headers (HSTS, X-Content-Type-Options, frame-ancestors none, Referrer-Policy). Sanitize any rich text with DOMPurify. sqlc/parameterized queries only. | Implemented | 256-bit per-document CSPRNG nonces, strict-dynamic, no unsafe-inline/eval, no-store and no conditional HTML caching; proxy preserves backend sandbox CSP. `scripts/security_smoke.py`, `TestHealth`, frontend XSS tests and `TestDatabaseBoundaries`. |
| Files and web | [x] Secrets from environment only, never committed. CI runs govulncheck, npm audit, gosec, golangci-lint, and the tenant isolation suite. | Implemented | CI scans include ./pkg, both RLS test modes, security/nonce smoke and audit CLI verification. `make lint`; production managed secrets/TLS remain deployment requirements. |

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

Other agent features, non-GitHub OAuth/webhooks, arbitrary outbound fetches,
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


## Hardening verification and scope

| Threat | Mitigation | Executed test / gate | Status |
| --- | --- | --- | --- |
| A new route silently bypasses tenant tests | Read the actual OpenAPI methods/paths; require an exact reviewed case and compare registered Chi routes | `TestEveryOpenAPIOperationHasSecurityCase`, `TestNewEndpointFailsCoverageGate`, `TestRegisteredRoutesMatchOpenAPI` | Implemented |
| A foreign ID is rejected only because it is invalid or nonexistent | Seed real projects/tasks/channels/messages/proposals/runs/briefs/meetings/segments/files; require own reads to succeed and foreign resource reads/actions to return 404 | `TestOpenAPITenantMatrix` | Implemented |
| A collection leaks another tenant without accepting a tenant parameter | Distinct private canaries and every fixture ID; test lists/settings, forged headers and search/Ask while A has indexed data and B has none | `TestOpenAPITenantMatrix`, `TestSearchIsolationAndCitations` | Implemented |
| New consumer or proposal kind lacks a security case | Source/schema inventory plus manifest; runtime test must execute every declared case | `TestRegisteredJobsHaveExecutedSecurityCases`, `TestEveryWorkerJobRejectsForeignScope` | Implemented |
| A job merely fails validation after reading foreign data | Require not-found/source-unavailable errors, approved executable fixtures, valid service identities, and zero provider calls | `TestEveryWorkerJobRejectsForeignScope` | Implemented |
| RLS passes only because SQL happened to include filters | Compile-time `rlstest` rewrites business workspace equality guards while preserving parameter types, joins and transaction GUC; repeat complete HTTP/worker suite | `TestTenantFilterFaultInjection`, CI `integration,rlstest` run | Implemented |
| New tenant table lacks real isolation coverage | Enumerate every app heap/partition, require real A fixtures and ENABLE/FORCE RLS, then SELECT with no tenant WHERE; test foreign object writes and search invoker view | `TestEveryTenantTableRLSWithoutApplicationFilters` | Implemented |
| Global identity exception expands unnoticed | Catalog allow-list remains exactly the six approved identity tables | `TestEveryTenantTableRLSWithoutApplicationFilters`, `TestIdentitySecurity` | Implemented |
| Traffic evades limits before session resolution | Bounded global IP budget, shared pre-auth IP budget, authenticated action budgets, NGINX zone; existing feature budgets remain | `TestGlobalRateBudgetBoundedAndRefills`, `TestIdentitySecurity`, `TestChatRealtimeIsolation` | Implemented |
| Browser reuses a nonce or attacker chooses it | API crypto/rand nonce required by an internal proxy subrequest; HTML fails closed, has no-store and conditional caching disabled | `scripts/security_smoke.py` | Implemented |
| Reverse proxy weakens a private response | Preserve upstream CSP, including media sandbox; enforce additional edge headers | `scripts/security_smoke.py` | Implemented |
| Whitelisted log keys carry arbitrary credentials | Validate UUID, method, service and numeric attribute types; redact everything else including nested values | `TestAllowedKeysCannotSmuggleSecretValues`, `TestRedaction` | Implemented |
| Signed webhook is replayed concurrently or in another workspace | Bind workspace/provider/delivery/time/body; constant-time MAC, short freshness window and atomic INSERT-only tenant receipt | `TestWebhookSignatureBindingAndExpiry`, `TestWebhookReplayAndTenantScope` | Generic primitive plus separate GitHub adapter; other ingress gated |
| DNS rebinding, private/mapped IP or environment proxy bypass | Validate every resolved address, pin IP at dial, preserve TLS hostname validation, disable proxies, check every redirect/new dial | `TestSSRFBlockedRanges`, `TestSSRFDNSPinningAndMixedAnswers`, `TestSSRFRedirectsAndProtocols` | Implemented for current outbound HTTP defaults |

The OpenAPI inventory is `api/security-coverage.json`; the job inventory is
`api/job-security-coverage.json`. Public health/authentication/bootstrap endpoints
and own-user identity directories cannot correctly return 403 solely because a
caller belongs to B. Their cases instead prove public/own-user behavior, absent
foreign data, credential/ticket isolation and CSRF. Resource endpoints use real
foreign IDs and must deny access. No production environment variable removes SQL
filters; normal builds use `filters_normal.go`.

The RLS fault build deliberately does **not** remove ownership filters from the
six approved global identity tables. Those tables have no tenant RLS by explicit
design, so their own-user predicates are an independent mandatory boundary,
covered by `TestIdentitySecurity`. Business SQL through TenantTx loses its
workspace equality guards in the test build; the GUC, FORCE RLS, tenant-aware
joins and integrity constraints remain active.

## workspace_id source audit

| Boundary | Authoritative source | Review / guard |
| --- | --- | --- |
| REST and WebSocket handshake | Resolved opaque cookie session plus verified live membership | `auth.HTTP.Require`, `auth.Service.InTenant`; request workspace query keys are rejected, never read as scope |
| Workspace creation | Server-minted UUID | Signup/create-workspace input contains only allowed profile/name fields |
| Workspace switching / invite acceptance | Own-directory single-use ticket or hashed invite bound to authenticated email | `TestIdentitySecurity`; no body/URL workspace identity |
| GitHub webhook routing | AES-GCM capability authenticates the exact consumer session; auth verifies its workspace and live credential before business access | `BoundConsumer`, route digest, per-connection HMAC, GitHub isolation tests |
| Worker and consumers | Verified service session plus persisted requester/approver membership | `AuthorizeConsumer`, `JobMember`, `TestEveryWorkerJobRejectsForeignScope` |
| SQL, quotas, Redis and files | TenantTx.WorkspaceID or verified session/actor scope | Forced RLS, composite keys, prefixed broker/storage keys; catalog and storage tests |
| Audit CLI | Resolved admin session from stdin/environment, then central policy | No workspace argument; `scripts/audit_ci.py` |
| Future code | Literal request scope getters prohibited | `TestDatabaseBoundaries` / `TestGuardRejectsBypass`; `TestRequestContractsCannotSelectTenantIdentity` forbids scope fields in request schemas; manual review found only the rejecting Query.Has guard |

## Remaining gaps and deployment responsibilities

- **Only GitHub ingress is implemented, and is disabled until configured.** Other
  vendor protocols remain gated. The generic verifier is not a vendor adapter.
  GitHub supports public repositories only with no requested scopes; broader
  existing grants are rejected. Remote grant revocation and webhook installation
  are manual actions. GitHub has no signed delivery timestamp; see the exact
  freshness/deduplication boundary below.
- **Public password-reset email, exports and rich HTML are disabled.** Their
  delivery, authorization, audit and abuse controls must be implemented with those
  features; a foundation test is not a claim that absent delivery works.
- **Production deployment is not attested by local CI.** Managed secrets, HTTPS,
  database/Redis TLS, rotation, patched FFmpeg/base images, network restrictions,
  operational retention and alerting remain operator responsibilities. The direct
  Vite development server is not the nonce-protected production frontend.
- **Database administrators and a compromised runtime process remain trusted
  boundaries.** RLS depends on trusted binding of the transaction GUC. An admin can
  rewrite the chain and its stored head; independent export/anchoring of audit heads
  is still needed for evidence against a malicious database administrator.
- **Real provider and vendor services were not contacted by security tests.**
  HTTP contracts, DNS pinning and rejection paths use deterministic fixtures.
  Deployed CSP markup/headers and inert frontend rendering are tested; automated
  real-browser CSP violation reporting/evaluation is not yet part of CI.


## GitHub threat checklist

| Threat / required rule | Mitigation | Test / evidence | Status |
| --- | --- | --- | --- |
| OAuth login CSRF / callback swaps | Ten-minute single-use state hash bound to initiating session; exact callback; POST initiation CSRF | `TestGitHubOAuthWebhookAndDelayIsolation`, `TestOpenAPITenantMatrix` | Implemented |
| Authorization code interception | S256 PKCE with encrypted verifier, no plaintext persistence; fixed guarded token endpoint | `TestGitHubOAuthContractAndScopeDowngrade`, `TestGitHubOAuthWebhookAndDelayIsolation` | Implemented |
| Excessive access / private content | No requested scopes; reject broader returned grant and private repositories; one pinned repository ID | `TestGitHubOAuthContractAndScopeDowngrade` | Implemented; public repositories only |
| Token theft or cross-tenant ciphertext swap | Separate AES-256-GCM key; workspace/connection/purpose AAD; erase on disconnect | `TestGitHubEncryptionBoundToTenantAndPurpose`, `TestGitHubOAuthWebhookAndDelayIsolation` | Implemented |
| Forged webhook | Exact GitHub raw-body HMAC-SHA256, constant-time comparison; independent per-connection secret | `TestGitHubRawBodySignature`, `TestGitHubOAuthWebhookAndDelayIsolation` | Implemented |
| Delivery replay or unsigned delivery-header replacement | Unique delivery and signed-body digest, transactionally persisted before worker processing; concurrent replay fails | `TestGitHubOAuthWebhookAndDelayIsolation` | Implemented |
| Fabricated timestamp guarantee | Use authenticated event payload timestamps, seven-day window and future bound; ping uses receipt/digest dedupe | `TestGitHubOAuthWebhookAndDelayIsolation` | Implemented with vendor limitation; no signed timestamp header exists |
| Cross-workspace routing | Encrypted capability verified before consumer/session scope; connection route hash, repository ID/name and HMAC checked inside FORCE-RLS transaction | `TestGitHubOAuthWebhookAndDelayIsolation`, `TestEveryTenantTableRLSWithoutApplicationFilters` | Implemented |
| Forged task references or foreign jobs | Live connector admin and per-action policy; active tenant task/project lookup; composite links | `TestGitHubOAuthWebhookAndDelayIsolation`, `TestEveryWorkerJobRejectsForeignScope`, normal plus `rlstest` | Implemented |
| Prompt injection / unapproved task writes | External titles remain untrusted message content; ordinary worker detection and pending proposals only | `TestGitHubOAuthWebhookAndDelayIsolation` | Implemented |
| Secret leakage through logs/audit/UI | Fixed metadata DTOs only; no URL/body logs; status excludes credentials; one-time setup in memory; processed bodies cleared | `TestGitHubOAuthWebhookAndDelayIsolation`, `Integrations.test.tsx`, `scripts/github_proxy_smoke.py` | Implemented |
| Abuse / stale updates / repeated signals | One MiB ingress, per-connection/IP limits, bounded batches and links, per-item daily signal cap, update ordering | `TestOpenAPITenantMatrix`, GitHub workflow tests | Implemented |
| Remote grant removal, missed deliveries, key rotation | Manual revoke in GitHub; last-received-event staleness disclosed; independent key backup/rotation procedure required | Deployment runbook in README | Operational boundary |

GitHub integration configuration is admin-only. The callback GET is a narrowly
reviewed OAuth exception to browser mutation CSRF: exact session-bound, single-use
state replaces the header. Webhook POST uses vendor HMAC plus an authenticated
service route, never browser cookies or caller-selected workspace IDs. The approved
identity schema has not gained any global integration/business tables.


## Showcase safety

| Threat | Mitigation | Test / evidence | Status |
| --- | --- | --- | --- |
| Demo data or credentials sent to production | Showcase client refuses non-loopback origins, ignores proxies and refuses redirects | `ShowcaseFixtureTests.test_remote_origin_refused_before_network_or_credentials`; API demo run | Implemented |
| Static demo passwords or recordings enter Git | Random credentials stored only in Git/Docker-ignored user-restricted `.showcase/`; public artifacts exclude tokens | `ShowcaseFixtureTests.test_private_showcase_material_is_ignored`; local seed output prints paths only | Implemented |
| Attractive demo silently bypasses tenancy or human approval | Normal signup/login, directory tickets, CSRF and runtime policy/RLS; task versions remain unchanged pending approval | `scripts/showcase_demo.py`, `docs/SHOWCASE_RESULT.json`; ordinary two-workspace suites | Implemented |
| Broken fixture hides dependency failures | Sixty unique tasks, six milestones, DAG and all-workstream launch ancestry checks | `ShowcaseFixtureTests.test_all_workstreams_reach_launch_without_cycles`, `test_dates_and_milestones_are_valid_calendar_inputs` | Implemented |
| Invented benchmark or misleading model/screen recording claims | Actual isolated 100k measurement with recorded scope/resources; labeled fixed mocks, silent recording and real UI capture | `docs/SEARCH_BENCHMARK.json`, `docs/SHOWCASE.md`, captured GIF | Implemented |

The showcase is opt-in local tooling, not a public seed endpoint or an unauthenticated
fixture API. No production security controls are disabled. The benchmark's bulk
synthetic insert and index rebuild occur only in a disposable testcontainer, never
the running application's database. Runtime audit-table SELECT remains forbidden;
the walkthrough verifies the real chain rather than exposing raw audit rows.
