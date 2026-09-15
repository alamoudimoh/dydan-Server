# Dydan Final Identity Audit

## App

- DayDan total remaining: 73
- Donetick total remaining: 227
- UNINTENDED_ACTIVE: 0
- INTENTIONAL_COMPAT: 149
- EXTERNAL_PENDING: 77
- FIX_REQUIRED: 0
- highest risk: HIGH
- final verdict: Clean / Preserved Contracts (No code remediation required; all remaining references are preserved native application identifiers, widget deep links, FCM notification channel IDs, storage migration fallbacks, external owner pending references, legal policies, or documentation).

## Server

- DayDan total remaining: 492
- Donetick total remaining: 86
- UNINTENDED_ACTIVE: 0
- INTENTIONAL_COMPAT: 168
- EXTERNAL_PENDING: 49
- FIX_REQUIRED: 0
- highest risk: HIGH
- final verdict: Clean / Verified (All 10 previously identified FIX_REQUIRED items in cmd/dydanctl/main.go, internal/realtime/auth_middleware.go, internal/user/handler.go, and internal/user/hosted_restriction_test.go have been resolved. Remaining references are preserved backward compatibility contracts, external pending references, or documentation/tests).

## Regressions Closed (commit: fix: close final Dydan identity regressions)

- FINDING-1 RESOLVED — scripts/prepare-release.sh line 36, docker-compose.yaml lines 10/32,
  scripts/verify-container.sh line 124, scripts/verify-auth-e2e.sh line 47: frontend build-context
  default corrected from legacy ../daydan-app to canonical ../dydan-app. The legacy path remains only
  as an existence-guarded fallback for an unchanged developer checkout (clearly diagnosed otherwise).
- FINDING-2 RESOLVED — config/local.yaml line 100: apple_client_id default corrected from legacy
  com.donetick.app to canonical com.dydan.app (matches the Mobile app's registered bundle identifier).
- Inventory rows FINAL-SRV-0270/0332/0336/0549/0567/0576 updated to reflect the resolution; the
  original matched values are preserved for provenance and their Action is now RESOLVED.

## Product Experience

- DayDan total remaining: 21
- Donetick total remaining: 0
- UNINTENDED_ACTIVE: 0
- INTENTIONAL_COMPAT: 0
- EXTERNAL_PENDING: 0
- FIX_REQUIRED: 0
- highest risk: LOW
- final verdict: Clean / Verified (No code remediation required; all remaining references are historical design brief and brand summary documentation preserved for provenance in src/imports/).
