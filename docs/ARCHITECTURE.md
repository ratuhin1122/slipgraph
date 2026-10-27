# Slipgraph architecture

Slipgraph is a multi-tenant team workspace with authenticated identity, projects,
tasks, chat, realtime presence, deterministic dependency analysis, and transactional audit delivery. The agent safety foundation supports human-reviewed proposals; real agent features
and external integrations remain disabled. Provider calls are disabled by default.

## Runtime and boundaries

One Go module contains separate API and worker binaries, owner-only goose migrations,
and the authenticated `slipgraph audit verify` command. Domain packages live under
`internal/`; infrastructure lives under `internal/platform/`. Postgres 16 with
pgvector holds durable state. Redis supplies workspace-prefixed pub/sub invalidations, connection leases, flood
budgets, and expiring presence. Message bodies remain in Postgres.

## The nine foundations

1. **Tenant schema and RLS.** Every business table in `app` has `workspace_id`,
   ENABLE/FORCE RLS, explicit grants, and tenant-aware composite keys. Runtime is
   `slipgraph_app`, never an owner, superuser, privileged member, or BYPASSRLS role.
   `database.WithTenant` uses transaction-local `set_config(..., true)` and supplies
   a sealed `TenantTx` capability that expires after its callback. Tenant identity
   is resolved from an authenticated session and verified membership. The approved
   global identity exception is described below; it is not a business-data escape.
2. **Generic objects.** Workspaces, tenant member projections, projects, tasks,
   channels, messages, agent runs, proposals, and risk assessments extend `app.objects` through `(workspace_id, id)` foreign keys.
   Application IDs are UUIDv7. Mutable objects use versions and soft deletion.
3. **Generic links.** `app.links` stores task-to-project `belongs_to`, task-to-member
   `owned_by`, task-to-blocker `blocked_by`, assessment-to-project relationships, message-to-channel `belongs_to`,
   message-to-member `mentions`, message-to-task `references`, and run/proposal-to-object `source` links.
   Proposal `source_object_ids` are derived from these canonical links, not duplicated arrays.
   Composite foreign keys prevent cross-workspace references. The requested `messages.thread_id` is a composite tenant-aware self-reference;
   other relationship columns are not duplicated in feature tables. Ownership IDs refer to tenant member objects.
4. **Central policy.** `policy.Can` fails closed for unknown actions. Public health
   and limited authentication entry points are explicit. Verified admins manage
   invites and other members' roles; admins and members edit projects/tasks; viewers
   read. Chat channels are workspace-wide; writers post and edit/delete only their
   own messages, including admins. A user cannot change their own role. Every tenant route and worker consumer
   rechecks authorization; job actors come from authenticated service sessions.
5. **Transactional events.** Business mutations and `app.events` outbox records
   commit together. The dispatcher locks scoped batches with SKIP LOCKED, rechecks
   service identity, and commits consumer effects, delivery receipts, and publication
   together. Retries provide at-least-once delivery with idempotent consumers.
   Audit records every event. Risk coalesces superseded graph versions and emits
   `risk.assessed`; it never changes task dates automatically.
6. **Human-approved proposals.** Agents may only create validated pending proposals.
   Before execution, verify human approval, current permissions, expiry, unchanged
   payload hash, and exactly-once application using the approver's authority.
   The proposals inbox exposes reads, approval and rejection; an authorized outbox
   consumer executes approved changes atomically. There is no public generation endpoint.
7. **Worker-only slow work.** Dependency calculations and audit delivery run in the
   worker. LLM calls use authorized, bounded worker jobs. Transcription and embeddings
   remain gated. Redis chat fanout runs as an authorized outbox consumer.
   River infrastructure remains disabled and gated.
8. **OpenAPI contract.** `api/openapi.yaml` defines health, authentication, membership,
   projects, tasks, dependencies, risk reads, chat, presence, websocket frames, and proposal review. oapi-codegen produces Go interfaces
   and DTOs; openapi-typescript and openapi-fetch supply the strict TypeScript client.
   `make gen` also generates sqlc bindings. Handlers reject unknown JSON fields,
   missing required fields, invalid nulls, trailing input, and oversized bodies.
9. **LLM provider boundary.** OpenAI, Anthropic and deterministic mock adapters implement `Complete` with
   JSON schema output. Worker orchestration treats content as untrusted data,
   validates output locally, and enforces workspace quotas and bounded calls.
   Dependency risk requires no model or external service.

## Approved identity control plane

Only these six tables are exempt from tenant RLS: `identity.users`, `credentials`,
`sessions`, `memberships`, `invites`, and `password_resets`. Only `internal/auth`
queries them. They contain identity and authorization metadata, never workspace
business content, task data, or AI content. Runtime privileges are explicitly
limited. Migration bookkeeping in `migrations` remains inaccessible infrastructure.

Directory and human session queries filter the authenticated user's ID. Member
administration reads the separate RLS-protected `app.members` projection; directory
and projection changes commit atomically. Session resolution verifies membership,
and every tenant transaction rechecks the live session and tenant member projection.
Signup/new-workspace creation mints a server-side workspace ID. Switching uses a
short-lived, single-use ticket minted from the user's own membership directory;
invite acceptance verifies the hashed token and matching authenticated email.
Requests never supply authoritative workspace IDs.

