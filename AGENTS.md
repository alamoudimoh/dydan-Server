# Taskier Server Verification Policy

- Run `./scripts/verify.sh` before completing substantial backend changes.
- Run `./scripts/verify-container.sh` when changing configuration, migrations,
  persistence, Docker, frontend serving, or startup behavior.
- Never bypass failing required CI; fix regressions before declaring work complete.
- Add a regression test for every reasonably testable bug fix and appropriate
  automated tests for every new backend feature.
- Keep Meter scheduling, points accounting/reset, Circle invitations and
  isolation, authentication, migrations, zero-config startup, JWT persistence,
  and SQLite restart persistence permanently covered.
- Commit tests with their feature or fix. Push only after relevant local checks
  pass, then inspect GitHub Actions and resolve failures when access permits.
