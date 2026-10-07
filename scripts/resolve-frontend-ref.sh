#!/usr/bin/env bash
# resolve-frontend-ref.sh — resolve the Dydan AppWeb ref a release build must
# embed, pinning it to exactly one immutable commit SHA.
#
# Inputs (environment):
#   INPUT_REF     explicit Dydan AppWeb ref (tag/branch/full SHA); may be empty
#   TAG           the server release tag being built (e.g. v1.2.3)
#   IS_TAG_PUSH   "true" for official tagged releases (push of a vX.Y.Z tag)
#   REPO_URL      URL of the Dydan AppWeb git repository
#   GITHUB_OUTPUT optional; when set (GitHub Actions), results are appended
#                 there as well as printed to stdout as key=value lines.
#
# Resolution rules:
# - Official tagged release: the exact same tag must exist in AppWeb,
#   otherwise this script FAILS. A release must never silently embed a moving
#   branch such as main.
# - Manual dispatch with frontend_ref: that ref is used as-is.
# - Manual dispatch without frontend_ref: AppWeb main HEAD is used and
#   the result is flagged reproducible=false (non-reproducible dev build).
#
# Outputs: "ref", "sha" and "reproducible" lines.

set -euo pipefail

REPRODUCIBLE=true

if [ -n "${INPUT_REF:-}" ]; then
  REF="$INPUT_REF"
  echo "Using explicit frontend ref: $REF"
elif [ "${IS_TAG_PUSH:-false}" = "true" ]; then
  if git ls-remote --exit-code --tags "$REPO_URL" "refs/tags/$TAG" >/dev/null 2>&1; then
    REF="$TAG"
    echo "Found synchronized frontend tag: $REF"
  else
    echo "::error::Official release $TAG has no matching Dydan AppWeb tag."
    echo "::error::Refusing to build against a moving branch. Create the synchronized frontend tag first"
    echo "::error::(scripts/prepare-release.sh) or dispatch manually with an explicit frontend_ref."
    exit 1
  fi
else
  REF="main"
  REPRODUCIBLE=false
  echo "No frontend ref supplied for manual build; using moving branch: $REF"
  echo "This build is NON-REPRODUCIBLE (development build)."
fi

# Resolve the ref to exactly one commit SHA now. Peel annotated tags so the
# recorded provenance is always the frontend commit itself.
if [[ "$REF" =~ ^[0-9a-fA-F]{40}$ ]]; then
  SHA="$REF"
else
  SHA=$(git ls-remote "$REPO_URL" "refs/tags/$REF" | awk '{print $1}')
  PEELED=$(git ls-remote "$REPO_URL" "refs/tags/${REF}^{}" | awk '{print $1}')
  if [ -n "$PEELED" ]; then
    SHA="$PEELED"
  fi
  if [ -z "$SHA" ]; then
    SHA=$(git ls-remote "$REPO_URL" "refs/heads/$REF" | awk '{print $1}')
  fi
fi
if [ -z "$SHA" ]; then
  echo "::error::Could not resolve Dydan AppWeb ref '$REF' to a commit."
  exit 1
fi

echo "Frontend ref $REF resolved to commit $SHA"

print_results() {
  echo "ref=$REF"
  echo "sha=$SHA"
  echo "reproducible=$REPRODUCIBLE"
}

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  print_results >> "$GITHUB_OUTPUT"
fi
print_results
