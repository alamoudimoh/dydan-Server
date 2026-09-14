# DayDan → Dydan Identity Audit — Server (Phase 01)

**Scope:** Identity-reference inventory and technical contract classification only. No runtime remediation, no code changes, no configuration changes, and no version bump.  
**Repository:** `alamoudimoh/DayDan-Server`  
**Audited Branch:** `audit/dydan-identity-server` (audit-only branch)  
**Baseline Scanned:** `origin/develop` (`bfb59685bd878d8d0f3c986ec76f1ceb91badf7c`)  
**Audit Date:** 2026-09-14  

---

## Executive Summary

This repository audit establishes the complete inventory of legacy product identity references (`DayDan`, `Daydan`, `daydan`, `DAYDAN`, and derived forms) across the DayDan Server codebase prior to any remediation work.

The canonical identities established for future migration are:
- **Product Name (English):** Dydan
- **Product Name (Arabic):** دَيْدَن
- **Technical Identifier (lowercase):** dydan
- **Technical Identifier (uppercase):** DYDAN

### Key Audit Metrics

| Metric | Value | Notes |
|---|---|---|
| **Total Tracked Files in Repository** | 343 | Tracked via `git ls-files` |
| **Total Files with Legacy Occurrences** | **271** | 79.0% of tracked repository files |
| **Files with ONLY Go Module Imports** | 193 | `github.com/alamoudimoh/daydan-server/...` only |
| **Files with Other Contracts / Content** | 78 | Config, Docker, scripts, database, docs, tests, etc. |
| **Total Legacy Occurrences Discovered** | **1,468** | Complete inventory in `identity-inventory.csv` |
| **Go Module & Package Import Occurrences** | 816 | 1 in `go.mod` + 815 across Go source and test files |
| **Non-Import Legacy Occurrences** | 652 | Technical contracts, env vars, config, docs, tests |
| **Runtime Remediation Performed** | **0** | **STRICT AUDIT ONLY** — working tree clean of code changes |

---

## Distribution by Classification

Every legacy occurrence is classified using the six-tier disposition model:

| Classification | Count | Percentage | Description & Expected Action |
|---|---|---|---|
| **A — Safe Rename** | 88 | 6.0% | Pure product identity, presentation, user-facing email templates, push titles, Swagger metadata, and documentation prose. Safe to rename directly to Dydan / دَيْدَن. |
| **B — Technical Rename** | 1,097 | 74.7% | Internal Go module declaration and package import tree (816), internal Docker image/service naming, internal CLI binary (`daydanctl`), internal Go types, functions, and test fixtures. Requires coordinated migration and build verification. |
| **C — Compatibility Required** | 265 | 18.1% | Identifiers consumed by existing deployments, external integrations, persisted databases, or clients. Includes 33 `DAYDAN_*` environment variables, `daydan.db` SQLite path resolution, `X-Daydan-*` webhook signature headers, `daydan_notifications` Android FCM channel, `daydan-data` Docker volume, and `daydan-backup-*` archive prefixes. |
| **D — Intentionally Preserve** | 3 | 0.2% | Immutable historical references: historical commit SHA anchors in review documentation and unit test assertion `TestAuthProviderDayDanPreservesLegacyNumericValue` locking database compatibility for enum value 0. |
| **E — Remove** | 0 | 0.0% | No obsolete or dead code was identified for removal. All occurrences represent active server contracts, code, or documentation. |
| **F — Investigate** | 15 | 1.0% | External GitHub repository slugs in CI workflows (`alamoudimoh/daydan-app`, `alamoudimoh/DayDan-Server`) and cloud OAuth configurations requiring owner action on external platforms before code changes. |
| **Total** | **1,468** | **100.0%** | **0 unexplained legacy references remain** |

---

## Distribution by Risk Level

| Risk Level | Count | Context |
|---|---|---|
| **High** | 202 | Persisted databases (`daydan.db`), environment variables (`DAYDAN_*`), Docker volume mounts (`daydan-data`), webhook signature headers (`X-Daydan-*`), FCM channel (`daydan_notifications`), and external GitHub slugs. |
| **Medium** | 1,051 | Internal Go module paths (816 occurrences), container user/runtime, operator CLI, and verification scripts. |
| **Low** | 215 | Presentation text, documentation prose, email subjects, Swagger metadata, and unit test assertions. |

