# Contributing to Slipgraph

Start with [ARCHITECTURE.md](docs/ARCHITECTURE.md) and
[THREAT_MODEL.md](docs/THREAT_MODEL.md). Discuss substantial product or schema changes
in an issue before implementation. Report security issues privately as described
in [SECURITY.md](SECURITY.md).

## Local setup

Use a patched Go release (minimum 1.26.10), Node 24+, npm, Python 3.11+, GNU Make,
and Docker with Compose v2. Run `npm --prefix web ci`, `make up`, then `make build`.
`make seed` opts the local stack into deterministic mock providers and creates
fictional data; `.showcase/` contains private generated credentials and is ignored.
Never commit it, `.env`, provider keys, session cookies, recordings from real people,
or unredacted logs.

## Change boundaries

- Keep one Go module and separate API/worker binaries, with domain code under `internal/`.
- Derive tenant identity only from verified human/service sessions. Use `WithTenant`
  and central `policy.Can`; never turn a request workspace field into authority.
- Add ENABLE/FORCE RLS, explicit minimal grants, composite references, and real
  two-workspace tests for every tenant table, endpoint and job.
- Preserve the six-table identity exception; only `internal/auth` queries that schema.
- Commit domain mutations and outbox events together. Models produce validated
  pending proposals; humans approve, and the executor rechecks every safety rule.
- Treat workspace text as untrusted. Render inert content; bound external work,
  uploads, retries and quotas. Never weaken a security check to make a demo work.
- OpenAPI is the contract. Update its coverage inventory and executed security cases;
  run `make gen` and commit generated output. Do not hand-edit generated files.
- Use strict TypeScript with no `any`, format Go with gofmt, and keep docs accurate.

## Before opening a pull request

```sh
make gen
make build
make test
make lint
```

Docker-backed tests must run, including the filter-removal RLS build. If your local
Docker host needs a larger timeout, extend the wall-clock test budget without
skipping cases. `make benchmark` is opt-in because it seeds an isolated 100k-task
fixture and records host-specific results. Never invent or copy benchmark numbers.

Explain the concrete behavior change, its security implications, relevant tests and
remaining limits. Include screenshots for visible changes with synthetic data only.
Keep pull requests focused and commit messages descriptive (`feat:`, `fix:`, `docs:`
are useful conventions). Contributions are provided under the repository's MIT
license; no separate CLA process is configured.
