# Slipgraph architecture

Slipgraph is a multi-tenant team workspace with authenticated identity, projects,
tasks, chat, realtime presence, deterministic dependency analysis, and transactional audit delivery. Chat delay analysis, brief planning, and meeting extraction use human-reviewed proposals.
GitHub is opt-in; other external integrations remain disabled. Providers are opt-in; an offline mock supports the demo.

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
   channels, messages, briefs, meetings, transcript segments, decisions, agent runs, proposals, risk assessments, GitHub connections/events and remote PR/issue objects extend `app.objects` through `(workspace_id, id)` foreign keys.
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
   consumer executes approved changes atomically. The brief endpoint queues generation;
   chat creation queues delay detection without model calls in an HTTP or outbox transaction.
7. **Worker-only slow work.** Dependency calculations and audit delivery run in the
   worker. LLM calls and transcription use authorized, bounded worker jobs. Embedding and cited-answer calls use authorized search worker jobs. Redis chat fanout runs as an authorized outbox consumer.
   River infrastructure remains disabled and gated.
8. **OpenAPI contract.** `api/openapi.yaml` defines health, authentication, membership,
   projects, tasks, dependencies, risk reads, chat, presence, websocket frames, proposal review, agent runs, briefs, impact scenarios, meetings, private recordings, transcript evidence, hybrid search and cited answers. oapi-codegen produces Go interfaces
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
never message text; search consumers re-fetch canonical content under tenant policy.

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

