# Slipgraph

Multi-tenant team workspace with identity, projects, tasks, chat, realtime presence, and dependency risk. Go modular monolith, Postgres 16 with
pgvector, Redis, React/TypeScript. Agents will propose changes for human approval.

## Start

Install Docker with Compose v2, then run:

```sh
docker compose up --build
```

No `.env` or manual passwords are needed for local development. Optional settings
are in `.env.example`. First startup generates random credentials and runs goose
migrations before API/worker start. Ports bind to loopback only.

- Web: http://localhost:5173
- API readiness: http://localhost:8080/healthz
- Same-origin readiness: http://localhost:5173/api/healthz

`make up` starts in the background and waits for health. `make down` preserves
named data and credential volumes. Keep those volumes together; deleting only
credential volumes leaves the database passwords out of sync. This Compose setup
is for local development; terminate TLS and use managed secrets in production.

## Development

Requires Go 1.26.10+ (use the latest security patch on newer Go releases), Node 24+, npm, GNU Make and Docker for integration tests.
Dependencies are locked in go.mod/go.sum and web/package-lock.json.

```sh
npm --prefix web ci
make gen       # sqlc, oapi-codegen, frontend schema
make build
make test      # unit + frontend + testcontainers isolation; Docker required
make lint      # vet, golangci-lint, gosec, govulncheck, TS, ESLint, npm audit
make migrate   # one-shot owner-role goose migrations
```

`npm --prefix web run dev` serves the Vite development UI and proxies `/api` to the
running API. The Compose web service serves production assets with NGINX. To run
Go directly, supply DATABASE_URL, REDIS_URL, AUTH_KEY_FILE, and PUBLIC_ORIGIN (see
.env.example), using the runtime role. The migration command uses owner credentials
and expects the bootstrap schemas/roles to exist. Do not use owner credentials for
the API or worker: startup deliberately rejects them.

CI runs Go build/vet/race tests, testcontainers tenant isolation, golangci-lint,
gosec, govulncheck, frontend checks/tests/build/audit, generated-code drift, Docker
builds and full Compose readiness smoke tests. Integration tests fail if Docker is
unavailable; they do not silently skip security coverage.

After startup, create an account at `/signup`. Signup creates your first workspace
with an admin membership. Create projects and tasks, add blockers in the task drawer,
and use Kanban, table, or timeline views. Delay reports use total calendar days;
the worker stores versioned risk, earliest dates, slack, critical path, and launch
slip. Assessments show pending/stale status until the worker catches up. Task dates
are never automatically rewritten. All planning times are UTC; an estimate is a
24-hour-calendar duration, not a working-day capacity model.

Admins invite members/viewers from Members and privately share the generated link.
Sign in with the invited email before accepting. Workspace selection uses verified
membership tickets. Members can edit projects/tasks; viewers read; nobody can edit
their own role. Secure cookies work on trusted localhost development origins;
production requires HTTPS. Set PUBLIC_ORIGIN when changing the browser origin.
Password-reset primitives exist internally, but no email/recovery UI is enabled.

Open Chat to create workspace-wide channels, post messages, start threads, mention
teammates, and insert task cards. Writers may edit/delete only their own messages.
Viewers can read. Online/typing indicators expire automatically. Reconnecting clients
reload history; failed optimistic sends can be retried without duplicate messages.

The worker processes transactional events for audit, risk, and chat fanout with idempotent
receipts. Administrators can verify the current workspace audit chain through
`GET /api/audit/verify` or run `/app/slipgraph audit verify` in the API container
with `SLIPGRAPH_SESSION` containing their own access cookie (supply securely through
the environment; never paste it into Git, logs, or command history).

`make test` includes the 10k graph benchmark with a 200 ms budget. Database and
identity/project/chat integration suites run as separate Docker-backed invocations.
Comments and unrelated agent features remain disabled. See [architecture](docs/ARCHITECTURE.md) and the
[security checklist](docs/THREAT_MODEL.md) for the nine foundations and feature gates.

## Agent safety foundation

Open `/proposals` to review pending proposals, compare before/after fields and inspect
source links. Admins/members may approve or reject; viewers may only read. Approval
pins the displayed payload hash and version. The worker rechecks permissions,
expiry, hash and tenant references before atomically applying the change and audit.

Providers default to `LLM_PROVIDER=disabled`. To run the signature demo without API
keys, set `LLM_PROVIDER=mock` in your local `.env`, then run `docker compose up --build -d --wait`. This mode is a deterministic fixture, clearly labeled in the plan UI.

1. Open `/plans`, paste a brief containing a start date such as `2030-01-01`, and
   choose **Propose a plan**. Review its project, tasks and dependencies in one
   create-plan proposal. Approve it to create the demo project.
2. In Chat, post **backend API will slip 3 days**. The worker posts an impact card
   linking to the timeline and three pending reschedule proposals. The launch warning
   and explanations are deterministic; task dates are still unchanged.
3. Review the before/after dates and approve or reject proposals. Approved changes
   execute with your current permissions and an atomic audit trail.
