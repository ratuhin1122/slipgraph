# Slipgraph architecture

Slipgraph is a multi-tenant team workspace with authenticated identity, projects,
tasks, deterministic dependency analysis, and transactional audit delivery. Agents,
proposals, external integrations, and AI providers remain disabled.

## Runtime and boundaries

One Go module contains separate API and worker binaries, owner-only goose migrations,
and the authenticated `slipgraph audit verify` command. Domain packages live under
`internal/`; infrastructure lives under `internal/platform/`. Postgres 16 with
pgvector holds durable state. Redis currently supplies infrastructure readiness;
no tenant content or unscoped domain cache is stored there.

## The nine foundations

1. **Tenant schema and RLS.** Every business table in `app` has `workspace_id`,
   ENABLE/FORCE RLS, explicit grants, and tenant-aware composite keys. Runtime is
   `slipgraph_app`, never an owner, superuser, privileged member, or BYPASSRLS role.
   `database.WithTenant` uses transaction-local `set_config(..., true)` and supplies
   a sealed `TenantTx` capability that expires after its callback. Tenant identity
   is resolved from an authenticated session and verified membership. The approved
   global identity exception is described below; it is not a business-data escape.
2. **Generic objects.** Workspaces, tenant member projections, projects, tasks, and
   risk assessments extend `app.objects` through `(workspace_id, id)` foreign keys.
   Application IDs are UUIDv7. Mutable objects use versions and soft deletion.
3. **Generic links.** `app.links` stores task-to-project `belongs_to`, task-to-member
   `owned_by`, task-to-blocker `blocked_by`, and assessment-to-project relationships.
   Composite foreign keys prevent cross-workspace references. Relationship columns
   are not duplicated in feature tables. Ownership IDs refer to tenant member objects.
4. **Central policy.** `policy.Can` fails closed for unknown actions. Public health
   and limited authentication entry points are explicit. Verified admins manage
   invites and other members' roles; admins and members edit projects/tasks; viewers
   read. A user cannot change their own role. Every tenant route and worker consumer
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
   No agent, proposal, approval, or executor route is enabled in this implementation.
7. **Worker-only slow work.** Dependency calculations and audit delivery run in the
   worker. LLM calls, transcription, and embeddings must also be authorized worker
   jobs when introduced. River and websocket infrastructure are not enabled; their
   isolation designs and tests remain feature gates.
8. **OpenAPI contract.** `api/openapi.yaml` defines health, authentication, membership,
   projects, tasks, dependencies, and risk reads. oapi-codegen produces Go interfaces
   and DTOs; openapi-typescript and openapi-fetch supply the strict TypeScript client.
   `make gen` also generates sqlc bindings. Handlers reject unknown JSON fields,
   missing required fields, invalid nulls, trailing input, and oversized bodies.
9. **LLM provider boundary.** Future providers must implement the provider interface,
   use deterministic mocks in tests, treat workspace content as untrusted data,
   validate JSON outputs, and enforce quotas, token/step limits, and timeouts.
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
escapes text; rich text must add DOMPurify before activation. Only the configured
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
