# DayDan-Server architecture

## Application shape

DayDan-Server is a Go monolith. `main.go` wires configuration, database
connections, startup migrations, repositories, HTTP handlers, authentication,
notifications, schedulers, storage, payment adapters, event delivery,
realtime, and frontend serving.

Gin exposes the versioned REST API under `/api/v1`. Domain packages under
`internal/` own their handlers, models, repositories, and service behavior.
The major domains include chores, circles, users, subtasks, projects,
routines, shopping lists, maintenance, meters, Things, labels, progress,
points, rewards, storage, synchronization, notifications, and resources.
The generated API description is in `docs/swagger.yaml` and
`docs/swagger.json`.

## Persistence

SQLite is the default for a new self-hosted instance. The server configures
WAL mode, busy timeouts, bounded connections, and startup migrations. PostgreSQL
is supported for deployments that need a separately managed database.

Migrations are applied at startup with migration locking. The canonical
database is `/data/daydan.db`; existing installations may be migrated from
legacy database filenames without changing application behavior.

Local uploads live below `/data/uploads`. S3-compatible storage is available
when operators need object storage, including compatible endpoints such as
MinIO or Cloudflare R2.

## Realtime and background work

The public realtime transport is authenticated server-sent events at
`/api/v1/realtime/sse`. Native clients obtain a short-lived ticket so an
`EventSource` connection does not need to place an access token in a URL.
Connections are limited, heartbeated, cleaned up, and scoped to the relevant
circle and privacy rules.

Schedulers plan reminders, recurring work, notification inbox entries, and
other periodic domain operations. Outbound webhooks use bounded queues,
destination isolation, retries, backoff, and HMAC signing.

## Deployment shape

The Docker build compiles the frontend and backend, embeds the frontend into
the production image, and exposes a single origin for the SPA and `/api`.
Compose persists `/data` in `daydan-data`. The image includes a healthcheck
and runs without root privileges. Reverse-proxy deployments must configure
the trusted-proxy allowlist explicitly.
