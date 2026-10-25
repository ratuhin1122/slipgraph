# Slipgraph architecture

Slipgraph is a multi-tenant AI team workspace. A reported delay will traverse task
dependencies and produce a proposal. People approve a specific proposal before an
executor can change state. This repository currently implements the foundation and
public readiness route; it does not implement authentication or team features.

## Runtime and boundaries

One Go module contains separate `cmd/api` and `cmd/worker` binaries and a one-shot
`cmd/migrate`. Domain modules under `internal/` have package documentation and no
public business endpoints yet. Infrastructure lives under `internal/platform/`.
No `pkg/` is needed until there is a deliberate externally reusable API.

Postgres 16 + pgvector stores state; Redis is reserved for scoped pub/sub, rate
limits, and presence. The worker currently serves readiness and verifies its
runtime role. River jobs and coder/websocket handlers must be added with their
feature, policy checks, tenant tests, and outbox consumers; no jobs are registered
and no websocket endpoint is exposed by this scaffold.

## The nine foundations

1. **Tenant schema and RLS.** Every tenant table has `workspace_id`, ENABLE and FORCE
   RLS, and USING/WITH CHECK policies. `database.WithTenant` sets
   `app.workspace_id` with transaction-local `set_config(..., true)` (the parameterized
   equivalent of SET LOCAL). Callers must supply a workspace resolved from an
   authenticated session, never a request field. No tenant endpoints exist yet.
2. **Generic objects.** `app.objects` supplies UUID IDs, workspace, type, creator,
   UTC timestamps, and soft deletion. Future tasks, messages, docs, meetings, and
   transcripts extend it through composite `(workspace_id, id)` foreign keys.
   New application IDs use `uuid.NewV7`; no serial tenant IDs are introduced.
3. **Generic links.** `app.links` represents blocked_by, mentions, derived_from,
   and other relations. Composite foreign keys prevent cross-workspace edges.
   Do not add feature-specific relationship columns.
4. **Central policy.** `policy.Can(ctx, actor, action, resource)` denies by default.
   Only the public health-read action exists. Every future HTTP route, websocket
   message, job, and proposal executor must call this boundary. Actors are derived
   from trusted session resolution; agent roles never imply execution authority.
5. **Transactional events.** `app.outbox` stores tenant-scoped events. Every future
   domain mutation and its event must commit in the same tenant transaction.
   Audit, notification, indexing, risk, and realtime consumers will be separate
   handlers with idempotency, tenant checks, and policy checks. The scaffold has
   no domain mutation endpoints and no event publisher yet.
6. **Human-approved proposals.** Agents may only produce validated pending
   proposals. A human approves a specific proposal ID. The executor must verify
   approval, approver policy, expiry, approved payload hash, and single execution,
   and execute using the approver's permissions. These modules are reserved; no
   agent or executor exists yet.
7. **Worker-only slow work.** LLM calls, transcription, and embeddings belong in
   River worker jobs, never request handlers. Future queue payloads carry trusted
   workspace identity and are re-authorized on execution. Infrastructure queue
   tables require an explicit isolation design before adding River migrations.
8. **OpenAPI contract.** `api/openapi.yaml` defines `/healthz`. oapi-codegen produces
   the strict Go server interface; openapi-typescript generates the frontend schema
   and openapi-fetch supplies its typed client. `make gen` regenerates Go, SQL, and
   TypeScript outputs; CI checks for drift. Never hand-edit generated files.
9. **LLM provider boundary.** Future LLM work uses an interface with anthropic,
   openai, and deterministic mock providers. Workspace content is untrusted data,
   delimited away from instructions. JSON schema validation, quotas, token/step
   caps, and timeouts are required before a provider is enabled. The scaffold
   makes no model calls.

## Database identities and bootstrap

The PostgreSQL administrative `postgres` role only initializes the database and
extension. `slipgraph_owner` owns the `app` and `migrations` schemas and runs goose;
it is neither superuser nor BYPASSRLS. `slipgraph_app` is an independent runtime
LOGIN role with no privileged memberships, database/schema/table ownership,
CREATE, or BYPASSRLS. It has only explicit tenant-table privileges.

Every runtime connection checks both session and current identity, privileged
memberships (including built-in server-file roles), database/schema/table ownership, existence of the foundational
schema, and ENABLE/FORCE RLS. Unsafe configurations refuse startup. New migrations
must preserve these invariants. Tables outside `app` are not a place for tenant data.
The `migrations.goose_db_version` table is global migration bookkeeping, not tenant
state: it has no workspace or RLS and the runtime role cannot access its schema.
This infrastructure-only exception does not apply to domain or queue data.

Compose generates 256-bit random local credentials into separate named volumes.
API and worker mount only app and Redis credentials; they cannot read owner or
administrator credentials. Secret files are read through a rooted filesystem,
preventing symlink escapes from the configured secret directory. Migration mounts owner credentials. PostgreSQL mounts
the three DB credentials for initialization. Services do not publish DB or Redis
ports. Persistent DB and credential volumes must be backed up/restored together.
Use a secret manager and verified TLS in production. Local Compose is development
only; production config requires Postgres `sslmode=verify-full` and `rediss`.

## HTTP and web

API `GET /healthz` returns 200 `{ "status": "ok" }` only if Postgres and Redis
respond; failure is 503 without dependency details. The worker has the same probe
on its private port 8081. These routes are public, policy-controlled infrastructure
endpoints and never return tenant data. Unknown methods/routes expose no business
operations. Request IDs are server-generated UUIDv7. HTTP timeouts, panic recovery,
strict headers, and graceful SIGINT/SIGTERM shutdown are configured. Logs use JSON
slog and an allow-list; URLs, body content, arbitrary error strings, and tokens are
redacted. Future log fields must be reviewed before adding them to the allow-list.

The React application uses strict TypeScript, React Router, TanStack Query,
Tailwind, and the generated contract. Production assets are served by unprivileged
NGINX with same-origin `/api/` proxying, Docker DNS refresh for API replacements, and a strict CSP without inline scripts,
external fonts, or remote images. The Vite development proxy targets localhost:8080.
React escapes text; rich-text rendering is not implemented and must add DOMPurify
before enabling it. Cookie sessions, CSRF, websocket upgrades, and authenticated
requests are not implemented yet.

## Verification and evolution

`make test` runs Go/React tests and Docker-backed testcontainers tests. The latter
boot the real bootstrap script, run goose up/down/up as owner, reject privileged
roles and missing RLS, seed two workspaces, and check visibility, denied writes,
cross-tenant references, and transaction-setting cleanup. There are no search,
websocket, or job types yet; each new type must extend this suite in the same PR.
CI also checks generated drift, vulnerability scans, lint, and Compose startup.
See THREAT_MODEL.md for exact security requirements and implementation status.
