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

The client is React 19/Vite with TanStack Query, authenticated SSE, native
offline storage, widgets, deep links, localization, Arabic RTL, and responsive
Figma-derived surfaces. The server is a Go/Gin monolith using Fx, GORM,
SQLite-first persistence, optional PostgreSQL, startup migrations, and embedded
frontend serving.

## Confirmed capabilities

The implementation contains circles and members, assignments, recurring
chores, subtasks, labels, projects, routines, shopping lists, maintenance,
meters, Things, attachments, timers, history, points, rewards, progress,
calendar, dashboards, search, notifications, account settings, MFA, API
tokens, managed child accounts, storage adapters, and payment/integration
boundaries.

## Security and reliability

Confirmed controls include rotating refresh sessions, credential-version
invalidation, session revocation, TOTP MFA, hashed API tokens, circle and
managed-child authorization, authentication and API rate limits, trusted
proxy allowlists, security headers, signed URLs, separate media signing
secrets, outbound destination checks, HMAC-signed webhooks, privacy-scoped
realtime broadcasts, bounded retry queues, and runtime artifact checks.

## Deployment

The recommended deployment is Docker Compose with the paired repositories.
SQLite and `/data/daydan.db` are the zero-config path; PostgreSQL is optional.
The image exposes port `2021`, serves the API and embedded client from one
origin, persists uploads and secrets under `/data`, and includes `daydanctl`
for SQLite backup/restore verification and retention.

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

These themes are evidenced by the recent `develop` histories and the
corresponding `internal/`, `src/`, `android/`, `ios/`, `scripts/`, and
workflow paths.

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
