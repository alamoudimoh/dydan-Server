# Reverse-proxy deployment guide

Dydan is designed to be safely exposed either directly to the internet or
behind a reverse proxy. This document explains the proxy-trust model
introduced with the Phase 3 authentication hardening and exactly what
operators of Nginx, Caddy, and Traefik must configure.

## The trust model

Forwarding headers (`X-Forwarded-Proto`, `X-Forwarded-Host`,
`X-Forwarded-For`, `Forwarded`) influence two security decisions:

1. whether the refresh-token cookie is flagged `Secure`;
2. which origins are accepted as "same deployment" by the CSRF origin check
   on cookie-authenticated endpoints (`/auth/refresh`, `/auth/logout`).

Because a directly exposed Dydan instance receives these headers from
untrusted clients, headers are honored **only** when the TCP peer is in the
configured allowlist:

```yaml
server:
  trusted_proxies: ["127.0.0.1/8", "::1/128"]   # default
```

Environment variable equivalent:

```bash
DYDAN_TRUSTED_PROXIES=10.0.0.5,172.18.0.0/16
```

* **Direct exposure** — keep the default. Clients can send whatever headers
  they like; they are ignored. Requests are treated as plain HTTP (or HTTPS
  if TLS terminates inside Dydan itself).
* **Proxy on the same host / same container network gateway** — the default
  loopback list usually already matches.
* **Proxy on another host or a Docker bridge network** — add the proxy's IP
  or CIDR (e.g. `172.18.0.0/16` for a compose network, or the nginx host IP).
* **No proxies are trusted implicitly.** An empty list disables all
  forwarded-header handling.

The same list also drives gin's client-IP resolution, so rate limiting sees
the real client address behind a trusted proxy instead of the proxy's own.

## Finding your proxy IP

Before configuring `DYDAN_TRUSTED_PROXIES`, you need to know the IP address or
CIDR range of your reverse proxy as seen by the Dydan container.

### Docker Compose / Docker Bridge Network

When using Docker Compose with a reverse proxy in another container:

```bash
# Find your Docker network's gateway
docker network inspect 01_apps_dydan_default | grep Gateway

# Or find the proxy container's IP on the shared network
docker inspect nginx-proxy-manager | jq '.[0].NetworkSettings.Networks["01_apps_dydan_default"].IPAddress'

# Use either the specific IP or the entire subnet
DYDAN_TRUSTED_PROXIES=172.24.0.2        # Specific proxy container
DYDAN_TRUSTED_PROXIES=172.24.0.0/16     # Entire Docker bridge network
```

The proxy must be on the same Docker network for this to work.

### Nginx Proxy Manager (Docker)

Nginx Proxy Manager typically runs on a Docker bridge network. Find its IP:

```bash
# List networks for the NPM container
docker inspect nginx-proxy-manager --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'

# Or use the network gateway
docker network inspect <network_name> --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}'
```

Common Docker bridge networks use:
- `172.17.0.0/16` (default Docker bridge)
- `172.18.0.0/16`, `172.19.0.0/16`, etc. (user-defined networks)
- `172.24.0.0/16` (example from this guide)

### Zero Trust / Cloud Tunnels (Cloudflare Tunnel, Tailscale, Zscaler)

For tunnel services, use the tunnel container's IP or the gateway:

```bash
# Cloudflare Tunnel (cloudflared)
docker inspect cloudflared-tunnel --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'

# Use the specific tunnel IP
DYDAN_TRUSTED_PROXIES=<tunnel-ip>
```

If the tunnel runs on the same host (not in a container), use `127.0.0.1`:

```bash
DYDAN_TRUSTED_PROXIES=127.0.0.1
```

### Testing your configuration

After setting `DYDAN_TRUSTED_PROXIES`, verify authentication works:

1. Access your application through the HTTPS reverse proxy
2. Log in and note the successful authentication
3. Refresh the page in your browser
4. Verify you remain authenticated (not logged out)

If refresh fails with "session expired":
- The proxy IP in `DYDAN_TRUSTED_PROXIES` is incorrect
- The proxy is not sending the correct `X-Forwarded-*` headers
- See the troubleshooting section below

