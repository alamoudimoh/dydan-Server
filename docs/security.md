# DayDan-Server security model

This is an implementation summary for operators and contributors, not a
substitute for threat modeling or deployment-specific security review.

## Authentication and sessions

DayDan supports local credentials and configured OAuth/OIDC providers,
including Google and Apple integrations. TOTP MFA uses recovery codes and
bounded verification attempts.

Interactive access JWTs are short-lived. Refresh credentials are represented
by database-backed sessions and rotating token families. Reuse detection,
credential-version checks, logout, password changes, MFA changes, and explicit
session revocation invalidate sessions according to the current user state.
Long-lived integrations use hashed API tokens rather than browser JWTs.

The session-management API exposes metadata about the user's own sessions,
including current-session state, without returning credential material.

## Authorization and isolation

Circle membership and roles are enforced server-side. Managed child accounts,
private chores, maintenance data, and other household resources are scoped to
the authenticated actor and permitted circle. Realtime broadcasts apply the
same privacy boundaries.

## Request and network controls

Authentication, recovery, API-token creation, realtime ticket issuance, and
other sensitive operations are rate-limited. Security-sensitive limits use
database-backed buckets so replicas sharing PostgreSQL observe the same
limits.

Forwarded headers are trusted only from configured proxy addresses. Configure
`DAYDAN_TRUSTED_PROXIES` when a reverse proxy terminates TLS or forwards the
client address. The application also sets security headers and rejects unsafe
CORS configurations.

## Media and outbound requests

Media URLs are signed and use a secret separate from the JWT secret. Storage
access applies size, quota, and destination rules. Outbound webhooks and
provider requests validate destinations to reduce SSRF risk, use bounded
request behavior, and avoid exposing secrets in logs or diagnostic payloads.

Webhook deliveries are signed with HMAC and documented in
[webhook-signing.md](webhook-signing.md). Receivers must verify the timestamp,
raw body, signature, and replay tolerance before processing a delivery.

## Operational boundaries

Keep `/data` on a protected persistent volume and restrict access to backup
archives because they contain application state and the persisted JWT secret.
Use HTTPS at the edge, configure trusted proxies narrowly, and use the
operator's native backup tooling for PostgreSQL. The included `daydanctl`
backup flow is SQLite-focused.
