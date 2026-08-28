# Security Policy

## Reporting a Vulnerability

Please **do not** open a public issue for security vulnerabilities.

Report privately to **nagizaazs@gmail.com** with a description, reproduction
steps, and impact. You will get an acknowledgement within 48 hours.

## Security posture

- **No secrets in the repo.** Configuration is environment-only; `DATABASE_URL`
  carries the database credentials and is never logged.
- **Structured audit trail.** Every request is logged as one JSON line with a
  request ID, method, route, status, and latency — never request bodies.
- **Least-privilege container.** The image is `distroless/static` and runs as a
  non-root user with no shell.
- **Input limits.** Request bodies are capped at 1 MiB and unknown JSON fields
  are rejected.
- **Money integrity.** Amounts are integer minor units (never floating point);
  the ledger is append-only and every transfer is balanced by database
  constraints (`amount > 0`, `balance >= 0` unless overdraft is allowed,
  distinct debit/credit accounts).

## Not in scope for this reference service

- Authentication / authorization — expected to be enforced by an upstream API
  gateway or service mesh. Add mTLS or a token check before exposing publicly.
- Rate limiting — delegate to the ingress.