## Operator requirements: sanitize forwarding headers

Your proxy must **overwrite** (not append to) the forwarding headers it sets,
so a malicious client can never smuggle their own values past your proxy into
Dydan.

### Nginx

```nginx
location / {
    # $proxy_add_x_forwarded_for APPENDS; use explicit overwrite instead:
    proxy_set_header X-Forwarded-For   $remote_addr;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host  $host;
    proxy_set_header Host              $host;
    proxy_pass http://127.0.0.1:7171;
}
```

Then set on the Dydan side:

```bash
DYDAN_TRUSTED_PROXIES=127.0.0.1
```

### Caddy

Caddy's `reverse_proxy` already sanitizes `X-Forwarded-*` by default:

```caddyfile
example.com {
    reverse_proxy 127.0.0.1:7171
}
```

If Dydan runs on another host, set `DYDAN_TRUSTED_PROXIES` to that Caddy
host's source IP as seen by Dydan.

### Traefik

Traefik overwrites `X-Forwarded-*` itself. Only make sure no entrypoint
forwards raw client headers through to Dydan unmodified (do not enable the
`forwardHeader` passthrough custom options), then set
`DYDAN_TRUSTED_PROXIES` to Traefik's container/host IP as seen by Dydan
(e.g. the compose network gateway).

## Access-token lifetimes

| Credential                              | Config key            | Default | Applies to |
|-----------------------------------------|-----------------------|---------|------------|
| Interactive access JWT                  | `jwt.access_token_time` | **15m** | Browser PWA + native app logins (`/auth/login`, refresh rotation, OIDC/MFA flows) |
| Legacy login JWT                        | `jwt.session_time`      | 24h     | `/api/v1/auth/login/legacy` only |
| Refresh session (rotating family)       | `jwt.max_refresh`       | 60d absolute | Both browser cookie and native body flow |

Every JWT carries a `cv` credential-version claim. Each authenticated request
reloads the user from the database and rejects a stale version. Interactive
JWTs also carry `sid` and `fam` and are accepted only while that user's family
is live, unrevoked, and unexpired. Logout, family revocation, revoke-others,
and refresh-replay revocation therefore reject access JWTs immediately after
the database commit, including on another replica and after restart. There is
no cache or process-local denylist.

Legacy login JWTs carry `cv` but intentionally have no refresh family, keeping
integration behavior compatible. Tokens issued by older releases without
`cv` are rejected after this deployment, so upgrading logs out existing JWT
clients. Browsers restore ordinary sessions through refresh rotation. Long-lived machine credentials are
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
Both operations increment `cv`, immediately invalidating prior access JWTs;
the preserved family can refresh to obtain a JWT with the new version. Child
password changes also increment `cv` and revoke every child session in the same
transaction.

Enabling or disabling MFA is stricter: it increments `cv` and revokes every
refresh family atomically, including the caller's family. The response clears
the refresh cookie and the user must sign in again. SSE tickets retain the JWT
security state present when minted and revalidate it at consumption.

## Distributed authentication rate limits

Security-sensitive authentication routes use fixed-window buckets in the
application database. Buckets are shared by all replicas using that database;
keys are SHA-256 hashes and never persist usernames, email addresses, session
tokens, refresh tokens, or raw client IPs. Usernames and emails are trimmed and
case-folded, token values are trimmed but remain case-sensitive, IPv4 is
canonicalized with `net.ParseIP`, and IPv6 sources are grouped by `/64`. Gin's
`ClientIP` remains the source, so configure trusted proxies as described above.

The defaults are 10 attempts per 60 seconds for login, signup, legacy login,
refresh, logout, OIDC start/callback, MFA login/confirm/disable, and recovery
redemption; recovery requests allow 5 per hour; password changes allow 10 per
60 seconds. Both IP and exposed account identifiers consume independent
buckets. Responses include `X-RateLimit-Limit`, `X-RateLimit-Remaining`, and
`X-RateLimit-Reset`; rejected requests return `429` with `Retry-After`.

