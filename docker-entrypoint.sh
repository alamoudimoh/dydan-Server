#!/bin/sh
# Dydan container entrypoint.
#
# Starts as root with exactly one job: make sure the unprivileged runtime user
# can access the persistent data directory (/data). This keeps existing
# deployments working after `docker compose pull && docker compose up -d`, even
# when the volume was previously populated by the root-running image:
#
#   1. prepare ownership/permissions of /data (db, WAL/SHM sidecars,
#      jwt-secret, uploads)
#   2. drop privileges permanently (su-exec)
#   3. exec Dydan as the unprivileged user (signal handling preserved: the
#      Go binary replaces this shell via exec and receives SIGTERM directly)
set -eu

DATA_DIR="${DYDAN_DATA_DIR:-/data}"
RUN_USER="dydan"

TARGET_UID="$(id -u "$RUN_USER")"
TARGET_GID="$(id -g "$RUN_USER")"

log() {
  echo "[dydan-entrypoint] $*" >&2
}

# ensure_owner takes a cheap stat-based check before touching anything so that
# healthy volumes cost one stat per known path instead of a recursive chown.
ensure_owner() {
  path="$1"
  recursive="${2:-no}"
  if [ ! -e "$path" ]; then
    return 0
  fi
  current_uid="$(stat -c %u "$path" 2>/dev/null || echo "$TARGET_UID")"
  if [ "$current_uid" = "$TARGET_UID" ] && [ "$(stat -c %g "$path" 2>/dev/null || echo "$TARGET_GID")" = "$TARGET_GID" ]; then
    return 0
  fi
  log "fixing ownership of $path ($current_uid -> $TARGET_UID)"
  if [ "$recursive" = "yes" ]; then
    chown -R "$TARGET_UID:$TARGET_GID" "$path"
  else
    chown "$TARGET_UID:$TARGET_GID" "$path"
  fi
}

mkdir -p "$DATA_DIR"
ensure_owner "$DATA_DIR"

# Known data-dir artifacts: SQLite database plus WAL/SHM sidecars, the
# persisted JWT secret (and its temp files), and local uploads.
for entry in "$DATA_DIR"/*.db "$DATA_DIR"/*.db-wal "$DATA_DIR"/*.db-shm \
  "$DATA_DIR/jwt-secret" "$DATA_DIR"/.jwt-secret-*; do
  [ -e "$entry" ] || continue
  ensure_owner "$entry"
done

if [ -d "$DATA_DIR/uploads" ]; then
  # Only recurse when the tree actually needs migration (e.g. written by the
  # previous root-running image); otherwise this is a single stat.
  ensure_owner "$DATA_DIR/uploads" yes
fi

# Drop privileges permanently and exec Dydan in the same PID so PID 1 signal
# semantics (SIGTERM graceful shutdown) keep working.
exec su-exec "$TARGET_UID:$TARGET_GID" "${@:-/dydan}"
