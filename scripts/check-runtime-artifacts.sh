#!/usr/bin/env bash
set -euo pipefail

tracked=$(git ls-files)
forbidden=$(printf '%s\n' "$tracked" | grep -E '(^|/).*\.sqlite[0-9]*(-(wal|shm))?$|(^|/)jwt-secret$|(^|/)(data|uploads|node_modules)/|\.log$' || true)

if [[ -n "$forbidden" ]]; then
  echo "Tracked runtime or generated files are not allowed:" >&2
  printf '%s\n' "$forbidden" >&2
  exit 1
fi
