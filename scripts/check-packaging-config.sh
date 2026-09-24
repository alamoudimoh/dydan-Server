#!/usr/bin/env bash
# Validate the packaging/deployment configuration: canonical GHCR identity,
# no Docker Hub publishing, no legacy daydan production targets, and a
# published-image default compose that still supports local source builds.
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"

canonical="ghcr.io/alamoudimoh/dydan"

fail() {
  echo "packaging-config check failed: $1" >&2
  exit 1
}

# --- Default compose must use the canonical published image ---------------
compose_output=$(mktemp)
trap 'rm -f "$compose_output"' EXIT

# Without DYDAN_IMAGE set, the resolved compose config must reference the
# canonical latest image for both services.
env -u DYDAN_IMAGE docker compose config >"$compose_output"
grep -Eq "image: ${canonical}:latest" "$compose_output" ||
  fail "default compose does not resolve to ${canonical}:latest"
grep -Fq "DYDAN_IMAGE:-${canonical}:latest" docker-compose.yaml ||
  fail "docker-compose.yaml must default DYDAN_IMAGE to ${canonical}:latest"

# Operators may pin any published tag, so DYDAN_IMAGE must be overriding.
DYDAN_IMAGE="${canonical}:202609-05" docker compose config | grep -Fq "image: ${canonical}:202609-05" ||
  fail "DYDAN_IMAGE override is not applied"

if grep -Eq '^\s+image: .*daydan' docker-compose.yaml; then
  fail "legacy daydan image reference found in docker-compose.yaml"
fi

# --- Local source-build path must survive as a documented override ---------
grep -Fq 'docker-compose.build.yaml' README.md ||
  fail "README must document the local source-build override file"
grep -Fq 'additional_contexts' docker-compose.build.yaml ||
  fail "docker-compose.build.yaml must carry the frontend build context"

# --- go-release.yml must publish to GHCR only -------------------------------
release_yml=".github/workflows/go-release.yml"
grep -Fq 'registry: ghcr.io' "$release_yml" ||
  fail "go-release.yml must authenticate to ghcr.io"
grep -Fq 'packages: write' "$release_yml" ||
  fail "go-release.yml permissions must include packages: write"
grep -Eq 'ghcr.io/\${{ github.repository_owner }}/dydan' "$release_yml" ||
  fail "go-release.yml must tag the canonical ${canonical} image"
if grep -Eq 'DOCKER_USERNAME|DOCKER_PASSWORD|docker\.io|hub\.docker' "$release_yml"; then
  fail "Docker Hub publishing logic still present in go-release.yml"
fi

# Calendar tags are the current shared AppWeb/Server release convention. Keep
# accepting historic semantic tags so existing operators can rebuild them.
bash scripts/validate-release-tag.sh v202609-13
bash scripts/validate-release-tag.sh v1.2.46
if bash scripts/validate-release-tag.sh 202609-13 >/dev/null 2>&1; then
  fail "release tags must remain v-prefixed"
fi
