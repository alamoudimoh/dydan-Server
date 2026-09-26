# HTTPS Session Handling Fix - Implementation Summary

## Confirmed Root Cause

The existing DyDan authentication implementation was **already correct**. The issue is purely a **deployment configuration problem**:

When DyDan is accessed through HTTPS behind a reverse proxy (Nginx Proxy Manager):
1. Browser sends `Origin: https://example.local` header
2. Nginx forwards to `http://dydan:7171` with `X-Forwarded-Proto: https`
3. If Nginx's IP is **not** in `DYDAN_TRUSTED_PROXIES`:
   - DyDan ignores the forwarded headers (security feature)
   - DyDan computes origin as `http://dydan:7171`
   - CSRF check rejects `Origin: https://example.local` as cross-origin
   - Result: "Your session expired"

## Changes Made

### 1. Comprehensive Test Coverage

**File**: `internal/auth/https_proxy_test.go` (NEW)

Added 5 test cases covering:
- ✅ Session restore behind trusted HTTPS proxy (browser refresh succeeds)
- ✅ Direct HTTP access (no proxy) still works
- ✅ Untrusted IPv6 proxy cannot spoof forwarding headers
- ✅ Nginx Proxy Manager compatibility
- ✅ Zero Trust tunnel compatibility

All tests use the existing, proven security model:
- Trusted proxy allowlist (`DYDAN_TRUSTED_PROXIES`)
- Secure cookie flag derived from request scheme
- CSRF origin validation using public origin
- HttpOnly cookies preserved
- SameSite=Lax maintained

### 2. Enhanced Documentation

**File**: `docs/reverse-proxy.md` (UPDATED)

Added sections:
- **Finding your proxy IP**: Docker network inspection commands
- **Troubleshooting**: Common symptoms and solutions
- **Security warnings**: Why not to trust 0.0.0.0/0

**File**: `docs/deployment/npm-docker.md` (NEW)

Complete deployment guide for Nginx Proxy Manager:
- Architecture diagram
- Step-by-step configuration
- Custom Nginx headers (critical)
- Security checklist
- Troubleshooting steps

## Exact Behavior

### With Trusted Proxy Configured

```yaml
environment:
  DYDAN_TRUSTED_PROXIES: "172.24.0.2"  # NPM's IP
```

Request flow:
```
Browser: https://example.local/api/v1/auth/refresh
  Origin: https://example.local
  Cookie: refresh_token=... (HttpOnly)

↓ [TLS terminates at NPM]

NPM → DyDan:
  X-Forwarded-Proto: https
  X-Forwarded-Host: example.local
  X-Forwarded-For: 203.0.113.77
  Host: example.local
  RemoteAddr: 172.24.0.2:54321
  Origin: https://example.local

DyDan Processing:
  ✓ Proxy trust: 172.24.0.2 in allowlist
  ✓ RequestScheme: https (from X-Forwarded-Proto)
  ✓ RequestHost: example.local (from X-Forwarded-Host)
  ✓ Computed origin: https://example.local
  ✓ CSRF: Origin matches computed origin
  ✓ HttpOnly cookie: Yes
  ✓ Secure cookie: Yes (scheme is https)
  ✓ SameSite: Lax

Response:
  Set-Cookie: refresh_token=...; Secure; HttpOnly; SameSite=Lax; Path=/api/v1
  access_token: ... (JSON body)

Browser:
  - Stores access token in memory
  - HttpOnly cookie remains (Secure)
  - User stays on current page
```

### Without Trusted Proxy

```yaml
environment:
  DYDAN_TRUSTED_PROXIES: ""  # or default loopback only
```

Request flow:
```
NPM → DyDan:
  RemoteAddr: 172.24.0.2:54321
  X-Forwarded-Proto: https (IGNORED - untrusted source)
  X-Forwarded-Host: example.local (IGNORED)
  Host: dydan:7171 (internal Docker hostname)
  Origin: https://example.local

DyDan Processing:
  ✗ Proxy trust: 172.24.0.2 NOT in allowlist
  ✗ RequestScheme: http (no TLS, no trusted X-Forwarded-Proto)
  ✗ RequestHost: dydan:7171 (from Host header)
  ✗ Computed origin: http://dydan:7171
  ✗ CSRF: Origin https://example.local ≠ http://dydan:7171
  ✗ Result: 403 Forbidden (Cross-origin request rejected)

Browser:
  "Your session expired"
```

## Nginx Proxy Manager Configuration

**Critical**: The following headers MUST be set in NPM's "Advanced" tab:

```nginx
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header X-Forwarded-Host $host;
proxy_set_header X-Forwarded-For $remote_addr;
proxy_set_header Host $host;
```

These headers tell DyDan:
1. The original protocol (HTTPS)
2. The public hostname
3. The real client IP (for rate limiting)
4. The public host (standard)

## Security Properties Maintained

