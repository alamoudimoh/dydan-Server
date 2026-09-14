# Migration Decisions — Server (DayDan → Dydan)

**Repository:** `alamoudimoh/DayDan-Server`  
**Audited Branch:** `audit/dydan-identity-server`  
**Baseline Audited:** `origin/develop` (`bfb59685bd878d8d0f3c986ec76f1ceb91badf7c`)  
**Audit Date:** 2026-09-14  
**Primary Inventory Reference:** [`identity-inventory.csv`](./identity-inventory.csv) (1,468 occurrence records across 271 files)

This document formalizes the migration decisions for all logical identity groups identified during the DayDan Server identity audit. All decisions follow the audit-first methodology established for the Dydan ecosystem. No runtime remediation has been performed in this phase.

---

## Classification Model Legend

- **A — Safe Rename:** Pure product identity, presentation, user-facing text, or internal naming with no compatibility concern. Rename to canonical Dydan identity.
- **B — Technical Rename:** Technical identifier requiring coordinated changes, builds, and verification (internal Go module, container naming, CLI binary, internal package types).
- **C — Compatibility Required:** Identifier consumed by existing deployments, external integrations, persisted storage, clients, or environment configurations. Must retain backward compatibility while introducing canonical Dydan identity.
- **D — Intentionally Preserve:** Legacy identifier that must remain for documented historical, migration, or backward-compatibility reasons.
- **E — Remove:** Obsolete or dead legacy content that should be removed rather than migrated.
- **F — Investigate:** External dependency or contract where owner action or external verification is required before remediation.

---

## Logical Identity Groups

### Group 1: Go Module & Package Import Tree

1. **Current Legacy Identity:**  
   `module github.com/alamoudimoh/daydan-server` in `go.mod` and all package import paths `github.com/alamoudimoh/daydan-server/...` across 226 Go files (816 total occurrences).
2. **Intended Dydan Identity:**  
   `module github.com/alamoudimoh/dydan-server` and imports `github.com/alamoudimoh/dydan-server/...`.
3. **Classification:**  
   **B — Technical Rename**
4. **Why:**  
   The Go module path defines the namespace for all internal packages. It is entirely internal to compilation, binary builds, and tests.
5. **Compatibility Impact:**  
   Zero runtime compatibility impact on deployed instances or clients once compiled. Third-party Go packages importing this repository as a library (if any) would need to update imports; however, this is an application monolith, not a public library.
6. **Dependencies:**  
   Must be executed atomically across `go.mod`, `go.sum`, all Go source files, `.goreleaser.yaml`, and `Dockerfile` `-ldflags`.
7. **Required Remediation Approach:**  
   Coordinated repository-wide refactoring of the module statement in `go.mod` and all Go imports, followed by `go mod tidy` and full `./scripts/verify.sh` test run.
8. **Verification Required After Remediation:**  
   `go build ./...`, `go test ./...`, and Docker compilation smoke tests must succeed cleanly with zero remaining references to the old module path.

---

### Group 2: Server Runtime Environment Variables

1. **Current Legacy Identity:**  
   33 `DAYDAN_*` environment variables in `config/env.go`, `config/config.go`, `Dockerfile`, `docker-compose.yaml`, and documentation:  
   `DAYDAN_CONFIG_FILE`, `DAYDAN_DATA_DIR`, `DAYDAN_SQLITE_PATH`, `DAYDAN_JWT_SECRET`, `DAYDAN_STORAGE_MEDIA_SECRET`, `DAYDAN_WEBHOOK_SIGNING_SECRET`, `DAYDAN_STRIPE_WEBHOOK_SECRET`, `DAYDAN_TRUSTED_PROXIES`, `DAYDAN_ENV`, `DAYDAN_DISABLE_PASSWORD_AUTH`, `DAYDAN_SINGLE_CIRCLE_INSTANCE`, `DAYDAN_DISABLE_SIGNUP`, `DAYDAN_OAUTH2_ADMIN_GROUPS`, `DAYDAN_OAUTH2_MANAGER_GROUPS`, `DAYDAN_OAUTH2_ALLOW_LEGACY_STATE`, `DAYDAN_LOG_LEVEL`, `DAYDAN_LOG_ENCODING`, `DAYDAN_LOG_DEVELOPMENT`, `DAYDAN_TELEGRAM_TOKEN`, `DAYDAN_PUSHOVER_TOKEN`, `DAYDAN_VERSION`, `DAYDAN_COMMIT`, `DAYDAN_DOCKER_VOLUME`, `DAYDAN_BACKUP_DIR`, `DAYDAN_BACKUP_KEEP`, `DAYDAN_FRONTEND_CONTEXT`, `DAYDAN_FRONTEND_COMMIT`, `DAYDAN_FRONTEND_REF`, `DAYDAN_SKIP_CONTAINER_SMOKE`, `DAYDAN_TEST_POSTGRES_DSN`, `DAYDAN_TEST_S3_ENDPOINT`, `DAYDAN_TEST_S3_ACCESS`, `DAYDAN_TEST_S3_SECRET`.
