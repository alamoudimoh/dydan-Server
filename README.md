# DayDan-Server

DayDan-Server is the Go backend for DayDan. It provides the REST API,
authentication and authorization, persistence, migrations, realtime events,
notifications, integrations, media storage, and the production container that
serves the AppWeb client.

DayDan-AppWeb is the paired client:
[alamoudimoh/DayDan-AppWeb](https://github.com/alamoudimoh/DayDan-AppWeb).
The server can serve an embedded frontend bundle, or build one from that
repository during a Docker build.

## What DayDan provides

DayDan coordinates household and shared work through circles, members,
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
- `daydanctl` backup, verification, restore, and retention operations for
  SQLite deployments.

## Quick start

Requirements: Docker with Compose v2.17 or newer.

Clone the paired repositories side by side:

```bash
git clone https://github.com/alamoudimoh/DayDan-AppWeb.git daydan-app
git clone https://github.com/alamoudimoh/DayDan-Server.git daydan-server
cd daydan-server
docker compose up -d --build
```

The default deployment uses SQLite, stores persistent state in the
`daydan-data` volume, serves the API and embedded client on port `2021`, and
requires no configuration file, database selection, or manually generated JWT
secret. The frontend build uses same-origin `/api` requests.

Useful operations:

```bash
docker compose logs -f
docker compose restart
docker compose down
docker compose up -d --build
```

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
docker build --build-context frontend=../daydan-app -t daydan .
```

`Dockerfile.dev` is a legacy artifact-oriented development image. It expects a
prebuilt `donetick` backend binary and the development config files in the
server build context; it does not build the frontend and is not the normal
full-application workflow. If you specifically need that image, build the
artifact first and then use the Dockerfile:

```bash
go build -o donetick .
docker build -f Dockerfile.dev -t daydan-dev .
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

DayDan is zero-config by default. Use `config/daydan.example.yaml` for
advanced settings such as PostgreSQL, trusted proxies, storage, notifications,
OIDC, rate limits, and webhook signing. Canonical environment variables use
the `DAYDAN_` prefix; compatibility aliases remain supported where existing
deployments depend on them.

The default data directory is `/data`. It contains `daydan.db`, `jwt-secret`,
and local uploads. PostgreSQL deployments require operator-managed database
backups; `daydanctl` is designed for SQLite snapshots.

## License

DayDan-Server is licensed under the AGPLv3. See [LICENSE.md](LICENSE.md).