Passwords use Argon2id (64 MiB, three iterations, parallelism two, random salt).
Login has shared rate budgets, five-failure lockout, and bounded hashing concurrency.
Sessions store only hashes of opaque 256-bit access/refresh tokens. Access expires
in 15 minutes, refresh in seven days. Rotation revokes the previous access session;
refresh-token reuse revokes the family. Cookies are HttpOnly, Secure, SameSite=Lax,
Path=/, and host-only. Unsafe HTTP requests require exact Origin plus a session-bound
CSRF token, including signup/login. All auth responses carrying credentials are
no-store. Invites expire in 24 hours; internal reset tokens in 15 minutes, both
hashed and single-use. Reset issuance/delivery is internal; no public forgot-password
email or recovery flow is exposed. Invites are manually shared by an authorized admin.

The worker authenticates user-less consumer session records using the deployment
key. It rechecks revocation, expiry, scope, and central policy in every batch.
Rate-budget counters and switch tickets contain identity metadata only and use
sessions rather than adding an unapproved global table.

## Projects and risk semantics

Project/task writes use explicit allow-lists and a required current version;
stale writes return 409. Project graph versions independently track planning changes.
Project-scoped advisory locks serialize graph mutation and cycle checks, including
opposite concurrent edge additions. Deleted project/task objects are hidden from
reads and graph loading; events and audit history remain. Cancelled tasks are excluded
from risk; completed tasks have fixed actual finish times. Comments are a UI placeholder.

Dependencies are finish-to-start. Dates use UTC minute precision, estimates are
minutes, and delays are absolute totals in 24-hour calendar days. No work-calendar,
resource leveling, probabilistic model, or LLM is implied. The loader uses a scoped
recursive CTE for downstream closure and loads the project graph for critical path.
The pure engine validates and topologically sorts iteratively, then runs forward
and backward passes for earliest dates, float, forecast critical path, affected tasks,
slack consumed, milestone/launch slip, and risk levels. Stable ID ordering makes
results deterministic. A due-date edit sets a forecast finish floor; estimates and
start constraints define the baseline. A delay report replaces the previous total.
Assessments include algorithm and graph versions. Stale results are labeled while
the worker recomputes; task records are not silently rescheduled.

The UI offers Kanban, table, dependency-arrow timeline, and a task drawer. For usable
rendering, columns show up to 100 tasks, table 300, and timeline 200, with explicit
limits shown. The engine benchmark covers 10,000 tasks and fails above 200 ms.

## Audit, secrets, and deployment

The runtime has INSERT-only audit grants. A trigger blocks UPDATE/DELETE/TRUNCATE,
including owner attempts. A scoped security-definer append function serializes each
workspace chain and assigns sequence, previous hash, and a SHA-256 digest of canonical
JSON including actor kind and timestamp. A narrow verification function returns only
validity/count/head; the Go command and API require admin policy. Hash chains detect
tampering relative to trusted heads; a database administrator remains a trust boundary.
Snapshots use explicit metadata fields and omit free-text content and credentials.

Compose generates random database, Redis, and auth keys in named credential volumes.
API/worker cannot read owner or administrator credentials. A dedicated bootstrap
service installs approved schemas/extensions for both fresh and existing volumes;
owner-only migrations precede runtime startup. Never discard credentials independently
of persistent database volumes. Production requires managed secrets, TLS, verified
Postgres TLS, Redis TLS, and a correctly configured public browser origin.

NGINX serves same-origin assets/API with strict CSP and Docker DNS refresh. React
escapes text; chat strips HTML with DOMPurify and renders React text nodes and
server-validated internal task cards. There is no raw HTML or external media loading. Only the configured
trusted proxy may supply X-Real-IP; NGINX overwrites it. JSON slog uses an allow-list
and redacts arbitrary errors, bodies, URLs, and secrets. Request IDs are generated
server-side; timeouts, recovery, and graceful shutdown bound service work.

## Verification

`make build`, `make test`, and `make lint` cover Go/React checks. Docker-backed suites
run separately for database invariants and real identity/project HTTP flows. They
prove two-workspace endpoint/job isolation, directory privacy, CSRF, lockout,
fixation/rotation, invite expiry/reuse, self-role denial, append-only audit, and role
startup rejection. Pure graph tests cover diamonds, cycles, completed nodes,
determinism, and the 10k performance budget. CI also checks generated drift,
vulnerability scans, image builds, Compose readiness, and API address replacement.

## Chat and realtime

Channels and messages extend objects with forced RLS and composite tenant keys.
Channels are workspace-wide, not private. Root messages and one-level threads use
UUIDv7 keyset pagination (50 default, 100 maximum). Posts accept a client UUID for
idempotent retries; edits/deletes require the current version. Deleted messages
become content-free tombstones so replies remain accessible. Mentions resolve
only tenant member objects. Task URIs resolve active tasks and projects in the
same tenant before inserting links. Events contain IDs, versions and byte counts,
never message text; future search consumers must re-fetch under tenant policy.

