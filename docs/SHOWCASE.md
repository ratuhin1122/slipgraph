# Showcase runbook

All identities, projects, text and dates in this walkthrough are fictional.
Use a loopback local stack. `make seed` explicitly selects mock providers through
`docker-compose.showcase.yml`; it never sends fixture content to a real model.

## Prepare

1. Install Docker/Compose v2, Python 3.11+ and GNU Make.
2. Run `make seed`. Allow a few minutes for normal API writes and rate pacing.
3. Read the generated `.showcase/credentials.json` privately and sign in as the
   first account. Do not publish that file, its passwords or session cookies.
4. Open the Meridian launch project: 60 tasks, six workstreams/milestones and
   63 finish-to-start dependencies. Try Kanban, table and timeline views.
5. Open Meetings to see the launch readiness review and searchable timestamped
   transcript. Its silent two-second recording and fixed text are labeled mock data.
6. The second credential is an independent Northstar user, not a member of Meridian.

Repeated seeding resumes existing objects without deleting data or resetting
production limits. It preserves manual edits. If a user edited the seed project,
its contents may no longer match the fixture assertions; use a separate local
checkout/stack for a pristine presentation rather than deleting unrelated data.
Keep private manifests and credential volumes with their database. If you deliberately
reset a disposable database, regenerate the local fixture directory for that new instance.

## Verified story

Run `make demo` for the non-interactive story. It asserts:

1. A messy brief creates one **pending** create-plan proposal. No project is written.
2. Explicit approval creates the fixed mock's three tasks and two dependencies.
3. A chat message reports a three-day delay with a verified task card.
4. The pure risk engine forecasts three affected tasks and three days of launch slip.
   Every original task version/date remains unchanged while proposals are pending.
5. Each reviewed proposal executes once, under current human permissions.
6. The real append-only hash chain verifies via API and authenticated audit CLI.
7. Ask retrieves workspace data and returns only server-validated retrieved citations.
8. Northstar cannot read Meridian projects/tasks/chat/proposals/private media, and
   cannot retrieve or cite Meridian objects through search/Ask or switch into its directory.

The large seeded project and the small model fixture are intentionally separate.
The mock does not interpret arbitrary briefs or demonstrate real semantic reasoning.
The audit stage verifies the real chain and count; it does not pretend that raw
runtime SELECT access to the INSERT-only audit table exists. Evidence is saved to
`docs/SHOWCASE_RESULT.json`, without credentials or live routing/session tokens.

## Record the delay GIF

Run `python scripts/showcase_demo.py --pause` after seeding. At each verified stage
it writes the current product URL to `.showcase/live.json` and waits for Enter.
Sign in as the first synthetic account in your browser. Capture actual **viewport**
PNGs at a consistent window size, with no password forms or credential file visible.
Useful frames: pending plan, approved three-task project, chat report, timeline
impact, proposal review and executed proposals. Save them in `.showcase/frames/`
as `frame001.png`, `frame002.png`, etc. Do not fabricate dates or statuses in images.

Run `make gif` to encode those frames into `docs/assets/delay-demo.gif`. The encoder
uses FFmpeg from the already-built API image in a network-disabled, read-only
container with only the frame and output directories mounted; no credentials are
mounted. The GIF holds each real UI state for about three seconds with a longer final hold, so it is a time-compressed
step recording, not a continuous real-time screencast. Review it before publishing.
The isolation API stage uses Northstar's separate cookie session; a browser still
logged into Meridian will naturally retain Meridian access. Sign in separately as
Northstar when showing that denial visually.

## Benchmark

`make benchmark` creates an isolated Postgres testcontainer and 100,000 synthetic
**task objects** with 384-dimensional mock vectors in 100 buckets. It rebuilds the
fixture index, warms ten queries and measures 100 serial retrievals. Real runtime
TenantTx/session/policy checks are included; provider, HTTP, indexing and worker
queue times are excluded. It does not write to the running application's database.

Results and host settings are in `docs/SEARCH_BENCHMARK.json`, and the README block
is regenerated from that measurement. Compare only equivalent distributions and
resource limits. The report is a measured local sample, not a latency SLA or recall
assessment. `SEARCH_BENCH_TIMEOUT` can extend the isolated run's wall-clock budget
on slow Docker Desktop hosts without changing measured query boundaries.

## Return to ordinary development

Run `docker compose up -d --wait` with your intended `.env` provider settings.
`make down` stops services and preserves named data/credential volumes. Never remove
credentials independently of database data. Showcase commands refuse remote origins
and do not expose an unauthenticated seed endpoint or a runtime RLS bypass.