---

## Major Compatibility Risks (Must Be Preserved During Migration)

1. **Persisted SQLite Database (`daydan.db`)**  
   Existing self-hosted installations store live production databases at `/data/daydan.db` (with WAL/SHM sidecars). Unilaterally changing the canonical target to `dydan.db` would start existing deployments on an empty database. Auto-migration must copy `daydan.db` to `dydan.db` on startup via `resolveDefaultSQLitePath`.
2. **Android FCM Notification Channel (`daydan_notifications`) — [Cross-Repository Contract]**  
   Android OS `NotificationChannels` are immutable once registered on user devices. AppWeb and the native Android client register `daydan_notifications`. If Server changes its push payload channel ID to `dydan_notifications` before the client registers it, pushes will fail or misbehave on existing installations.
3. **Outbound Webhook Signature Headers (`X-Daydan-Signature`, `X-Daydan-Timestamp`)**  
   External consumers (such as Home Assistant or third-party webhooks) verify webhook payloads using `X-Daydan-Signature`. The server must dual-emit `X-Dydan-*` and `X-Daydan-*` headers for backward compatibility.
4. **Server Environment Variable Precedence (`DAYDAN_*`)**  
   33 distinct `DAYDAN_*` environment variables configure runtime behavior, JWT secrets, database paths, and proxy trust. Canonical `DYDAN_*` variables must be introduced with fallback: `DYDAN_*` > `DAYDAN_*` > `TASKIER_*` > `DONETICK_*` > defaults.
5. **Media URL HMAC Key Derivation Salt & Info**  
   Signed media URLs use HKDF key derivation with `daydan media-url-signing v1` and `daydan/storage/media-url`. Changing these strings immediately invalidates all active signed asset URLs. A transition window supporting legacy verification is required.
6. **Docker Named Volume Persistence (`daydan-data`)**  
   `docker-compose.yaml` mounts `daydan-data:/data`. The volume fallback chain `${DYDAN_DOCKER_VOLUME:-${DAYDAN_DOCKER_VOLUME:-${TASKIER_DOCKER_VOLUME:-daydan-data}}}` must be maintained to prevent data detachment.
7. **Operator Toolkit Backup Archives (`daydan-backup-*`)**  
   Existing backup directories and cron jobs rely on `daydan-backup-YYYYMMDD-HHMMSS`. The migrated `dydanctl` CLI must recognize and restore legacy `daydan-backup-*` archives.

---

## Major Technical Migration Areas

1. **Go Module Declaration & Package Import Tree:**  
   Coordinated rename of `module github.com/alamoudimoh/daydan-server` to `github.com/alamoudimoh/dydan-server` across `go.mod` and 815 import statements across 226 Go files.
2. **Container Image & Runtime User:**  
   Docker container runtime user `daydan` (UID:GID 1000:1000) migrated to `dydan`, strictly maintaining UID 1000 to preserve file ownership on mounted `/data` volumes. Container binary `/dydan` with symlinks `/daydan` and `/donetick`.
3. **Operator CLI (`daydanctl` → `dydanctl`):**  
   Rename binary to `dydanctl`, provide a container symlink `/usr/local/bin/daydanctl -> /usr/local/bin/dydanctl`, and retain legacy backup prefix parsing.
4. **Configuration Keys & File Resolution:**  
   Search order: `dydan.{yaml,yml}` > `daydan.{yaml,yml}` > `taskier.{yaml,yml}`. Config alias support for `is_dydan_cloud` and `dydan_cloud`.
5. **Sibling AppWeb Build Context:**  
   Default `--build-context frontend=../daydan-app` maintained via `${DYDAN_FRONTEND_CONTEXT:-${DAYDAN_FRONTEND_CONTEXT:-../daydan-app}}`.

---

## Cross-Repository Contracts Identified with AppWeb