4. Post **maybe backend API will slip 3 days** to exercise confirmation before proposals.
   Ambiguous names also require confirmation; task cards disambiguate targets.

The mock returns a fixed backend/frontend/launch plan rather than interpreting an
arbitrary brief. Select `openai` or `anthropic`, a structured-output-capable model,
worker-only credentials and conservative per-token price ceilings for real generation.
No live calls are required for tests. Run `python scripts/signature_smoke.py` against
a mock-enabled stack for the production-worker demo test. Original briefs are immutable
and source-linked; plans are bounded to 20 tasks/60 dependencies and delay scenarios
to 100 proposed date changes. See the architecture for quota and safety semantics.

## Meetings

Open `/meetings` to upload a private recording, search its timestamped transcript,
seek to evidence and review extracted decisions/actions. Enable both
`TRANSCRIPTION_PROVIDER=mock` and `LLM_PROVIDER=mock` for the offline demo.
The transcription fixture is explicitly labeled; actual media duration is still checked.
[Meeting configuration and limits](docs/MEETINGS.md) describes quotas, storage and real providers.

## Search and Ask

Open the command palette with **Ctrl+K** to search tasks, messages and meeting
transcripts or ask a question with source citations. For the offline demo enable
`EMBEDDING_PROVIDER=mock` and `LLM_PROVIDER=mock`. Providers default to disabled.
[Search configuration, safety and limits](docs/SEARCH.md).

<!-- search-benchmark:start -->
### Search benchmark

100,000 synthetic task objects, 384-dimensional mock vectors in 100 buckets,
one workspace, 10 warmups and 100 serial warm queries against isolated Docker Postgres.
Measured p50 **719.08 ms**, p95 **1082.99 ms**.
Includes hybrid SQL, tenant transaction and live session/policy checks. Excludes
indexing, worker queue/poll delay, remote embeddings and answer generation.
This synthetic warm-cache result is not a production latency guarantee.
Run `python scripts/search_benchmark.py` to reproduce. Details: [measurement](docs/SEARCH_BENCHMARK.json).
<!-- search-benchmark:end -->


## Security verification

`make test` runs the OpenAPI-driven cross-tenant suite both normally and with
compile-time SQL tenant-filter removal. Docker is required; isolation tests do not
skip. New routes, consumers, executable kinds and tenant tables need registered,
executed security cases. See [the threat model](docs/THREAT_MODEL.md) for the
coverage matrix, workspace-source audit and remaining gated integrations.

The production NGINX frontend uses a per-document nonce bootstrap; keep the API
available for HTML delivery. CI also checks the deployed nonce/header/download
boundary and runs authenticated `slipgraph audit verify` after smoke tests. Session
credentials are passed to that command on stdin and never printed.

Run `python scripts/run_smoke.py` against a mock-enabled stack for all production
smoke flows and the final audit command. The runner reuses four synthetic accounts
with fresh workspaces so it respects signup limits; credentials stay in memory.


## Optional GitHub integration

Create a GitHub OAuth app with callback
`PUBLIC_ORIGIN/api/integrations/github/callback`. Configure `GITHUB_CLIENT_ID`,
`GITHUB_CLIENT_SECRET`, and an independent random 32-byte `GITHUB_ENCRYPTION_KEY`
encoded as 64 hex characters. Secret-file alternatives are available when launching
the binaries; Compose's example uses environment interpolation, so production should
supply mounted managed secrets rather than plaintext shell history or committed files.
Keep this key separate from AUTH_KEY; back it up securely. For rotation, disconnect
connections first, deploy the new key, then reconnect/configure each workspace.
Old encrypted tokens/routes become unreadable with a changed key.

Open **Integrations** as a workspace admin, connect, select one public repository and
signal channel, then copy the one-time URL/secret into that repository's GitHub
webhook settings (`application/json`, pull request, issue and check run events).
No OAuth scopes are requested. If an old broader OAuth grant exists, revoke it in
GitHub and reconnect. Private repositories and repository replacement are not
supported in this first adapter. Configuring again rotates webhook credentials;
update GitHub accordingly. Disconnect revokes local routing and destroys local
credentials; revoke the remote OAuth application grant in GitHub separately.

Map issue numbers to tasks in settings, or put a task UUID in a PR branch name.
PR bodies such as `closes #12` follow those issue links. Failing checks, blocked PRs
and stale known PRs post a scenario into chat and the existing human-reviewed risk
workflow. Enable a worker model provider to generate proposals; no task changes
without approval. The configured delay estimate is not a measured slip. Stale checks
use the last received PR update, so ensure GitHub deliveries are healthy. The event log
shows receipt/processing metadata without raw payloads or credentials.

GitHub signs the body, not delivery headers/timestamps. Slipgraph deduplicates both
delivery ID and body hash and bounds signed event timestamps to seven days. Historical
redeliveries outside that window are rejected. Tests use fake OAuth responses and
signed fixtures; no live GitHub credentials are needed or used in CI.