2. **Intended Dydan Identity:**  
   Canonical `DYDAN_*` environment variables (e.g., `DYDAN_CONFIG_FILE`, `DYDAN_DATA_DIR`, `DYDAN_JWT_SECRET`).
3. **Classification:**  
   **C — Compatibility Required**
4. **Why:**  
   Existing deployments, container orchestrators, Helm charts, `.env` files, and operator runbooks inject `DAYDAN_*` variables into the environment. Unilaterally dropping them would break existing deployments upon container upgrade.
5. **Compatibility Impact:**  
   High risk of deployment failure if compatibility is broken.
6. **Dependencies:**  
   Precedence resolution in `config/env.go` and `config/config.go`.
7. **Required Remediation Approach:**  
   Implement canonical `DYDAN_*` variables with fallback to `DAYDAN_*`, preserving existing `TASKIER_*` and `DONETICK_*` fallbacks:  
   `DYDAN_*` > `DAYDAN_*` > `TASKIER_*` > `DONETICK_*` / `DT_*` > built-in defaults.  
   Update documentation and examples to show `DYDAN_*` while explicitly stating that `DAYDAN_*` remains supported.
8. **Verification Required After Remediation:**  
   Unit tests in `config/env_test.go` asserting that canonical `DYDAN_*` variables take precedence when set, and that legacy `DAYDAN_*` variables continue to function when canonical variables are omitted.

---

### Group 3: Persisted SQLite Database & Path Resolution

1. **Current Legacy Identity:**  
   `daydanDBFilename = "daydan.db"` in `config/sqlite_path.go`, default data directory paths `/data/daydan.db`, and SQLite path resolution candidate logic.
2. **Intended Dydan Identity:**  
   `dydanDBFilename = "dydan.db"` as canonical, with `daydan.db` preserved as an automatic migration candidate.
3. **Classification:**  
   **C — Compatibility Required**
4. **Why:**  
   Existing self-hosted SQLite installations have live production databases stored at `/data/daydan.db` (including `-wal` and `-shm` sidecars). If the server switched its target filename to `dydan.db` without automatic migration, existing instances would start with an empty database, appearing as catastrophic data loss to the operator.
5. **Compatibility Impact:**  
   Critical. Must guarantee seamless, zero-downtime, non-destructive migration on container restart.
6. **Dependencies:**  
   `config/sqlite_path.go`, `cmd/daydanctl/main.go`, `docker-entrypoint.sh`, `scripts/verify-container.sh`.
7. **Required Remediation Approach:**  
   Update `resolveDefaultSQLitePath`:
   - Canonical target becomes `filepath.Join(dataDir, "dydan.db")`.
   - Add `filepath.Join(dataDir, "daydan.db")` as the primary legacy candidate ahead of `taskier.db` and `donetick.db`.
   - Maintain the safe file copy with companion WAL/SHM sidecars via `migrateLegacySQLiteDatabase`.
   - Update `docker-entrypoint.sh` to fix ownership on `dydan.db*` as well as `daydan.db*`.
8. **Verification Required After Remediation:**  
   Automated integration test simulating an upgrade of a container with an existing `/data/daydan.db`, verifying that data is copied to `/data/dydan.db` and the server boots with full user and session continuity.

---

### Group 4: Media URL HMAC Key Derivation & Salts

1. **Current Legacy Identity:**  
   `mediaSigningKeyInfo = "daydan media-url-signing v1"` and `mediaSigningKeySalt = "daydan/storage/media-url"` in `config/config.go` (lines 968–969).
2. **Intended Dydan Identity:**  
   Canonical `dydan media-url-signing v1` and `dydan/storage/media-url`, with legacy signature acceptance during the transition window.
