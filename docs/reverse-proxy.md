# Reverse-proxy deployment guide

DayDan is designed to be safely exposed either directly to the internet or
behind a reverse proxy. This document explains the proxy-trust model
introduced with the Phase 3 authentication hardening and exactly what
operators of Nginx, Caddy, and Traefik must configure.

## The trust model

Forwarding headers (`X-Forwarded-Proto`, `X-Forwarded-Host`,
`X-Forwarded-For`, `Forwarded`) influence two security decisions:

1. whether the refresh-token cookie is flagged `Secure`;
2. which origins are accepted as "same deployment" by the CSRF origin check
   on cookie-authenticated endpoints (`/auth/refresh`, `/auth/logout`).

Because a directly exposed DayDan instance receives these headers from
untrusted clients, headers are honored **only** when the TCP peer is in the
configured allowlist:

```yaml
server:
  trusted_proxies: ["127.0.0.1/8", "::1/128"]   # default
```

Environment variable equivalent:

```bash
DAYDAN_TRUSTED_PROXIES=10.0.0.5,172.18.0.0/16
```

* **Direct exposure** — keep the default. Clients can send whatever headers
  they like; they are ignored. Requests are treated as plain HTTP (or HTTPS
  if TLS terminates inside DayDan itself).
* **Proxy on the same host / same container network gateway** — the default
  loopback list usually already matches.
* **Proxy on another host or a Docker bridge network** — add the proxy's IP
  or CIDR (e.g. `172.18.0.0/16` for a compose network, or the nginx host IP).
* **No proxies are trusted implicitly.** An empty list disables all
  forwarded-header handling.

The same list also drives gin's client-IP resolution, so rate limiting sees
the real client address behind a trusted proxy instead of the proxy's own.

## Operator requirements: sanitize forwarding headers

Your proxy must **overwrite** (not append to) the forwarding headers it sets,
so a malicious client can never smuggle their own values past your proxy into
DayDan.

### Nginx

```nginx
location / {
    # $proxy_add_x_forwarded_for APPENDS; use explicit overwrite instead:
    proxy_set_header X-Forwarded-For   $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host  $host;
    proxy_set_header Host              $host;
    proxy_pass http://127.0.0.1:2021;
}
```

Then set on the DayDan side:

```bash
DAYDAN_TRUSTED_PROXIES=127.0.0.1
```

### Caddy

Caddy's `reverse_proxy` already sanitizes `X-Forwarded-*` by default:

```caddyfile
example.com {
    reverse_proxy 127.0.0.1:2021
}
```

If DayDan runs on another host, set `DAYDAN_TRUSTED_PROXIES` to that Caddy
host's source IP as seen by DayDan.

### Traefik

Traefik overwrites `X-Forwarded-*` itself. Only make sure no entrypoint
forwards raw client headers through to DayDan unmodified (do not enable the
`forwardHeader` passthrough custom options), then set
`DAYDAN_TRUSTED_PROXIES` to Traefik's container/host IP as seen by DayDan
(e.g. the compose network gateway).

## Access-token lifetimes

| Credential                              | Config key            | Default | Applies to |
|-----------------------------------------|-----------------------|---------|------------|
| Interactive access JWT                  | `jwt.access_token_time` | **15m** | Browser PWA + native app logins (`/auth/login`, refresh rotation, OIDC/MFA flows) |
| Legacy login JWT                        | `jwt.session_time`      | 24h     | `/api/v1/auth/login/legacy` only |
| Refresh session (rotating family)       | `jwt.max_refresh`       | 60d absolute | Both browser cookie and native body flow |

Interactive access tokens are short-lived on purpose: logout and session
revocation take full effect within minutes even if a bearer token was copied
beforehand. Browsers restore sessions transparently via the HttpOnly refresh
cookie, so no UX change is expected. Long-lived machine credentials are
completely separate: `/api/v1/users/tokens` issues opaque API keys checked by
their own middleware and are unaffected by all JWT lifetime settings.

## Session management API

Interactive access tokens embed `sid` (refresh-session row) and `fam`
(rotation family) claims, which lets the API identify the caller's current
device without exposing any credential material. All endpoints require a
Bearer token; every query is scoped server-side to the authenticated user.
Responses contain metadata only — never token values, hashes, or secrets:

| Endpoint | Method | Behavior |
|----------|--------|----------|
| `/api/v1/auth/sessions` | GET | Lists the user's live sessions (one per login/device) with `id`, `createdAt`, `lastActiveAt`, `expiresAt`, `current` |
| `/api/v1/auth/sessions/:id` | DELETE | Revokes one session family; foreign IDs return 404 without leaking existence |
| `/api/v1/auth/sessions/revoke-others` | POST | Revokes every family except the caller's current one |

Revoking the current session also clears the refresh cookie (self-logout).

Refresh rotation is single-use. A duplicate request inside the bounded grace
window receives `409 REFRESH_RETRY` and no credentials; browser clients retry
against the successor in the shared HttpOnly cookie. Reuse after the grace
window revokes every descendant in the family.

An authenticated password change preserves the caller's current refresh
session and revokes all other interactive sessions. Password recovery revokes
all interactive refresh sessions transactionally with the password update.
Already-issued access JWTs remain usable only for their configured short
`jwt.access_token_time`; DayDan does not maintain an access-token blacklist.
