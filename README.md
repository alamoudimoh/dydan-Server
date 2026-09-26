# Dydan Server

Dydan Server is the Go backend for Dydan. It provides the REST API,
authentication and authorization, persistence, migrations, realtime events,
notifications, integrations, media storage, and the production container that
serves the AppWeb client.

Dydan AppWeb is the paired client:
[alamoudimoh/DyDan-AppWeb](https://github.com/alamoudimoh/DyDan-AppWeb).
The server can serve an embedded frontend bundle, or build one from that
repository during a Docker build.

## What Dydan provides

Dydan coordinates household and shared work through circles, members,
assignments, recurring tasks, subtasks, labels, projects, routines, shopping
lists, maintenance, meters, Things, attachments, timers, history, points,
rewards, progress, dashboards, calendar views, search, notifications, and
account settings.

The server also provides:

- local accounts, OAuth/OIDC, Google and Apple sign-in, TOTP MFA, recovery
  codes, API tokens, managed child accounts, circle roles, and optional
  single-circle deployments;
- short-lived access JWTs, rotating refresh sessions, session revocation,
  realtime SSE, database-backed authentication limits, trusted-proxy handling,
  signed webhooks, signed media URLs, and outbound destination controls;
- local filesystem or S3-compatible media storage, FCM/Telegram/Discord/
  Pushover notifications, SMTP/SMTP2Go email, payment provider webhooks, and
  REST/API and webhook integrations suitable for external automation
  platforms such as Home Assistant;
- `dydanctl` backup, verification, restore, and retention operations for
  SQLite deployments.

## Quick start

Requirements: Docker with Compose v2.17 or newer.

The default `docker-compose.yaml` runs the canonical **published** Dydan image
from GHCR with zero local build:

```bash
cd dydan-server
docker compose pull
docker compose up -d
```

`docker compose up -d` resolves to `ghcr.io/alamoudimoh/dydan:latest` by
default. Operators can pin a specific published image when necessary:

```bash
DYDAN_IMAGE=ghcr.io/alamoudimoh/dydan:202609-05 docker compose up -d
```

### Local source build

Developers building the stack from source (server plus a paired AppWeb
checkout) use the `docker-compose.build.yaml` override instead of the default
compose file:

```bash
git clone https://github.com/alamoudimoh/DyDan-AppWeb.git dydan-app
git clone https://github.com/alamoudimoh/DyDan-Server.git dydan-server
cd dydan-server
DYDAN_FRONTEND_CONTEXT=../dydan-app \
  docker compose -f docker-compose.yaml -f docker-compose.build.yaml up -d --build
```

### Published images

The canonical Dydan image locator is:

```text
ghcr.io/alamoudimoh/dydan
```

Releases are published to GitHub Container Registry (GHCR). Each release is
referenced by its release tag (for example `202609-02`) and by `latest`; both
point to the same image digest. The normal deployment path uses `latest`:

```bash
docker pull ghcr.io/alamoudimoh/dydan:latest
docker run -d -v /data:/data -p 7171:7171 ghcr.io/alamoudimoh/dydan:latest
```

The legacy locator `ghcr.io/alamoudimoh/daydan` was retired during the move
to a canonical Dydan package identity. It is kept only as a legacy
compatibility artifact and must not be used in new configurations or
deployments.

The default deployment uses SQLite, stores persistent state in the
`dydan-data` volume, serves the API and embedded client on port `7171`, and
requires no configuration file, database selection, or manually generated JWT
secret. The frontend build uses same-origin `/api` requests.

Useful operations:

```bash
docker compose pull
docker compose logs -f
docker compose restart
docker compose down
docker compose up -d
```

For a source build instead of the published image, add the
`docker-compose.build.yaml` override (see "Local source build").

Keep the data volume when upgrading. For reverse proxies, trusted forwarding
headers, access-token policy, and TLS deployment, see
[docs/reverse-proxy.md](docs/reverse-proxy.md). For backups and restores, see
[docs/backup-restore.md](docs/backup-restore.md).

## Development

Build or run the backend directly:

```bash
go mod download
go run .
go test ./...
```

To build the full application image locally with the paired client, use the
production Dockerfile and its named frontend context:

```bash
docker build --build-context frontend=../dydan-app -t dydan .
```

Or, to mirror the default deployment (service name, volumes, port mapping)
with a locally built image, use the `docker-compose.build.yaml` override:

```bash
DYDAN_FRONTEND_CONTEXT=../dydan-app \
  docker compose -f docker-compose.yaml -f docker-compose.build.yaml up -d --build
```

The source-build override is documented for developers only; it must not be
confused with the production push-to-`latest` deployment path documented
above.

`Dockerfile.dev` is an artifact-oriented development image. It expects a
prebuilt `dydan` backend binary and the development config files in the server
build context; it does not build the frontend and is not the normal
full-application workflow. If you specifically need that image, build the
artifact first and then use the Dockerfile:

```bash
go build -o dydan .
docker build -f Dockerfile.dev -t dydan-dev .
```

The production image is built by
`.github/workflows/go-release.yml`; it
compiles the client and server together and serves the resulting SPA and API
from one origin.

Detailed implementation evidence and cross-product documentation are in
[docs/product-implementation-review.md](docs/product-implementation-review.md)
and [docs/architecture.md](docs/architecture.md). The generated API contract
is maintained in `docs/swagger.yaml` and `docs/swagger.json`.

Authentication, OIDC group mapping, MFA, sessions, and authorization are
covered in [docs/security.md](docs/security.md).

## Configuration

Dydan is zero-config by default. Use `config/dydan.example.yaml` for
advanced settings such as PostgreSQL, trusted proxies, storage, notifications,
OIDC, rate limits, and webhook signing. Canonical environment variables use
the `DYDAN_` prefix.

The default data directory is `/data`. It contains `dydan.db`, `jwt-secret`,
and local uploads. PostgreSQL deployments require operator-managed database
backups; `dydanctl` is designed for SQLite snapshots.

## License

Dydan Server is licensed under the AGPLv3. See [LICENSE.md](LICENSE.md).
