# DayDan product implementation review

**Reviewed:** 2026-09-12
**Repositories:** `alamoudimoh/DayDan-AppWeb` and `alamoudimoh/DayDan-Server`
**Baseline:** `develop`

This evidence baseline records the current product and the engineering themes
that materially shape its documentation.

## Product and architecture

DayDan is a self-hostable household coordination product. AppWeb supplies the
browser/PWA and Capacitor mobile experience; Server supplies the API,
authentication, persistence, migrations, realtime events, notifications,
integrations, media storage, and deployment image.

The client is React 19/Vite with React Router, TanStack Query, authenticated
SSE, native offline storage, widgets, deep links, localization, Arabic RTL,
and responsive Figma-derived surfaces. The server is a Go/Gin monolith using Fx, GORM,
SQLite-first persistence, optional PostgreSQL, startup migrations, and embedded
frontend serving.

## Confirmed capabilities

The implementation contains circles and members, assignments, recurring
chores, subtasks, labels, projects, routines, shopping lists, maintenance,
meters, Things, attachments, timers, history, points, rewards, progress,
calendar, dashboards, search, notifications, account settings, MFA, API
tokens, managed child accounts, storage adapters, payment/integration
boundaries, Home Assistant support, notification targets, and webhooks.

Representative evidence: client route modules under `src/views/`, server
domains under `internal/chore`, `internal/circle`, `internal/subtask`,
`internal/project`, `internal/routine`, `internal/shoppinglist`,
`internal/maintenance`, `internal/meter`, `internal/thing`, `internal/points`,
`internal/reward`, `internal/progress`, `internal/notifier`, and
`internal/events`.

## Security and reliability

Confirmed controls include rotating refresh sessions, credential-version
invalidation, session revocation, TOTP MFA, hashed API tokens, circle and
managed-child authorization, authentication and API rate limits, trusted
proxy allowlists, security headers, signed URLs, separate media signing
secrets, outbound destination checks, HMAC-signed webhooks, privacy-scoped
realtime broadcasts, bounded retry queues, and runtime artifact checks.

Representative evidence: `internal/auth/session_manager.go`,
`internal/auth/access_validation.go`, `internal/auth/role_resolver.go`,
`internal/mfa/`, `internal/authlimit/`, `internal/realtime/ticket_store.go`,
`internal/realtime/service.go`, `internal/utils/middleware.go`,
`config/config.go`, `frontend/handler.go`, `internal/events/signing.go`,
`internal/storage/`, and `internal/outbound/`.

## Mobile and offline

Representative evidence includes AppWeb's
`src/utils/NativeCredentialStorage.js`, `src/utils/OfflineDB.js`,
`src/utils/SyncEngine.js`, `src/service/WidgetService.js`,
`src/CapacitorListener.js`, `android/`, `ios/`, and the native plugin
dependencies in `package.json`. These cover secure credentials, native SQLite,
queued sync, widgets, deep links, NFC, speech, OCR/scanning, notifications,
and RevenueCat integration. Local AI is platform-dependent rather than a
general browser capability.

## Deployment and operations

The recommended deployment is Docker Compose with the paired repositories.
SQLite and `/data/daydan.db` are the zero-config path; PostgreSQL is optional.
The image exposes port `2021`, serves the API and embedded client from one
origin, persists uploads and secrets under `/data`, runs as an unprivileged
runtime user, includes a healthcheck, and includes `daydanctl` for SQLite
backup/restore verification and retention.

Representative evidence: `Dockerfile`, `docker-compose.yaml`,
`docker-entrypoint.sh`, `internal/database/`, `migrations/`,
`cmd/daydanctl/main.go`, `docs/backup-restore.md`, and
`docs/reverse-proxy.md`.

## Evolution themes

Recent merged history shows three material streams of DayDan work:

1. Figma-derived UX modernization across the shared shell, home/Today,
   tasks/calendar, household organization, execution surfaces, auth, and
   payment/public screens.
2. Security hardening across refresh contention, session boundaries, realtime
   tickets, proxy trust, CORS, webhook/payment validation, storage signing,
   external destinations, and private-event isolation.
3. Production-readiness work across reproducible releases, Docker
   distribution, zero-config startup, migrations, notification behavior,
   reliability verification, and operator backup tooling.

These themes are evidenced by AppWeb Figma migration PRs #38, #40, #42, #43,
#44, and #45; and Server security/reliability PRs #8, #10, #11, #12, #13,
#14, #15, #16, #17, #18, #19, #20, #21, and #22. Representative commits
include `546227c` for security headers, `f5b6fd7` for realtime ticket
hardening, `6ccb5b1` for media-secret separation, `2851a2b` for outbound
destination policy, `fa68833` for production reliability, and `0404ccc` for
reproducible releases.

## Current limitations

- Native offline behavior is broader than browser offline behavior and depends
  on supported sync commands.
- Widgets are bounded snapshots rather than full task clients.
- Local AI and some capture capabilities depend on native platform support.
- The notification center is a retained production route whose complete UX
  specification is still narrower than the rest of the modernized surfaces.
- `daydanctl` does not replace PostgreSQL-native backup and restore tooling.

## Compatibility identifiers

Existing installations and release operations may still require legacy
database filenames, Docker volume names, environment-variable aliases, mobile
bundle/deep-link identifiers, Apple signing records, and Vaultwarden item
names. These remain operational compatibility identifiers only.
