# Dydan Backup & Restore (self-hosted operators)

Dydan ships an operator toolkit, `dydanctl`, inside the production image. It
performs consistent SQLite online backups (via `VACUUM INTO`, safe with WAL
mode — never a raw copy of a live DB file), records SHA-256 checksums and an
integrity check in `manifest.json`, and restores with validation.

A backup contains:

- the SQLite database (`dydan.db`, consistent snapshot)
- the persisted JWT secret (`jwt-secret`)
- local uploads (`uploads/`)
- `manifest.json` (timestamp, version, per-file sizes/checksums, integrity)

JWT secret contents are **never** printed to logs by the tooling.

## One-off backup

```bash
docker compose run --rm backup
```

Backups land in `./dydan-backups/dydan-backup-<timestamp>/`
(override with `DYDAN_BACKUP_DIR`; retention keeps the newest 7, override
with `DYDAN_BACKUP_KEEP`).

## Scheduled backups

The default Compose setup intentionally does not run an always-on cron
container. Two supported options:

1. Host cron against the compose service:

   ```cron
   # /etc/cron.d/dydan-backup  (host user in the docker group)
   15 3 * * * cd /opt/dydan && docker compose run --rm backup >> dydan-backups/backup.log 2>&1
   ```

2. A systemd timer running the same command on your preferred schedule.

## Verify a backup

```bash
docker compose run --rm --entrypoint /usr/local/bin/dydanctl backup \
  verify /backups/dydan-backup-<timestamp>
```

Checks manifest presence, all checksums, and re-runs SQLite `integrity_check`.

## Restore (requires downtime)

Stop Dydan first — restore replaces the live database:

```bash
docker compose stop dydan

docker compose run --rm \
  --entrypoint /usr/local/bin/dydanctl backup \
  restore --backup /backups/dydan-backup-<timestamp> --data-dir /data

docker compose start dydan
```

Behaviour:

- refuses obviously invalid backups (missing manifest, checksum mismatch,
  failed integrity check)
- preserves current live data as `<data-dir>/pre-restore-<timestamp>/`
  unless `--no-safety-backup` is given
- refuses to run if the database appears to have an active writer unless
  `--force` is passed
- restores database, JWT secret, and uploads consistently

After restoring, log in and verify data before deleting any `pre-restore-*`
directory.

## Disaster recovery onto a fresh volume

Restore works onto an empty data directory (no existing database), which is
the path used after losing a host: create the volume, run `restore`, then
start Dydan.

## Retention only

```bash
docker compose run --rm --entrypoint /usr/local/bin/dydanctl backup prune --root /backups --keep 14
```
