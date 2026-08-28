# Security Policy

## Supported versions

| Version | Supported |
|---------|-----------|
| `0.2.x` | ✅ |
| `< 0.2` | ❌ |

## Reporting a vulnerability

**Do not open a public issue.**

Report privately to **nagizaazs@gmail.com** (or via GitHub's *Report a
vulnerability* on the Security tab) with:

- a description and the impact,
- steps or a script to reproduce,
- affected version (`GET /version` or the image tag).

You will get an acknowledgement within **48 hours** and a fix or mitigation
plan within **7 days** for confirmed issues. Coordinated disclosure is
appreciated; credit will be given unless you prefer otherwise.

## Security posture

- **No secrets in the repo.** All configuration is environment-only.
  `DATABASE_URL` and `LEDGER_API_TOKENS` are never logged.
- **Authentication.** Optional static bearer-token auth on `/v1/*`
  (`LEDGER_API_TOKENS`, comma-separated), compared in constant time. When
  unset, the `/v1` surface is open and is expected to sit behind an API
  gateway, service mesh, or mTLS. Operational endpoints (`/healthz`, `/readyz`,
  `/version`, `/metrics`) are always unauthenticated.
- **Structured audit trail.** One JSON log line per request (request id,
  method, route, status, latency, client) — never request or response bodies.
- **Least-privilege container.** `distroless/static`, non-root user, no shell,
  static binary.
- **Input hardening.** Request bodies capped at 1 MiB; unknown JSON fields
  rejected; per-request timeout; path and query params strictly parsed.
- **Money integrity.** Integer minor units only. The ledger is append-only and
  every transfer is balanced by database `CHECK` constraints (`amount > 0`,
  `balance >= 0` unless overdraft, distinct debit/credit accounts, at most one
  reversal per transfer).
- **Supply chain.** Pinned dependencies, Dependabot (gomod + actions + docker),
  and `govulncheck` on every CI run.

## Out of scope

- Rate limiting — delegate to the ingress.
- Multi-tenant authorization (which token may touch which account) — belongs in
  a policy layer in front of this service.