3. **Classification:**  
   **C — Compatibility Required**
4. **Why:**  
   Local media attachments (chore photos, profile pictures, receipts) generate HMAC-SHA256 signatures derived using HKDF with this salt and info string. Changing either string alters the derived key, which would immediately invalidate all pre-signed media URLs stored in client caches or embedded in messages.
5. **Compatibility Impact:**  
   High. In-flight media URLs would yield HTTP 403 Forbidden on asset access until client re-signing occurred.
6. **Dependencies:**  
   `config/config.go`, `internal/storage/handler.go`, `internal/storage/signer_local.go`.
7. **Required Remediation Approach:**  
   Establish canonical keys using `dydan`. Support verifying legacy signatures generated with `daydan` parameters during a configurable transition window (similar to `allow_legacy_media_signatures`), while signing all new URLs with the canonical Dydan key.
8. **Verification Required After Remediation:**  
   Test verifying that an asset URL signed with the legacy `daydan` salt/info remains readable during the migration grace period, while newly minted URLs use canonical `dydan` keys.

---

### Group 5: Outbound Webhook Authentication Headers

1. **Current Legacy Identity:**  
   `X-Daydan-Signature` and `X-Daydan-Timestamp` in `internal/events/headers.go` and `internal/events/signing.go`.
2. **Intended Dydan Identity:**  
   `X-Dydan-Signature` and `X-Dydan-Timestamp` as canonical, with dual-emission of legacy headers.
3. **Classification:**  
   **C — Compatibility Required**
4. **Why:**  
   External consumers (such as Home Assistant automations, Node-RED, custom webhooks, or third-party notification services) parse `X-Daydan-Signature` to verify webhook authenticity. If the header name changes unilaterally, external consumers will reject valid webhooks as unauthenticated.
5. **Compatibility Impact:**  
   High for external integrations.
6. **Dependencies:**  
   `internal/events/headers.go`, `internal/events/signing.go`, `internal/events/producer.go`, `docs/webhook-signing.md`.
7. **Required Remediation Approach:**  
   Dual-emit headers on all outbound webhook deliveries:
   - `X-Dydan-Signature` (canonical)
   - `X-Dydan-Timestamp` (canonical)
   - `X-Daydan-Signature` (legacy compatibility header)
   - `X-Daydan-Timestamp` (legacy compatibility header)
   Document canonical headers in `docs/webhook-signing.md` and announce deprecation of `X-Daydan-*` for a future major release.
8. **Verification Required After Remediation:**  
   Unit tests in `internal/events/signing_test.go` asserting that both canonical and legacy headers are emitted on every webhook request with identical signature payloads.

---

### Group 6: Android Push Notification Channel [Cross-Repository Contract]

1. **Current Legacy Identity:**  
   `DayDanFCMChannelID = "daydan_notifications"` in `internal/notifier/service/fcm/channels.go`.
2. **Intended Dydan Identity:**  
   `DydanFCMChannelID = "dydan_notifications"` as canonical, with fallback retention of `daydan_notifications`.
3. **Classification:**  
   **C — Compatibility Required**  
   **Cross-Repository Contract with AppWeb (`alamoudimoh/DayDan-AppWeb`)**
