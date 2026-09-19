# Deploying DyDan with Nginx Proxy Manager on Docker

This guide covers deploying DyDan behind Nginx Proxy Manager (NPM) with proper HTTPS configuration.

## Prerequisites

- Docker and Docker Compose installed
- Nginx Proxy Manager running in a Docker container
- A domain or local hostname pointing to your server
- TLS certificates (NPM can use Let's Encrypt or custom certificates)

## Architecture

```
Browser (HTTPS)
  https://example.local
         ↓
  [Nginx Proxy Manager - TLS Termination]
    - Port 80/443 (public)
    - Validates certificates
         ↓
    Internal Docker Network: 01_apps_dydan_default
    http://dydan:2021 (or http://172.24.0.X:2021)
         ↓
  [DyDan Container]
    - Port 2021 (internal)
    - Embedded AppWeb frontend
    - No TLS, no separate frontend container
```

## Step 1: Create the Docker Network

If NPM is not already on a shared network with DyDan:

```bash
docker network create 01_apps_dydan_default

# Connect NPM to this network if it's not already
docker network connect 01_apps_dydan_default nginx-proxy-manager
```

## Step 2: Deploy DyDan

Create your docker-compose.yml:

```yaml
version: '3.8'

services:
  dydan:
    image: ghcr.io/alamoudimoh/dydan:latest
    pull_policy: always
    container_name: dydan
    restart: unless-stopped
    ports:
      - "2021:2021"
    environment:
      TZ: Asia/Riyadh
      # CRITICAL: Set this to NPM's IP on the Docker network
      DYDAN_TRUSTED_PROXIES: "<npm-ip-address>"
    volumes:
      - /opt/Applications/dydan/Data:/data
    networks:
      - 01_apps_dydan_default

networks:
  01_apps_dydan_default:
    external: true
```

**Finding NPM's IP address:**

```bash
# Method 1: Inspect NPM container
docker inspect nginx-proxy-manager \
  --format '{{range .NetworkSettings.Networks}}{{if eq .NetworkID "01_apps_dydan_default"}}{{.IPAddress}}{{end}}{{end}}'

# Method 2: Use the network gateway
docker network inspect 01_apps_dydan_default \
  --format '{{range .IPAM.Config}}{{.Gateway}}{{end}}'

# Method 3: List all containers on the network
docker network inspect 01_apps_dydan_default | grep -A 4 nginx-proxy
```

Replace `<npm-ip-address>` with the actual IP (e.g., `172.24.0.2`).

**Alternative: Use the entire Docker subnet:**

```yaml
DYDAN_TRUSTED_PROXIES: "172.24.0.0/16"
```

This is slightly less secure but works if container IPs are dynamic.

## Step 3: Configure Nginx Proxy Manager

### In NPM Web UI:

1. **Hosts → Proxy Hosts → Add Proxy Host**

2. **Details Tab:**
   - Domain Names: `example.local` (or your actual domain)
   - Scheme: `http`
   - Forward Hostname / IP: `dydan` (container name) or `172.24.0.X` (DyDan's IP)
   - Forward Port: `2021`
   - ☑ Cache Assets (optional, recommended)
   - ☐ Block Common Exploits (not needed for internal apps)
   - ☐ Websockets Support (not needed for DyDan)

3. **SSL Tab:**
   - SSL Certificate: Select your certificate (Let's Encrypt or custom)
   - ☑ Force SSL
   - ☐ HTTP/2 Support (optional)
   - ☐ HSTS Enabled (optional, but recommended for production)
   - Certificate Path: `/etc/nginx-proxy-manager/...`

4. **Advanced Tab (CRITICAL):**

Add the following to the Custom Nginx Configuration:

```nginx
# Forwarding headers - REQUIRED for proper HTTPS detection
proxy_set_header X-Forwarded-Proto $scheme;
proxy_set_header X-Forwarded-Host $host;
proxy_set_header X-Forwarded-For $remote_addr;
proxy_set_header Host $host;

# Security headers
proxy_hide_header X-Powered-By;
add_header X-Content-Type-Options nosniff;
add_header X-Frame-Options SAMEORIGIN;
add_header X-XSS-Protection "1; mode=block";

# Timeouts (optional but recommended)
proxy_connect_timeout 60s;
proxy_send_timeout 60s;
proxy_read_timeout 60s;
```

**Why these headers matter:**

| Header | Purpose |
|--------|---------|
| `X-Forwarded-Proto: $scheme` | Tells DyDan the original request was HTTPS |
| `X-Forwarded-Host: $host` | Tells DyDan the public hostname (e.g., `example.local`) |
| `X-Forwarded-For: $remote_addr` | Tells DyDan the real client IP for rate limiting |
| `Host: $host` | Standard Host header (public hostname) |

Without these headers, DyDan sees:
- Protocol: `http` (even though browser used HTTPS)
- Host: internal Docker hostname/IP
- Result: CSRF validation fails, Secure cookies not set

## Step 4: Verify HTTPS Authentication Works

1. Open your browser to `https://example.local`
2. Log in with your credentials
3. Open Developer Tools → Application → Cookies
4. Verify the `refresh_token` cookie:
   - ☑ HttpOnly
   - ☑ Secure
   - SameSite: Lax
   - Path: `/api/v1`

5. Refresh the page (F5)
6. Verify you remain logged in (no "session expired" message)

### Troubleshooting

**"Your session expired" immediately after login:**

- NPM's IP is not in `DYDAN_TRUSTED_PROXIES`
- Fix: Update environment variable and restart container

**Cookie not marked Secure:**

- Same cause as above
- DyDan doesn't know the request is HTTPS

**403 Forbidden on login/refresh:**

- NPM not sending forwarding headers
- Fix: Add custom Nginx config in NPM's Advanced tab

**Works on port 2021 directly but not through NPM:**

- NPM configuration issue
- Verify NPM's custom config matches the example above

## Security Checklist

- [ ] `DYDAN_TRUSTED_PROXIES` is set to a specific IP or small subnet
- [ ] NOT using `0.0.0.0/0` or other trust-all CIDRs
- [ ] NPM custom config includes `X-Forwarded-*` headers
- [ ] TLS certificate is valid and not expired
- [ ] Cookie is HttpOnly and Secure (check in browser DevTools)
- [ ] SameSite is Lax (not None)

## Example docker-compose.yml with NPM

Complete example with both NPM and DyDan:

```yaml
version: '3.8'

services:
  nginx-proxy-manager:
    image: jc21/nginx-proxy-manager:latest
    container_name: nginx-proxy-manager
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "81:81"  # Admin UI
    volumes:
      - npm_data:/data
      - npm_letsencrypt:/etc/letsencrypt
    networks:
      - 01_apps_dydan_default

  dydan:
    image: ghcr.io/alamoudimoh/dydan:latest
    pull_policy: always
    container_name: dydan
    restart: unless-stopped
    ports:
      - "2021:2021"
    environment:
      TZ: Asia/Riyadh
      DYDAN_TRUSTED_PROXIES: "nginx-proxy-manager"  # Or use IP
    volumes:
      - dydan_data:/data
    networks:
      - 01_apps_dydan_default
    depends_on:
      - nginx-proxy-manager

volumes:
  npm_data:
  npm_letsencrypt:
  dydan_data:

networks:
  01_apps_dydan_default:
    driver: bridge
```

Note: If using container names, ensure Docker DNS resolution works. Using IPs is more reliable.

## Advanced: Zero Trust / Tunnel Integration

For Cloudflare Tunnel, Tailscale, or similar:

1. Tunnel terminates TLS at the edge
2. Tunnel container sends traffic to DyDan on internal network
3. Trust the tunnel container's IP instead of NPM

```yaml
DYDAN_TRUSTED_PROXIES: "<tunnel-container-ip>"
```

The tunnel container must also send `X-Forwarded-*` headers. Most tunnel services do this automatically.

## Summary

The key to HTTPS behind reverse proxies:

1. **Configure `DYDAN_TRUSTED_PROXIES`** with the proxy container's IP
2. **Proxy must overwrite (not append) X-Forwarded-* headers**
3. **Verify cookies are Secure and HttpOnly** in browser DevTools
4. **Test refresh** - user should stay logged in

For more details, see [reverse-proxy.md](./reverse-proxy.md).