`server.rate_limit_max_active_buckets` defaults to 100000. Expired rows have an
expiry index and are deleted before admitting a new key. Capacity exhaustion
or any database error fails closed with `503 AUTH_RATE_LIMIT_UNAVAILABLE`.
SQLite is suitable for one application node and uses WAL/busy timeout; replicas
must use one shared PostgreSQL database for cross-node atomic increments. The
general `server.rate_limit` API limiter is still process-local and is not a
distributed security control.

## Troubleshooting

### Symptom: "Your session expired" immediately after logging in through HTTPS

**Cause**: The reverse proxy IP is not in `DYDAN_TRUSTED_PROXIES`.

When you access Dydan through `https://example.local`:
1. Browser sends `Origin: https://example.local`
2. Nginx proxies to `http://dydan-container:7171`
3. Dydan sees request from Nginx's IP
4. If Nginx's IP is not trusted:
   - Dydan ignores `X-Forwarded-Proto: https`
   - Dydan ignores `X-Forwarded-Host: example.local`
   - Dydan uses `Host: dydan-container:7171`
   - Origin `https://example.local` doesn't match computed origin `http://dydan-container:7171`
   - CSRF rejects the request

**Solution**:

1. Find Nginx's IP on the Docker network (see "Finding your proxy IP" above)
2. Add it to your docker-compose:

```yaml
services:
  dydan:
    environment:
      DYDAN_TRUSTED_PROXIES: "172.24.0.2"  # Nginx's actual IP
```

3. Restart the Dydan container
4. Test login and refresh again

### Symptom: Refresh cookie not marked Secure under HTTPS

**Cause**: Same as above - Dydan doesn't know the request is HTTPS.

When a browser reload happens through an HTTPS proxy:
1. Browser sends refresh request with HttpOnly cookie
2. Dydan must decide: `Secure` or not?
3. If proxy is trusted: uses `X-Forwarded-Proto: https` → Secure=true
4. If proxy is untrusted: uses direct connection (http) → Secure=false

**Solution**: Add the proxy IP to `DYDAN_TRUSTED_PROXIES`.

### Symptom: Works locally but fails in production

Docker networking differs between local and production:

- **Local (docker-compose)**: Containers on the same bridge network, gateway IPs may differ
- **Production**: May use different network drivers, multiple networks, or host networking

**Debugging steps**:

1. Check which networks your containers use:
   ```bash
   docker inspect dydan --format '{{range .NetworkSettings.Networks}}{{.NetworkID}}{{end}}'
   docker inspect nginx-proxy --format '{{range .NetworkSettings.Networks}}{{.NetworkID}}{{end}}'
   ```

2. Ensure both containers share at least one network:
   ```bash
   docker network connect 01_apps_dydan_default nginx-proxy
   ```

3. Verify the proxy sends the correct headers:
   ```nginx
   # In your Nginx config (NPM → Advanced → Custom Configuration)
   proxy_set_header X-Forwarded-Proto $scheme;
   proxy_set_header X-Forwarded-Host $host;
   proxy_set_header X-Forwarded-For $remote_addr;
   proxy_set_header Host $host;
   ```

4. Check Dydan logs for rejected CSRF:
   ```bash
   docker logs dydan 2>&1 | grep -i "cross-origin"
   ```

### Security note: Do NOT use trust-all configurations

Never configure Dydan to trust all IPs or networks:

```bash
# ❌ DANGEROUS - Do not use
DYDAN_TRUSTED_PROXIES=0.0.0.0/0
DYDAN_TRUSTED_PROXIES=::/0
DYDAN_TRUSTED_PROXIES=172.0.0.0/8  # Too broad

# ✅ CORRECT - Specific proxy IP or network
DYDAN_TRUSTED_PROXIES=172.24.0.2
DYDAN_TRUSTED_PROXIES=172.24.0.0/28  # Specific small subnet
DYDAN_TRUSTED_PROXIES=127.0.0.1       # Localhost proxy
```

Allowing arbitrary IPs to set forwarding headers lets attackers:
- Bypass CSRF protection
- Set Secure cookies on HTTP connections
- Impersonate any client IP for rate-limiting purposes

Always trust only the specific proxy IP or the smallest possible subnet.
