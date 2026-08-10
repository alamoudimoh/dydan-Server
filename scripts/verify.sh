#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"

bash scripts/check-runtime-artifacts.sh
bash scripts/check-time-utc.sh
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
