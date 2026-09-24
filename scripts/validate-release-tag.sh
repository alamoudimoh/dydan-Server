#!/usr/bin/env bash
# Accept the established Dydan calendar release format and retained legacy
# semantic tags. The release workflow uses this single validator so the server
# can be tagged with the same immutable release tag as AppWeb.
set -euo pipefail

tag="${1:-}"

if [[ "$tag" =~ ^v[0-9]{6}-[0-9]+$ ]] || [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]]; then
  exit 0
fi

echo "Release tag must be vYYYYMM-NN or a legacy v-prefixed semantic version, got: $tag" >&2
exit 1
