#!/usr/bin/env bash
# prepare-release.sh — create a synchronized release tag across both Dydan
# repositories so an official vX.Y.Z release deterministically pairs one server
# commit with one frontend commit.
#
# Usage:
#   scripts/prepare-release.sh vX.Y.Z [/path/to/dydan-app]
#
# The script only creates LOCAL annotated tags after validating everything;
# it never pushes and never overwrites anything. Pushing is a separate,
# explicit maintainer action (the commands are printed at the end).

set -euo pipefail

usage() {
  echo "Usage: $0 vX.Y.Z [/path/to/dydan-app]" >&2
  exit 2
}

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  usage
fi
TAG="$1"

if ! [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "ERROR: '$TAG' is not a stable semver tag of the form vX.Y.Z." >&2
  echo "       Beta/pre-release tags are cut by beta-release.yml and must not be created here." >&2
  exit 1
fi

SERVER_REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)

if [ $# -eq 2 ]; then
  APP_REPO="$2"
else
  # Canonical default is ../dydan-app; fall back to ../daydan-app if it exists
  CANONICAL="$SERVER_REPO/../dydan-app"
  LEGACY="$SERVER_REPO/../daydan-app"
  if [ -d "$CANONICAL" ]; then
    APP_REPO="$CANONICAL"
  elif [ -d "$LEGACY" ]; then
    APP_REPO="$LEGACY"
  else
    echo "ERROR: Frontend repository not found at canonical path '$CANONICAL' or legacy path '$LEGACY'." >&2
    echo "       Provide an explicit path as the second argument." >&2
    exit 1
  fi
fi
APP_REPO=$(cd "$APP_REPO" && pwd)

for repo in "$SERVER_REPO" "$APP_REPO"; do
  name=$(basename "$repo")
  git -C "$repo" rev-parse --is-inside-work-tree >/dev/null 2>&1 \
    || { echo "ERROR: $name ($repo) is not a git repository." >&2; exit 1; }

  if [ -n "$(git -C "$repo" status --porcelain)" ]; then
    echo "ERROR: $name has uncommitted changes:" >&2
    git -C "$repo" status --short >&2
    exit 1
  fi

  branch=$(git -C "$repo" rev-parse --abbrev-ref HEAD)
  if [ "$branch" != "develop" ]; then
    echo "ERROR: $name is on branch '$branch'; releases must be tagged from 'develop'." >&2
    exit 1
  fi

  git -C "$repo" rev-parse --verify --quiet 'refs/remotes/origin/develop' >/dev/null \
    || { echo "ERROR: $name has no origin/develop remote branch." >&2; exit 1; }
  ahead=$(git -C "$repo" rev-list --count "origin/develop..develop")
  behind=$(git -C "$repo" rev-list --count "develop..origin/develop")
  if [ "$ahead" != "0" ] || [ "$behind" != "0" ]; then
    echo "ERROR: $name develop diverged from origin/develop (ahead: $ahead, behind: $behind)." >&2
    echo "       Push/pull before preparing a release." >&2
    exit 1
  fi
done

# Refuse to shadow an existing tag anywhere: local or remote, either repo.
for repo in "$SERVER_REPO" "$APP_REPO"; do
  name=$(basename "$repo")
  if git -C "$repo" rev-parse -q --verify "refs/tags/$TAG" >/dev/null; then
    echo "ERROR: tag $TAG already exists locally in $name." >&2
    exit 1
  fi
  url=$(git -C "$repo" remote get-url origin)
  if git ls-remote --tags "$url" "refs/tags/$TAG" | grep -q .; then
    echo "ERROR: tag $TAG already exists on the remote of $name ($url)." >&2
    exit 1
  fi
done

SERVER_SHA=$(git -C "$SERVER_REPO" rev-parse HEAD)
APP_SHA=$(git -C "$APP_REPO" rev-parse HEAD)
SERVER_SUBJECT=$(git -C "$SERVER_REPO" log -1 --format=%s)
APP_SUBJECT=$(git -C "$APP_REPO" log -1 --format=%s)

echo "Release plan for $TAG"
echo ""
echo "  dydan-server ($(basename "$SERVER_REPO"))"
echo "    commit : $SERVER_SHA"
echo "    subject: $SERVER_SUBJECT"
echo ""
echo "  dydan-app"
echo "    commit : $APP_SHA"
echo "    subject: $APP_SUBJECT"
echo ""
echo "  Release image $TAG will embed server $SERVER_SHA + frontend $APP_SHA."
echo ""

read -r -p "Create LOCAL annotated tag $TAG in both repositories? [y/N] " answer
case "$answer" in
  y|Y) ;;
  *) echo "Aborted; nothing was changed."; exit 1 ;;
esac

git -C "$SERVER_REPO" tag -a "$TAG" -m "Dydan release $TAG (server $SERVER_SHA, frontend $APP_SHA)"
git -C "$APP_REPO" tag -a "$TAG" -m "Dydan release $TAG (server $SERVER_SHA, frontend $APP_SHA)"

echo ""
echo "Local tags created. Pushing them will trigger the official release build:"
echo ""
echo "  git -C \"$SERVER_REPO\" push origin \"$TAG\""
echo "  git -C \"$APP_REPO\" push origin \"$TAG\""
echo ""
echo "(The server tag push triggers go-release.yml, which resolves the matching"
echo " Dydan AppWeb tag and builds the paired reproducible image.)"
