# Security policy

Slipgraph is a developer preview. There is no supported stable release line or
claim that the example Compose configuration is ready for production. Security
fixes target the current development branch; use a patched Go release and current
dependencies/images. The [threat model](docs/THREAT_MODEL.md) lists implemented
controls, tests, disabled features and deployment responsibilities.

## Reporting a vulnerability

**Do not disclose exploits, credentials or tenant data in a public issue or pull request.**

Use [GitHub private vulnerability reporting](https://github.com/ratuhin1122/slipgraph/security/advisories/new)
if that option is available for this repository. Enabling it is a repository-owner
setting; this file does not imply that it has been enabled. If no private reporting
channel is available, open a public issue asking the maintainer for a private
contact method, with no vulnerability details, tenant identifiers or reproduction
payloads. Do not send sensitive details until a private channel is established.

A private report should include the affected revision, environment, impact,
minimal steps using two synthetic workspaces, expected versus actual behavior,
and a safe proof of concept. Remove passwords, tokens, real recordings and customer
content. Do not test against other people's instances or data without permission.

Maintainers aim to acknowledge actionable reports within five business days and
coordinate a fix and disclosure timeline. This is a best-effort target, not a
response-time guarantee. Avoid public disclosure while a remediation plan is being
agreed; coordinate an advisory and credit preference with the maintainer.

## Important boundaries

RLS depends on trusted session-to-transaction scope binding. A compromised runtime
or malicious database administrator remains a trust boundary. The audit chain
needs independently anchored heads to establish evidence against such an admin.
Citations establish authorized source membership, not correctness. Prompt delimiters
alone do not grant safety; pending-only proposals and executor checks enforce it.
Production deployments must supply TLS, managed secrets, rotation, patched media
parsers, network restrictions, retention and monitoring. See the complete checklist
before exposing a currently gated feature.
