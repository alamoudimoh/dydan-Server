# DayDan Server Verification Policy

- Run `./scripts/verify.sh` before completing substantial backend changes. It
  includes the zero-config container smoke test. Set
  `DAYDAN_SKIP_CONTAINER_SMOKE=true` only when that check runs separately, as
  it does in parallel CI jobs.
- Never bypass failing required CI; fix regressions before declaring work complete.
- Add a regression test for every reasonably testable bug fix and appropriate
  automated tests for every new backend feature.
- Keep Meter scheduling, points accounting/reset, Circle invitations and
  isolation, authentication, migrations, zero-config startup, JWT persistence,
  and SQLite restart persistence permanently covered.
- Commit tests with their feature or fix. Push only after relevant local checks
  pass, then inspect GitHub Actions and resolve failures when access permits.
