#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"

bash scripts/check-runtime-artifacts.sh
bash scripts/check-time-utc.sh
bash scripts/check-packaging-config.sh
go test ./...
go build ./...
go test -race \
  ./config \
  ./internal/database \
  ./migrations \
  ./internal/meter/... \
  ./internal/chore/repo \
  ./internal/circle \
  ./internal/circle/repo

if [[ "${DYDAN_SKIP_CONTAINER_SMOKE:-false}" != "true" ]]; then
  bash scripts/verify-container.sh
fi