Explicit plan requests require an authenticated human session; other internal enqueue
seams validate 1–8 source objects under tenant policy. The chat consumer resolves the
original author from the persisted message, never from model output or event payloads. Worker service sessions select scope; jobs recheck the requester's live
membership before calling a provider and before creating a pending proposal.
Executable model output supports reschedule_task, add_dependency, create_task,
change_assignee, create_plan, meeting_task and record_decision. Delay detection has a separate schema and is never
an executable proposal kind. Typed validators and existing task mutation helpers retain version,
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
output format with storage disabled; Anthropic uses Messages JSON output. Both move
unsupported schema bounds into descriptions while retaining full local validation.
Both use fixed HTTPS endpoints, reject redirects, limit response sizes and expose
no tool calls. Contracts follow the official [OpenAI structured output guide](https://developers.openai.com/api/docs/guides/structured-outputs)
and [Anthropic structured output guide](https://platform.claude.com/docs/en/build-with-claude/structured-outputs).

The inbox shows before/after fields, validated internal source links and review
controls. HTML is stripped and rendered as React text; external URLs/images never
load. Viewers cannot decide. Approval is not optimistic: a stale version/hash forces
review of fresh state. Tests cover hostile chat content, schema failure, quota
exhaustion, retries, cross-workspace endpoints/jobs, unapproved/expired/tampered/
replayed execution, revoked approver permission, concurrent execution and audit.

## Signature workflows

A `message.created` consumer queues `delay_detection` once per source event. It ignores
system notices, deleted messages and authors without current AgentRequest permission.
The consumer performs no model I/O. Automatic enqueue is capped at ten runs per author/
workspace/hour; all calls share the persistent workspace budgets. The worker pins the
message version, rechecks live author permission and source visibility before and after
model I/O, and terminalizes invalid queued inputs. Changed/deleted input cannot silently
produce proposals. Authenticated service sessions determine scope throughout.

The detection schema contains only is_delay, task_candidates, days, confidence and
an exact evidence_quote. Task cards/links take precedence; otherwise candidate names
(case insensitive exact matches) or IDs resolve only to active visible tenant tasks.
No fuzzy match silently chooses a task. A factual delay requires 1–365 calendar days
and a literal quote from the source. Confidence below 0.85 or anything other than one
resolved candidate produces an immutable system notice and needs_confirmation run,
without an assessment or proposals. Only the message author can confirm one of the
resolved candidates and its duration through a CSRF-protected endpoint. Confirmation
queues deterministic worker analysis without another model call. Unknown targets
require a new message containing a task card. Confirmation is not approval.

Deterministic risk.Analyze evaluates a new due date equal to the task's current due date
plus the reported days. It creates an immutable hypothetical assessment and at most
100 reschedule proposals (including the source task) for graphs of at most 10,000 tasks.
Each proposal pins that task's version. Affected milestone/launch warnings are assessment
annotations, not changes to milestone flags or project dates. No task or dependency changes
until human approval. The channel notice has fixed server-authored text and validated task
links; it cannot be edited/deleted as a human message or recursively trigger detection.

Scenario assessments have a tenant FK to their run. Actual risk consumers and `/risk`
exclude them, so an unapproved scenario never replaces the committed graph forecast.
The timeline offers scenario selection, an optional forecast overlay, before/after dates,
slack explanations and launch warnings, and flags graph versions that have become stale.
Results, notices, proposals, sources, events and audit commit together. Failed generation
retains its quota reservations but rolls back all partial outputs.

`POST /plans` stores a bounded, immutable tenant brief and queues a create_plan run.
Its single proposal contains a project, 1–20 tasks with local keys, and at most 60
acyclic dependencies. Strict schemas reject extra properties and any existing object IDs;
validators reject duplicate keys/edges, missing nodes, invalid dates and cycles. The
executor revalidates using the approver's ProjectWrite and TaskWrite permissions, then
creates the project, tasks, links, execution marker, events and immediate audit rows in
one transaction. Replays cannot create a second project. Briefs and run results remain
workspace-scoped; the original brief is available as an internal source link.

Use `LLM_PROVIDER=mock` on API and worker for the reproducible offline demo. The mock
recognizes the demo delay sentence and returns a fixed three-task launch plan, clearly
labeled in the UI; it is not a general interpreter. Real adapters use deployment-selected
models and explicit price ceilings. No live API keys are required by CI, which also runs
`scripts/signature_smoke.py` through the production proxy and worker in mock mode.

## Meetings and recordings

Meetings and immutable transcript segments extend objects; generic belongs_to links
connect meetings to projects and segments to meetings. Approved decisions also extend
objects and source-link the segment. All three tables have ENABLE/FORCE RLS, explicit
runtime grants and composite tenant keys. Workspace identity always comes from the
verified session or consumer identity. Member/admin writers upload; viewers read.

The multipart endpoint accepts a title, project_id resource selector and recording.
It rejects unknown/duplicate fields, limits the recording to 25,000,000 bytes and the
entire multipart body to that limit plus 16 KiB, allows WAV/MP3/MP4/WebM, and checks the file signature
against the declared media type. Supplied filenames are ignored. Four streaming upload
slots bound concurrent per-process I/O. Files use random UUID names under workspace UUID
prefixes through internal/filestore.Store. The local adapter checks scope before opening
or deleting, uses exclusive creates/private permissions and never maps keys to public URLs.
A failed registration removes the staged file. Data and credential volumes are preserved.

The worker rechecks service policy, requester membership, project visibility and minute
budget before claiming a job. Its shared seven-minute lease and conditional status
transitions prevent duplicate transcription. A cancelled/crashed/uncertain attempt is
terminal rather than automatically charged again. A fixed ffprobe command checks the stream; a bounded FFmpeg decode counts audio
samples to measure billable duration without trusting container metadata. Both have
deadlines, limited output/allocation and local-only input protocols; filenames
and URLs never become shell arguments. The child runs as the restricted runtime user.
Recordings must contain audio and be no longer than 60 minutes (or the configured daily
budget if lower). Providers receive bounded streams behind a Transcriber interface.

The default budget is 600 minutes per workspace over 24 hours. Uploads reserve up to
60 minutes; claims renew that reservation under a workspace lock, so old queued jobs
cannot evade the current budget. The trusted decoded-sample count reduces it to rounded-up
actual minutes before any provider call. Unknown failures retain their reservation.
Invalid recordings never reach the provider. Uploads are also limited to ten per
requester/workspace/hour. Media duration and quota authority never come from the browser.

Transcription stores ordered start/end milliseconds, speaker labels and untrusted text.
Long source text is split into bounded blocks retaining the provider's original interval;
this does not invent finer timestamps. At most 2,000 segments are persisted. Extraction
runs contain up to eight segments and remain below the LLM prompt limit. They share the
existing LLM budgets, retries, output schemas and job authorization. Transcript text never
enters the instruction channel. A model can reference only its supplied segment IDs and
must quote literal source text. The server supplies the meeting/project and timestamps.

Extraction creates pending meeting_task and record_decision proposals, each hashing its
payload and evidence. Approval and execution revalidate the exact segment, quote, timestamp,
tenant, approver permissions and active project/meeting. Approved tasks and decisions retain source links to their transcript segments. They are created
only by the executor, atomically with events, the execution marker and audit. Transcript
storage and extraction queueing also commit together. No partial transcript survives a
failed transaction. A transcript remains readable if later extraction fails or exhausts
its model budget; the meeting page shows that state.

Recordings are fetched through authenticated tenant reads as attachment/octet-stream with
nosniff, private no-store and same-origin resource policy. The UI creates a typed media Blob
for its audio/video element and revokes it on navigation; CSP allows blob media only.
No uploaded HTML, SVG, script or arbitrary URL is rendered. Source links resolve through
a meeting-scoped segment endpoint; click-to-seek uses that segment's real start time,
not an untrusted URL time. Search and pagination apply tenant predicates in SQL.

Both providers default disabled. See [meeting configuration](MEETINGS.md) for offline
mock behavior, local storage, format/timestamp limitations and explicit real-provider setup.

## Search and retrieval

Search uses a FORCE-RLS embedding table, a security-invoker live document view,
HNSW cosine candidates and GIN full-text candidates. Both SQL branches carry an
explicit workspace predicate; live objects, parents and content hashes suppress
stale/deleted content before per-object central policy checks. Outbox consumers
queue bounded worker embedding jobs; generation/hash checks make retries safe.

Search and Ask requests are own-user tenant jobs, never caller-selected scope.
Workers reverify service and requester permissions, share agent token/cost budgets,
and keep network calls outside transactions. Ask receives only bounded untrusted
snippets and returns strict structured claims. Every cited ID must exist in the
retrieved set. Completion and reads recheck source visibility/hashes; changed
sources invalidate cached answers. The Ctrl+K palette renders text and code-built
internal links. See [SEARCH.md](SEARCH.md) for limits, configuration and benchmark
semantics. Default providers remain disabled; mock behavior is explicitly labeled.


## Security verification and browser boundary

OpenAPI methods/paths and registered routes are compared with the reviewed security
inventory. Every resource route has real A fixtures and foreign B attempts;
unscoped collections and public/identity operations have explicit privacy cases.
A source/schema inventory similarly gates consumer and proposal job kinds.

CI runs the entire Docker-backed tenant suite normally and with the compile-time
`rlstest` fault injection. That build removes business workspace equality guards
from TenantTx SQL, preserving bind parameters and the transaction GUC so FORCE RLS
must independently hold. Normal builds contain no runtime switch for this behavior.
The catalog suite checks every tenant table and the exact global identity exception.

The production frontend requires a fresh 256-bit crypto/rand nonce from the
rate-limited public document bootstrap before serving HTML. Vite nonce placeholders
are replaced by NGINX; strict-dynamic permits only the trusted script chain.
No-store, disabled conditional HTML caching and backend CSP preservation avoid
nonce reuse and loss of private recording sandbox headers. The API remains
default-src none. Role/action preflight rejects unknown actions; actual ownership
and resource policy remain mandatory in the tenant transaction.

Outbound provider defaults use a shared HTTPS guard with public-IP checks, DNS
pinning, TLS hostname verification and no environment proxy or redirects.
The generic, workspace-bound webhook verifier has atomic RLS replay receipts but
exposes no ingress endpoint or vendor adapter. These gated integration boundaries
and remaining deployment responsibilities are detailed in THREAT_MODEL.md.


## GitHub integration

GitHub OAuth is an explicit admin action. It requests **no scopes**, so this
first adapter supports public repositories only. OAuth state is 256-bit random,
hashed in the tenant row, single-use, expires in ten minutes and is bound to the
exact initiating human session. S256 PKCE is mandatory. The verifier and access
credential use a separate API-only configured AES-256-GCM key with random nonces and
workspace/connection/purpose authenticated data. A pre-existing broader GitHub
OAuth grant is rejected; revoke that grant on GitHub before retrying. Callback
exchange uses fixed endpoints and the guarded HTTP client. Disconnect erases local
credentials and webhook routing; remote grant revocation is a separate GitHub action.

Connections, durable ingress and remote PR/issue objects have ENABLE/FORCE RLS,
composite keys and explicit runtime grants. Admin-only settings reveal no stored
credentials. Configuring one public repository and a workspace channel returns a
one-time private callback URL and independent webhook secret for manual installation
in repository settings. Saving rotates both. Repository identity is pinned to the
server-verified numeric ID and full name; replacing that repository is deliberately
unsupported by this initial adapter. An opaque AES-GCM routing capability binds
workspace, connection, random nonce and an authenticated consumer session. A route
is never interpreted as a workspace UUID. Auth re-verifies the bound service session
before opening its tenant transaction; the stored route digest must still match.

GitHub's actual signature is HMAC-SHA256 of the raw body, compared in constant time.
GitHub does not sign a delivery timestamp or the delivery header. Consequently the
adapter persistently deduplicates **both** delivery UUID and signed-body SHA-256,
including concurrent requests and changed delivery headers. Non-ping payloads require
a signed PR/issue update or check-run timestamp within seven days (30 seconds future
skew). Ping uses authenticated content plus permanent deduplication, not a fabricated
signed header. Old redeliveries need a fresh GitHub event; the freshness policy can
intentionally reject delayed historical deliveries. Ingress is size/rate bounded and
performs no model/network work. Event logs contain type, receipt time and outcome only;
only modeled evidence is retained (ping configuration is discarded); processed payloads are cleared. Audit snapshots contain IDs and fixed metadata.
NGINX disables URI-bearing error logs for the webhook and OAuth callback locations;
structured API status/request-ID logs remain available without paths or query strings.

The worker rechecks live connector-admin membership and per-action policy. Issue-to-task
mapping is explicit; PR `closes #12` follows generic links, while task UUIDs in branch
names resolve only active visible tasks/projects. Failing check runs, blocked PRs and
known open PRs stale since their last received update create bounded, idempotent chat
signals with task/source links. The configured delay is a labeled scenario estimate,
not a measured duration. Task dates do not change. The existing message-created
consumer treats external text as untrusted, queues delay detection, computes deterministic
risk and generates pending proposals; normal human approval/executor checks still apply.
Staleness relies on received events, not periodic remote polling, so missing webhook
updates may produce a stale scenario. At most one signal per remote item per day is
emitted. A disabled model provider leaves signals in chat without agent proposals.

Tests include OAuth state/session/replay/PKCE, ciphertext swapping, actual GitHub
signature semantics, concurrent/body-hash replay, signed timestamp freshness,
foreign routing/references/jobs, pending-only risk flow, hostile external titles,
admin/CSRF boundaries, inert settings UI, and secret-free logs/audit. All new tables
and endpoints join the OpenAPI-driven and unfiltered-RLS gates.

Protocol references: [GitHub OAuth and S256 PKCE](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps),
[webhook signature verification](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries),
and [event payloads and check-run repository availability](https://docs.github.com/en/webhooks/webhook-events-and-payloads).

The worker receives no GitHub OAuth client secret, token decryption key or HTTP client.
It only processes authenticated ingress already persisted under tenant RLS.


## Showcase and reproducible measurements

The optional `docker-compose.showcase.yml` profile selects only mock LLM,
transcription and embedding providers. `make seed` creates fictional data through
normal session/CSRF APIs: 60 tasks, 63 dependencies, two channels, a mock recording
and transcript, plus an independent second identity/workspace. It never accepts
request-supplied tenant identity or bypasses policy, RLS, quotas or approval.
Seeding is resumable, paced within ordinary limits and does not delete existing data.
Random local credentials and live manifests/captures remain in ignored `.showcase/`
with local-user permissions; public reports contain only synthetic counts and dates.

`make demo` asserts the complete three-task mock workflow, pending-only state,
exactly-once version changes, real audit CLI verification, validated cited answers
and cross-identity read/search/Ask denial. The realistic launch fixture remains
separate from the intentionally bounded three-task mock plan. The sample silent
recording and its mock transcript are labeled; no real transcription claim is made.

The 100k-task benchmark uses an isolated testcontainer and runtime TenantTx seeding,
with fixture-only index maintenance. Measurements include retrieval, tenant and live
session/policy checks but exclude HTTP, worker queue and provider latency. The JSON
report records warmups, samples, dimensions, distribution, host and Docker resources.
The README GIF consists of actual paused UI states, compressed in time, not fabricated
screens or a claim of real-model behavior. See docs/SHOWCASE.md for reproduction.
