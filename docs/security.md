# DayDan-Server security model

This is an implementation summary for operators and contributors, not a
substitute for threat modeling or deployment-specific security review.

## Authentication and sessions

DayDan supports local credentials and configured OAuth/OIDC providers,
including Google and Apple integrations. TOTP MFA uses recovery codes and
bounded verification attempts.

### Single-household OIDC deployments

For a self-hosted household that uses an identity provider as its source of
truth, enable the following canonical environment variables:

```bash
DAYDAN_SINGLE_CIRCLE_INSTANCE=true
DAYDAN_DISABLE_PASSWORD_AUTH=true
DAYDAN_OAUTH2_ADMIN_GROUPS=household-admins
DAYDAN_OAUTH2_MANAGER_GROUPS=household-managers
```

`DAYDAN_SINGLE_CIRCLE_INSTANCE` places new OIDC users into the shared
household circle and hides circle-management flows intended for multi-circle
instances. `DAYDAN_DISABLE_PASSWORD_AUTH` makes the instance SSO-only.

On each OIDC login, if either group list is configured, role synchronization
resolves an exact, case-sensitive group match. Administrator groups take
precedence over manager groups; users in neither configured group resolve to
the member role. A managed child account cannot be elevated by OIDC group
mapping. If neither group list is configured, existing roles are not changed.

The behavior is implemented by `internal/auth/role_resolver.go`,
`internal/user/handler.go`, and `config/config.go`, with precedence and
case-sensitivity coverage in `internal/auth/role_resolver_test.go` and
configuration coverage in `config/env_test.go`.

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
