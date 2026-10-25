# Slipgraph repository instructions

Read docs/ARCHITECTURE.md and docs/THREAT_MODEL.md before changing domain behavior.
They preserve the Master Context foundations and security rules. Keep both updated.

- Keep one Go module, separate API/worker binaries, and domain boundaries under internal/.
- Resolve workspace identity only from authenticated sessions. Never trust body, URL,
  query, or arbitrary headers for tenant identity. Jobs re-verify identity and policy.
- Use central policy.Can for every route, websocket message, job, and agent executor.
  Unknown actions fail closed. The only public action currently is health:read.
- Tenant tables require workspace_id, ENABLE/FORCE RLS, explicit runtime grants,
  and composite tenant-aware keys. Runtime is slipgraph_app, never an owner or superuser.
- Scope tenant context to a transaction with database.WithTenant. Never session-level SET.
- Extend generic objects and links. Domain mutations and outbox events commit together.
- Agents produce validated pending proposals only. Human approval and executor checks
  must enforce payload hash, expiry, permissions, and exactly-once application.
- All workspace content is untrusted data. Slow LLM, transcription, and embedding work
  belongs in authorized worker jobs behind the LLM provider interface.
- OpenAPI is the contract. Run make gen after contract/schema changes; do not edit
  generated files. TypeScript is strict; do not introduce any.
- Keep credentials out of Git and logs. Preserve the runtime/migration secret separation.
- Work in small steps with unit/integration tests. Run make build, make test, and make
  lint; fix failures. Docker-backed tenant tests must never silently skip in CI.
- Add two-workspace isolation coverage for every new endpoint, search operation,
  websocket subscription, and job type. Pending security checklist items gate features.
- Use a currently patched Go release (minimum 1.26.10); an early patch of a newer minor
  version may still contain known vulnerabilities. CI uses the latest 1.26 patch.
