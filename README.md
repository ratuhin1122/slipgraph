# Slipgraph

Multi-tenant AI team workspace foundation. Go modular monolith, Postgres 16 with
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
Go directly, supply DATABASE_URL and REDIS_URL through the environment (see
.env.example), using the runtime role. The migration command uses owner credentials
and expects the bootstrap schemas/roles to exist. Do not use owner credentials for
the API or worker: startup deliberately rejects them.

CI runs Go build/vet/race tests, testcontainers tenant isolation, golangci-lint,
gosec, govulncheck, frontend checks/tests/build/audit, generated-code drift, Docker
builds and full Compose readiness smoke tests. Integration tests fail if Docker is
unavailable; they do not silently skip security coverage.

The worker currently exposes readiness and does not process jobs. All domain
packages are reserved boundaries. See [architecture](docs/ARCHITECTURE.md) and the
[security checklist](docs/THREAT_MODEL.md) for the nine foundations and feature gates.