WebSockets use coder/websocket, exact configured Origin and secure session cookies.
URL credentials and caller-selected scope are rejected. Every application frame,
heartbeat, subscription and outbound notification rechecks the live session and
central policy. Revocation/role changes apply on the next frame or server heartbeat.
Frames are strict allow-listed JSON, text only, at most 4096 bytes; Redis enforces
30 frames/10 seconds per user/workspace and three live connections across replicas.
Control frames have a separate 64/10-second cap. REST posts have a 30/minute budget
and 8000-byte limit. Each connection has at most eight subscriptions, a 75-second
application idle deadline and a 20-second ping heartbeat. Failed pings close after
five seconds; connection/presence leases expire after 60 seconds, typing after five.
Shutdown explicitly closes hijacked connections and Redis subscriptions.

Every Redis key/topic starts with the authenticated workspace UUID. Presence stores
user/connection IDs and expiry only; display names come from the authorized tenant
member projection. Fanout resolves the event's real tenant object, never trusts
payload routing, and emits stable event IDs without content. Redis pub/sub is
transient: outbox retries may duplicate notifications and disconnected subscribers
can miss them. Clients deduplicate notifications, reconnect with bounded exponential
backoff and jitter, and reload canonical REST state on reconnect (also polling).
Optimistic sends retain their client UUID when retried. Session rotation reconnects
sockets; workspace switching clears scoped caches and closes old connections.

Docker integration checks cover every chat endpoint, subscriptions, fanout, unfiltered
RLS, exact Origin, missing cookies, connection/frame/post limits, oversize frames,
revocation, own-message permissions, and presence expiry. Frontend checks cover
inert XSS payloads, internal task cards, backoff and idempotent optimistic retries.

## Agent safety foundation

`internal/llm` has no domain/database dependency or write tools. Prompt assembly
JSON-escapes workspace text in an explicit untrusted data block; fixed system
instructions never contain workspace content. Strict local JSON Schema validation
rejects unknown fields, duplicate keys, malformed values and remote schema references.
Delimiters are defense in depth: only code, never model text, grants authority.

The internal enqueue seam requires an authenticated human session and validates
1–8 source objects under tenant policy. There is no API or UI for requesting an
agent run. Worker service sessions select scope; jobs recheck the requester's live
membership before calling a provider and before creating a pending proposal.
Model output supports exactly reschedule_task, add_dependency, create_task and
change_assignee. Typed validators and existing task mutation helpers retain version,
cycle, active-object, assignee and tenant checks. Model text cannot set approval,
workspace identity, permissions, executor kind, or SQL.

`app.agent_runs` and `app.proposals` extend objects with forced RLS. Source IDs use
composite `source` links. The review hash binds workspace, proposal ID, kind,
canonical payload, before/after preview, sorted source IDs, requester, run and expiry.
Approval requires the displayed version and hash and records an independent
approval hash. The worker locks the proposal, verifies pending approval state,
expiry (again at commit), hash, live approver permissions and tenant references,
then applies the mutation, marks executed, and writes events and append-only audit
rows in one transaction. Concurrent/replayed executions cannot apply twice.
Permanent failures become failed/expired; transient transaction failures retry.
Outbox audit delivery remains idempotent with the immediate audit rows.

Defaults are 100,000 reserved tokens and 1,000,000 micro-USD per workspace per
rolling 24 hours, 2,048 output tokens per call, at most three attempts and a
30-second call deadline. Enqueue is limited to ten requests per user/workspace/hour.
Workspace advisory locks serialize quota reservations. Encoded prompt/schema byte
counts plus overhead conservatively reserve input and output before each call;
ambiguous failures retain reservations. Usage and configured price estimates are
logged without prompts, output or credentials. These are application budget
estimates, not a provider billing reconciliation. A five-minute run lease terminalizes
crashed jobs without retrying an unknown charge; expiry transitions are audited.

`LLM_PROVIDER=disabled` is the default. Explicit worker-only model/key configuration
and conservative per-token price ceilings are required to enable an adapter. No
live provider requests are part of tests. OpenAI uses the Responses API structured
output format with storage disabled; Anthropic uses Messages JSON output, moving
unsupported bounds into descriptions while retaining full local validation.
Both use fixed HTTPS endpoints, reject redirects, limit response sizes and expose
no tool calls. Contracts follow the official [OpenAI structured output guide](https://developers.openai.com/api/docs/guides/structured-outputs)
and [Anthropic structured output guide](https://platform.claude.com/docs/en/build-with-claude/structured-outputs).

The inbox shows before/after fields, validated internal source links and review
controls. HTML is stripped and rendered as React text; external URLs/images never
load. Viewers cannot decide. Approval is not optimistic: a stale version/hash forces
review of fresh state. Tests cover hostile chat content, schema failure, quota
exhaustion, retries, cross-workspace endpoints/jobs, unapproved/expired/tampered/
replayed execution, revoked approver permission, concurrent execution and audit.