| Contract | Server Component | AppWeb Component | Migration Requirement |
|---|---|---|---|
| **Android FCM Channel** | `internal/notifier/service/fcm/channels.go` (`DayDanFCMChannelID = "daydan_notifications"`) | `src/utils/notificationChannels.js` (`DAYDAN_FCM_CHANNEL_ID = 'daydan_notifications'`) | Must retain dual-channel support across both repositories until all installed mobile clients migrate. |
| **Frontend Build Context** | `Dockerfile`, `docker-compose.yaml` (`frontend=../daydan-app`) | AppWeb repository checkout directory name | Coordinated default directory migration with environment variable overrides. |
| **Allowed Hosts & Proxy** | `DAYDAN_TRUSTED_PROXIES` | `DAYDAN_ALLOWED_HOSTS` in `vite.allowed-hosts.mjs` | Coordinated introduction of `DYDAN_*` variables with legacy fallbacks. |
| **Backup Archive Naming** | `cmd/daydanctl/main.go` (`daydan-backup-*`) | `src/utils/backupFilenames.js` (`daydan-backup`) | Server and client backup prefixes aligned to `dydan-backup-`. |
| **External Issue / Repo URLs** | `README.md`, `docs/*`, `.github/workflows/*` | `src/config/ReportDestinations.js`, `AppBranding.js` | Coordinated GitHub repository slug updates with repository owner. |

---

## Investigation Items (Requiring Owner Action)

The 15 items classified as **F — Investigate** require explicit owner action before remediation:
1. **GitHub Repository Slugs:**  
   `.github/workflows/go-release.yml`, `.github/workflows/beta-release.yml`, and `.github/workflows/go-build.yml` clone and reference `github.com/${{ github.repository_owner }}/daydan-app`. Renaming the slug in CI workflows before the repository is renamed on GitHub will immediately break builds.
2. **Apple & Google Cloud OAuth Credentials:**  
   `DayDanCloudConfig.AppleClientID`, `GoogleClientID`, `GoogleIOSClientID`, and `GoogleAndroidClientID` in `internal/user/handler.go` validate OAuth tokens issued by third-party identity providers. Verify that redirect URIs and consent screen branding in external developer consoles are updated before modifying cloud config definitions.

---

## Recommended Remediation Sequence

1. **Phase 1: Zero-Downtime Compatibility Foundation**
   - Implement `DYDAN_*` environment variable precedence with `DAYDAN_*` fallback.
   - Implement `dydan.{yaml,yml}` configuration search with `daydan.{yaml,yml}` fallback.
   - Update `config/sqlite_path.go` to support `dydan.db` with auto-migration from `daydan.db`.
   - Dual-emit `X-Dydan-*` and `X-Daydan-*` webhook signature headers.
   - Retain `${DYDAN_DOCKER_VOLUME:-${DAYDAN_DOCKER_VOLUME:-daydan-data}}` in `docker-compose.yaml`.
2. **Phase 2: Internal Technical Migration**
   - Atomically refactor Go module path `github.com/alamoudimoh/dydan-server` across `go.mod` and all Go files.
   - Rename container user to `dydan` (preserving UID:GID 1000:1000) and binary to `/dydan` with symlinks.
   - Migrate `cmd/daydanctl` to `cmd/dydanctl` with archive backward compatibility.
   - Refactor internal Go struct fields, types, and constants (`AuthProviderDydan` with legacy alias).
3. **Phase 3: Presentation, Display & Documentation**
   - Update user-facing email templates ("Welcome to Dydan!", "Dydan! Password Reset", Arabic: دَيْدَن).
   - Update push notification title strings to `Dydan`.
   - Regenerate Swagger documentation metadata.
   - Update repository documentation (`README.md`, `docs/*`).
4. **Phase 4: Coordinated Ecosystem Cutover**
   - Synchronize Android FCM channel cutover with AppWeb release.
   - Coordinate GitHub repository slug renames with repository owner.

---

## Confirmation of Audit Scope

- **Zero Runtime Code Modified:** No Go source files, configuration files, Dockerfiles, scripts, workflows, or database migrations were modified.
- **Zero Configuration Drift:** The application compiles and runs identically to `origin/develop`.
- **Target Achieved:** Exactly 0 unexplained legacy DayDan references remain. Every single occurrence (1,468 total across 271 files) has been identified, cataloged, classified, and mapped to a concrete remediation decision.

---

## Primary Audit Artifacts

- [`identity-inventory.csv`](./identity-inventory.csv) — Machine-readable CSV inventory (1,468 rows, 13 columns).
- [`migration-decisions.md`](./migration-decisions.md) — Remediation decision groups, compatibility analyses, and verification gates.
- [`README.md`](./README.md) — Executive audit summary (this file).