4. **Why:**  
   Android `NotificationChannel` IDs are registered locally on the device OS and are immutable once created. AppWeb / Android native client has `daydan_notifications` registered (flagged in AppWeb audit as highest-risk contract #2). If Server begins sending pushes targeting `dydan_notifications` before the client creates that channel, Android may drop the notifications or fail to display them with configured sound/vibration.
5. **Compatibility Impact:**  
   Critical for mobile push notification delivery.
6. **Dependencies:**  
   Synchronized release coordination with AppWeb / Android native client.
7. **Required Remediation Approach:**  
   Follow the precedent already established for `LEGACY_DONETICK_FCM_CHANNEL_ID`. Client must register `dydan_notifications` while keeping `daydan_notifications`. Server should provide a transition mechanism or continue sending to `daydan_notifications` until AppWeb confirms channel rollout.
8. **Verification Required After Remediation:**  
   Channel resolution tests in `internal/notifier/service/fcm/channels_test.go` and cross-repo push notification verification.

---

### Group 7: Docker, Container Runtime & Volume Persistence

1. **Current Legacy Identity:**  
   - Docker service name: `daydan` in `docker-compose.yaml`
   - Named volume: `${DAYDAN_DOCKER_VOLUME:-${TASKIER_DOCKER_VOLUME:-daydan-data}}`
   - Container user/group: `daydan` (uid/gid 1000) in `Dockerfile` and `docker-entrypoint.sh`
   - Binary path in container: `/daydan`
   - Secondary container: `daydan-backup`
2. **Intended Dydan Identity:**  
   - Service name: `dydan`
   - Named volume fallback: `${DYDAN_DOCKER_VOLUME:-${DAYDAN_DOCKER_VOLUME:-${TASKIER_DOCKER_VOLUME:-dydan-data}}}`
   - Container user/group: `dydan` (preserving UID:GID 1000:1000)
   - Binary path: `/dydan` with symlink `/daydan`
   - Secondary container: `dydan-backup`
3. **Classification:**  
   **B — Technical Rename** (Service, User, Binary) & **C — Compatibility Required** (Volume persistence)
4. **Why:**  
   If the named volume in `docker-compose.yaml` switches to `dydan-data` without falling back to existing `daydan-data`, Docker will create a new empty volume, leaving existing data disconnected. If UID 1000 changes, file permissions in `/data` break.
5. **Compatibility Impact:**  
   Severe if volume fallback or UID 1000 is modified.
6. **Dependencies:**  
   `Dockerfile`, `docker-compose.yaml`, `docker-entrypoint.sh`, `scripts/verify-container.sh`.
7. **Required Remediation Approach:**  
   - Ensure UID/GID 1000:1000 is strictly preserved for user `dydan`.
   - Create symlinks `/daydan -> /dydan` and `/donetick -> /dydan` in the container image.
   - Maintain volume name fallback chain: `DYDAN_DOCKER_VOLUME` -> `DAYDAN_DOCKER_VOLUME` -> `TASKIER_DOCKER_VOLUME` -> `daydan-data` (or inspect volume existence).
8. **Verification Required After Remediation:**  
   Run `scripts/verify-container.sh` to test permissions, container entrypoint execution, and existing volume mounting.

---

### Group 8: Operator Toolkit CLI (`daydanctl`) & Backup Archives

1. **Current Legacy Identity:**  
   `cmd/daydanctl/main.go`, CLI binary name `daydanctl`, backup archive prefix `backupPrefix = "daydan-backup-"`, default backup path `./daydan-backups`.
2. **Intended Dydan Identity:**  
   `cmd/dydanctl/main.go`, binary `dydanctl`, backup prefix `dydan-backup-`, default path `./dydan-backups`.
3. **Classification:**  
   **B — Technical Rename** (CLI binary) & **C — Compatibility Required** (Archive restore & retention)
4. **Why:**  
   Operators have existing backup archives named `daydan-backup-YYYYMMDD-HHMMSS` on disk, and cron jobs executing `daydanctl backup` or `docker compose run --rm backup`.
5. **Compatibility Impact:**  
   Medium. Must ensure existing archives remain restorable, verifiable, and prunable by the new binary.
6. **Dependencies:**  
   `cmd/daydanctl/main.go`, `docs/backup-restore.md`, `Dockerfile`, `docker-compose.yaml`.
7. **Required Remediation Approach:**  
   - Rename CLI package/binary to `dydanctl`.
   - Provide symlink `/usr/local/bin/daydanctl -> /usr/local/bin/dydanctl` in the container image.
   - In `dydanctl verify`, `restore`, and `prune`, accept both `dydan-backup-` and `daydan-backup-` prefixes.
   - Default backup creation creates `dydan-backup-YYYYMMDD-HHMMSS`.
8. **Verification Required After Remediation:**  
   CLI tests in `cmd/dydanctl/main_test.go` verifying that `dydanctl` can verify and restore a legacy `daydan-backup-*` archive.

---

### Group 9: Sibling AppWeb Build Context & Embedding [Cross-Repository Contract]

1. **Current Legacy Identity:**  
   `--build-context frontend=../daydan-app` and `additional_contexts: frontend: ${DAYDAN_FRONTEND_CONTEXT:-../daydan-app}`, `DAYDAN_FRONTEND_COMMIT`, `DAYDAN_FRONTEND_REF`.
2. **Intended Dydan Identity:**  
   `${DYDAN_FRONTEND_CONTEXT:-${DAYDAN_FRONTEND_CONTEXT:-../daydan-app}}`, `DYDAN_FRONTEND_COMMIT`, `DYDAN_FRONTEND_REF`.
3. **Classification:**  
   **B — Technical Rename**  
   **Cross-Repository Contract with AppWeb (`alamoudimoh/DayDan-AppWeb`)**
4. **Why:**  
   Developers and CI systems clone repositories side by side (`daydan-app` and `daydan-server`). The Docker build embeds the frontend assets built from that context.
5. **Compatibility Impact:**  
   Local build script failure if sibling directory conventions diverge without fallback.
6. **Dependencies:**  
   `Dockerfile`, `docker-compose.yaml`, `scripts/verify-container.sh`, `.github/workflows/go-build.yml`, `.github/workflows/go-release.yml`.
7. **Required Remediation Approach:**  
   Support `DYDAN_FRONTEND_CONTEXT` while falling back to `DAYDAN_FRONTEND_CONTEXT` and default directory `../daydan-app` (until AppWeb local directory naming migration occurs).
8. **Verification Required After Remediation:**  
   Docker build smoke test verifying that `docker build --build-context frontend=...` succeeds.

---

### Group 10: Auth Providers & Session Handling

1. **Current Legacy Identity:**  
   `AuthProviderDayDan` enum constant in `internal/user/model/model.go` (line 87), and TOTP MFA `appName := "DayDan"` in `internal/mfa/service.go`. (Note: cookie name `refresh_token` and JWT claims are already product-neutral).
2. **Intended Dydan Identity:**  
   `AuthProviderDydan` (aliasing `AuthProviderDayDan = AuthProviderDydan`), and `appName := "Dydan"`.
3. **Classification:**  
   - `AuthProviderDayDan`: **B — Technical Rename**
   - MFA `appName`: **C — Compatibility Required**
4. **Why:**  
   `AuthProviderDayDan` evaluates to integer `0` stored in the database `users.provider` column. The integer `0` is immutable database state. Changing the constant name in Go must not change the numeric value.  
   MFA `appName` sets the TOTP issuer in authenticator apps (Google Authenticator, etc.).
5. **Compatibility Impact:**  
   Low for `AuthProviderDayDan` as long as value `0` is preserved.  
   Medium for MFA: existing TOTP tokens remain mathematically valid because the secret seed does not change, but the app label changes on newly enrolled tokens.
6. **Dependencies:**  
   `internal/user/model/model.go`, `internal/user/model/model_test.go`, `internal/mfa/service.go`.
7. **Required Remediation Approach:**  
   Define `AuthProviderDydan AuthProviderType = iota` and keep `const AuthProviderDayDan = AuthProviderDydan` as alias. Preserve unit test `TestAuthProviderDayDanPreservesLegacyNumericValue`.  
   Update `appName` in MFA service to `Dydan`.
8. **Verification Required After Remediation:**  
   `go test ./internal/user/model/...` and MFA setup/verification test.

---

### Group 11: Configuration Keys, Files, and Aliases

1. **Current Legacy Identity:**  
   - Config file names: `daydan.yaml`, `daydan.yml` in `config/config.go`
   - Viper/YAML keys: `is_daydan_cloud`, `daydan_cloud`
   - Default name: `Name: "daydan"`
   - Functions: `setDayDanDefaults()`, `applyDayDanCloudConfigAliases()`
2. **Intended Dydan Identity:**  
   - Config files: `dydan.yaml`, `dydan.yml` (canonical), falling back to `daydan.yaml`, `daydan.yml`, then `taskier.yaml`
   - YAML keys: `is_dydan_cloud`, `dydan_cloud` (with legacy aliases accepted)
   - Default name: `Name: "dydan"`
   - Functions: `setDydanDefaults()`, `applyDydanCloudConfigAliases()`
3. **Classification:**  
   **C — Compatibility Required** (filenames and YAML keys) & **B — Technical Rename** (internal functions)
4. **Why:**  
   Self-hosted operators may mount `/config/daydan.yaml`. If the server stopped reading `daydan.yaml`, configurations would be silently ignored.
5. **Compatibility Impact:**  
   High if config file fallback is omitted.
6. **Dependencies:**  
   `config/config.go`, `config/config_test.go`, `config/daydan.example.yaml`.
7. **Required Remediation Approach:**  
   Search order: `dydan.{yaml,yml}` -> `daydan.{yaml,yml}` -> `taskier.{yaml,yml}`.  
   Accept `is_dydan_cloud` with `is_daydan_cloud` fallback.
8. **Verification Required After Remediation:**  
   Config loading tests in `config/config_test.go` checking search precedence across all supported filenames.

---

### Group 12: Email Templates & Push Notifications

1. **Current Legacy Identity:**  
   User-facing strings in `internal/email/sender.go` ("Welcome to DayDan! Verify your email", "DayDan! Password Reset", email body text) and notification titles in `internal/notifier/service/pushover/pushover.go` and `internal/notifier/service/fcm/fcm.go` (`Title: "DayDan"`).
2. **Intended Dydan Identity:**  
   "Welcome to Dydan! Verify your email", "Dydan! Password Reset", Arabic: دَيْدَن, Title: "Dydan".
3. **Classification:**  
   **A — Safe Rename**
4. **Why:**  
   Pure product branding displayed to end users in emails and push notification headers. No programmatic dependency or machine-readable contract.
5. **Compatibility Impact:**  
   Zero technical compatibility risk.
6. **Dependencies:**  
   `internal/email/sender.go`, `internal/email/smtp2go_sender.go`, `internal/notifier/service/pushover/pushover.go`, `internal/notifier/service/fcm/fcm.go`.
7. **Required Remediation Approach:**  
   Update text strings directly to canonical Dydan / دَيْدَن.
8. **Verification Required After Remediation:**  
   Email rendering and notifier unit tests.

---

### Group 13: Swagger API Documentation & Metadata

1. **Current Legacy Identity:**  
   `docs/handler.go`, `docs/docs.go`, `docs/swagger.json`, `docs/swagger.yaml`:  
   Title: `DayDan Swagger API`, Description: `DayDan swagger documentation.`, License URL.
2. **Intended Dydan Identity:**  
   Title: `Dydan Swagger API`, Description: `Dydan swagger documentation.`.
3. **Classification:**  
   **A — Safe Rename**
4. **Why:**  
   Human-readable API documentation title and description.
5. **Compatibility Impact:**  
   Zero impact on API routes, request/response JSON schemas, or HTTP status codes.
6. **Dependencies:**  
   `docs/handler.go`, `swag` CLI generator.
7. **Required Remediation Approach:**  
   Update Swagger comments in `docs/handler.go` and regenerate Swagger artifacts via `swag init -g docs/handler.go`.
8. **Verification Required After Remediation:**  
   Verify that generated `docs.go`, `swagger.json`, and `swagger.yaml` compile and render cleanly.

---

### Group 14: CI/CD Workflows, Container Smoke Tests & Verification Scripts

1. **Current Legacy Identity:**  
   `.github/workflows/go-build.yml`, `.github/workflows/go-release.yml`, `.github/workflows/beta-release.yml`, `scripts/verify-container.sh`, `scripts/verify.sh`, `scripts/prepare-release.sh`, `scripts/resolve-frontend-ref.sh`, `scripts/verify-auth-e2e.sh`.
2. **Intended Dydan Identity:**  
   Workflow names and script variables updated to Dydan / dydan, supporting backward-compatible environment flags.
3. **Classification:**  
   **B — Technical Rename**
4. **Why:**  
   Internal automation and verification scripts used by developers and GitHub Actions.
5. **Compatibility Impact:**  
   Low to medium. Script flags such as `DAYDAN_SKIP_CONTAINER_SMOKE` must accept `DYDAN_SKIP_CONTAINER_SMOKE` with legacy fallback.
6. **Dependencies:**  
   `scripts/*.sh`, `.github/workflows/*.yml`.
7. **Required Remediation Approach:**  
   Update script variables, container names (`dydan-zero-config-...`), and workflow job titles.
8. **Verification Required After Remediation:**  
   Execute `./scripts/verify.sh` locally to confirm all checks pass.

---

### Group 15: Unit & Integration Test Suites, Fixtures, and Mock Domains

1. **Current Legacy Identity:**  
   Test fixtures containing `daydan.example.com`, `daydan-admins`, `daydan-managers`, and test descriptions across `config/*_test.go`, `internal/*/*_test.go`.
2. **Intended Dydan Identity:**  
   `dydan.example.com`, `dydan-admins`, `dydan-managers`, updated test names.
3. **Classification:**  
   **B — Technical Rename**
4. **Why:**  
   Test assertions and fixtures verifying server behavior.
5. **Compatibility Impact:**  
   Zero runtime impact on production systems.
6. **Dependencies:**  
   Go test files across the repository.
7. **Required Remediation Approach:**  
   Update test descriptions, assertions, and mock data to use Dydan, while retaining dedicated regression tests for legacy compatibility paths.
8. **Verification Required After Remediation:**  
   `go test -v -race ./...`.

---

### Group 16: Historical Baselines & Compatibility Assertions

1. **Current Legacy Identity:**  
   - `docs/product-implementation-review.md`: Lines citing historical repository SHAs `alamoudimoh/DayDan-AppWeb@8031cb...` and `alamoudimoh/DayDan-Server@cfb583...`.
   - `internal/user/model/model_test.go`: `TestAuthProviderDayDanPreservesLegacyNumericValue`.
2. **Intended Dydan Identity:**  
   Preserve historical references and legacy test assertions as-is with explanatory notes.
3. **Classification:**  
   **D — Intentionally Preserve**
4. **Why:**  
   Historical audit logs and review documents cite specific Git commits and repository states at the time of review. Modifying them destroys historical traceability.  
   The unit test explicitly locks the database enum value for legacy password users to `0`.
5. **Compatibility Impact:**  
   Essential for audit integrity and database backward compatibility.
6. **Dependencies:**  
   None.
7. **Required Remediation Approach:**  
   Leave historical commit references and compatibility tests intact; document why in audit artifacts.
8. **Verification Required After Remediation:**  
   Confirm `TestAuthProviderDayDanPreservesLegacyNumericValue` passes.

---

### Group 17: External Repositories & Cloud OAuth Client IDs

1. **Current Legacy Identity:**  
   - GitHub repository URLs: `https://github.com/alamoudimoh/daydan-app.git`, `https://github.com/alamoudimoh/DayDan-Server.git` in GitHub Actions workflows and documentation.
   - External OAuth configurations: `DayDanCloudConfig.AppleClientID`, `GoogleClientID` references in `internal/user/handler.go`.
2. **Intended Dydan Identity:**  
   `https://github.com/alamoudimoh/dydan-app.git`, `https://github.com/alamoudimoh/dydan-server.git`.
3. **Classification:**  
   **F — Investigate**
4. **Why:**  
   Renaming a remote GitHub repository slug requires repository owner action on GitHub. Doing so in workflow code prior to GitHub repository rename would immediately break CI checkout and release jobs.  
   Similarly, OAuth client IDs on Apple and Google developer consoles have registered redirect URIs that may include legacy product domains.
5. **Compatibility Impact:**  
   High if changed before external entities are updated.
6. **Dependencies:**  
   External repository owner action on GitHub and cloud provider developer consoles.
7. **Required Remediation Approach:**  
   Coordinate with repository owner:
   1. Keep current URLs until the GitHub repository slugs are officially updated.
   2. Ensure GitHub automatic redirects are active if repos are renamed.
   3. Verify OAuth consent screen and callback URIs before modifying client ID validation.
8. **Verification Required After Remediation:**  
   End-to-end OAuth login verification and CI checkout test.

---

## Summary of Remediation Sequence

1. **Phase 1: Dual-Compatibility Foundation (Zero-Risk Staging)**
   - Add `DYDAN_*` environment variables in `config/env.go` with `DAYDAN_*` fallback.
   - Add `dydan.{yaml,yml}` config file lookup with `daydan.{yaml,yml}` fallback.
   - Update `config/sqlite_path.go` to support `dydan.db` with `daydan.db` auto-migration.
   - Dual-emit webhook headers: `X-Dydan-Signature` + `X-Daydan-Signature`.
   - Update `docker-compose.yaml` volume fallback chain for `daydan-data`.
2. **Phase 2: Internal Technical Migration**
   - Coordinated Go module rename (`go.mod` and all Go imports).
   - Container runtime user (`dydan` with UID 1000) and binary symlinks (`/dydan` -> `/daydan`).
   - CLI rename `daydanctl` -> `dydanctl` with backward-compatible archive support.
   - Internal Go struct and helper renames.
3. **Phase 3: Presentation, Branding & Documentation**
   - User-facing email templates, notification titles, and error responses.
   - Swagger documentation regeneration.
   - Markdown documentation updates (`README.md`, `docs/*`).
4. **Phase 4: Coordinated Ecosystem Cutover**
   - Mobile FCM channel coordination with AppWeb client.
   - Coordinated GitHub repository slug updates with repository owner.