✅ **HttpOnly refresh cookie**: JavaScript cannot read it  
✅ **Secure flag**: Set under HTTPS (prevents MITM on cookie itself)  
✅ **SameSite=Lax**: Blocks cross-site POST attacks  
✅ **CSRF origin validation**: Defense in depth  
✅ **Trusted proxy allowlist**: Only specific IPs can set forwarding headers  
✅ **No trust-all**: 0.0.0.0/0 never accepted  
✅ **Refresh rotation**: Single-use tokens prevent replay  
✅ **No localStorage**: Refresh tokens never in JavaScript-readable storage  

## Operator Action Required

### For Nginx Proxy Manager:

1. Find NPM's IP on the Docker network:
   ```bash
   docker inspect nginx-proxy-manager \
     --format '{{range .NetworkSettings.Networks}}{{if eq .NetworkID "<network-id>"}}{{.IPAddress}}{{end}}{{end}}'
   ```

2. Add to docker-compose.yml:
   ```yaml
   environment:
     DYDAN_TRUSTED_PROXIES: "<npm-ip>"
   ```

3. Configure NPM Advanced tab with required headers (see above)

4. Restart DyDan:
   ```bash
   docker-compose restart dydan
   ```

5. Test login and refresh through HTTPS proxy

### For Zero Trust Tunnels:

Same process, but use the tunnel container's IP instead of NPM's IP.

## Tests Added

| Test | Coverage |
|------|----------|
| TestSessionRestoreBehindHTTPSProxy | Full browser refresh flow through trusted NPM |
| TestSessionRestoreBehindIntranetHTTP | Direct HTTP access (no proxy) |
| TestSessionRestoreBlockedWhenProxyIPv6NotTrusted | Untrusted proxy cannot bypass CSRF |
| TestNginxProxyManagerCompatibility | NPM header format compatibility |
| TestZeroTrustTunnelCompatibility | Cloudflare/Tailscale tunnel scenarios |

## Tests Passed

```bash
cd DyDan-Server
go test ./internal/auth -count=1 -v
# All 100+ authentication tests pass, including new HTTPS proxy tests
```

## Files Changed

| File | Change |
|------|--------|
| `internal/auth/https_proxy_test.go` | NEW - Comprehensive HTTPS proxy test coverage |
| `docs/reverse-proxy.md` | UPDATED - Added troubleshooting and finding proxy IP sections |
| `docs/deployment/npm-docker.md` | NEW - Complete NPM deployment guide |

## Docker Compose Example

```yaml
version: '3.8'

services:
  dydan:
    image: ghcr.io/alamoudimoh/dydan:<VERSION>
    pull_policy: always
    container_name: dydan
    restart: unless-stopped
    ports:
      - "7171:7171"
    environment:
      TZ: Asia/Riyadh
      DYDAN_TRUSTED_PROXIES: "172.24.0.2"  # NPM's actual IP
    volumes:
      - /opt/Applications/dydan/Data:/data
    networks:
      - 01_apps_dydan_default

networks:
  01_apps_dydan_default:
    external: true
```

## Verification Checklist

After deployment:

- [ ] Find NPM container IP: `docker inspect nginx-proxy-manager ...`
- [ ] Set `DYDAN_TRUSTED_PROXIES` to NPM's IP
- [ ] Configure NPM Advanced tab with required headers
- [ ] Restart DyDan container
- [ ] Access application through HTTPS
- [ ] Log in successfully
- [ ] Open browser DevTools → Application → Cookies
- [ ] Verify `refresh_token` cookie:
  - [ ] HttpOnly: ✓
  - [ ] Secure: ✓
  - [ ] SameSite: Lax
  - [ ] Path: `/api/v1`
- [ ] Refresh browser (F5)
- [ ] Verify user stays logged in (no "session expired")
- [ ] Verify user stays on the same URL/page

## Multi-Architecture Support

The DyDan Docker image supports:
- linux/amd64
- linux/arm64
- linux/arm/v7

Build process unchanged - no modifications needed.

## Commit Strategy

Changes should be committed together:

```bash
git add internal/auth/https_proxy_test.go
git add docs/reverse-proxy.md
git add docs/deployment/npm-docker.md
git commit -m "docs: add HTTPS reverse-proxy session handling documentation

- Add comprehensive tests for HTTPS behind trusted proxies
- Document proxy IP discovery for Docker deployments
- Add Nginx Proxy Manager deployment guide
- Add troubleshooting section for session-expired issues"
```

## Summary

The authentication system was **already correctly implemented**. This change:

1. ✅ Adds comprehensive test coverage for HTTPS proxy scenarios
2. ✅ Provides clear operator documentation
3. ✅ Documents the exact deployment configuration required
4. ✅ Maintains all existing security properties
5. ✅ No weakening of CSRF, cookie, or proxy-trust security

The "fix" is **deployment configuration**, not code changes. Operators must:

1. Set `DYDAN_TRUSTED_PROXIES` to their reverse proxy's IP
2. Ensure the proxy sends correct `X-Forwarded-*` headers
3. Verify cookies are Secure and HttpOnly
4. Test refresh through HTTPS to confirm

After proper configuration, HTTPS behind reverse proxies works correctly:
- Browser refresh succeeds (no "session expired")
- Cookies are Secure under HTTPS
- CSRF validation uses the public origin
- Users remain on the same page after refresh
