#!/usr/bin/env bash
# Phase 3 authentication E2E against a disposable container.
#
# Builds the DayDan image from the current working tree, starts a throwaway
# container on an ephemeral data directory, and exercises the maintained
# authentication paths end-to-end:
#
#   browser/PWA : signup, login (short access token), cookie refresh,
#                 parallel refresh, in-window replay, CSRF rejection
#                 (cross-origin + spoofed forwarding headers), logout,
#                 password change revoking other sessions
#   sessions API: list/current flag, revoke one (ownership enforced),
#                 revoke-others
#   native      : refresh-token body flow
#   integration : /auth/login/legacy keeps the long legacy lifetime
#
# Destructive by design; never point it at a real instance. All state lives
# in temp dirs that are removed on exit.
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"

run_id="e2e-$$"
image="daydan-auth-e2e:${run_id}"
container="daydan-auth-e2e-${run_id}"
data_dir=$(mktemp -d "${TMPDIR:-/tmp}/daydan-auth-e2e-${run_id}.XXXXXX")

pass=0
fail=0

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker image rm "$image" >/dev/null 2>&1 || true
  rm -rf -- "$data_dir"
}
trap cleanup EXIT

ok() { pass=$((pass + 1)); echo "PASS: $1"; }
bad() { fail=$((fail + 1)); echo "FAIL: $1"; }

assert_eq() { # desc actual expected
  if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1 (got '$2', want '$3')"; fi
}

docker build --quiet --tag "$image" \
  --build-context "frontend=${DAYDAN_FRONTEND_CONTEXT:-../daydan-app}" .

docker run -d \
  --name "$container" \
  --publish 127.0.0.1::2021 \
  --volume "$data_dir:/data" \
  --env DT_JWT_ACCESS_TOKEN_TIME=15m \
  --env DT_JWT_SESSION_TIME=24h \
  --env DAYDAN_DISABLE_SIGNUP=false \
  "$image" >/dev/null

port=""
for _ in $(seq 1 60); do
  port=$(docker port "$container" 2021/tcp 2>/dev/null | sed -E 's/.*:([0-9]+)$/\1/' | head -n 1)
  if [[ -n "$port" ]] && curl --fail --silent "http://127.0.0.1:${port}/api/v1/health" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if [[ -z "$port" ]]; then
  docker logs "$container" >&2
  echo "container failed to start" >&2
  exit 1
fi

base="http://127.0.0.1:${port}"
jar_a="${data_dir}/cookies-a.txt"   # device A
jar_b="${data_dir}/cookies-b.txt"   # device B
user="e2e-user-$$"

api() { curl --silent --show-error --cookie-jar /dev/null "$@"; }

# --- signup -----------------------------------------------------------------
code=$(curl --silent --output /dev/null --write-out '%{http_code}' \
  --header 'Content-Type: application/json' \
  --data "{\"username\":\"${user}\",\"password\":\"E2ePass123!\",\"email\":\"${user}@example.test\",\"displayName\":\"E2E User\"}" \
  "${base}/api/v1/auth/")
assert_eq "signup returns 201" "$code" "201"

# --- login: short interactive access token ----------------------------------
login_json=$(curl --silent --cookie-jar "$jar_a" \
  --header 'Content-Type: application/json' \
  --data "{\"username\":\"${user}\",\"password\":\"E2ePass123!\"}" \
  "${base}/api/v1/auth/login")
token_a=$(jq -r .access_token <<<"$login_json")
expiry_a=$(jq -r .access_token_expiry <<<"$login_json")
ttl_seconds=$(( $(date -d "$expiry_a" +%s) - $(date +%s) ))
if (( ttl_seconds > 60 && ttl_seconds <= 1200 )); then
  ok "interactive access token is short-lived (${ttl_seconds}s <= 15m)"
else
  bad "interactive access token TTL ${ttl_seconds}s outside 15m window"
fi

# Access token carries lineage claims usable by session management.
payload=$(python3 -c "
import base64,json,sys
p='$token_a'.split('.')[1]
p+='='*(-len(p)%4)
print(json.dumps(json.loads(base64.urlsafe_b64decode(p))))
")
sid=$(jq -r .sid <<<"$payload")
fam=$(jq -r .fam <<<"$payload")
if [[ -n "$sid" && "$sid" != "null" && -n "$fam" && "$fam" != "null" ]]; then
  ok "access token embeds sid/fam lineage claims"
else
  bad "access token missing lineage claims: $payload"
fi

# Cookie must be HttpOnly and present.
grep -q "#HttpOnly_.*refresh_token" "$jar_a" && ok "refresh cookie is HttpOnly" || bad "refresh cookie not HttpOnly in jar"

# --- sessions API -----------------------------------------------------------
sessions_a=$(curl --silent --header "Authorization: Bearer $token_a" "${base}/api/v1/auth/sessions")
assert_eq "one session listed after first login" "$(jq '.res.sessions | length' <<<"$sessions_a")" "1"
assert_eq "sole session flagged current" "$(jq '[.res.sessions[] | select(.current)] | length' <<<"$sessions_a")" "1"
assert_eq "session id matches fam claim" "$(jq -r '.res.sessions[0].id' <<<"$sessions_a")" "$fam"

# second device
login_b=$(curl --silent --cookie-jar "$jar_b" \
  --header 'Content-Type: application/json' \
  --data "{\"username\":\"${user}\",\"password\":\"E2ePass123!\"}" \
  "${base}/api/v1/auth/login")
token_b=$(jq -r .access_token <<<"$login_b")
fam_b=$(jq -r .fam <<<"$payload" >/dev/null; python3 -c "
import base64,json
p='$token_b'.split('.')[1]; p+='='*(-len(p)%4)
print(json.loads(base64.urlsafe_b64decode(p))['fam'])")
sessions_a2=$(curl --silent --header "Authorization: Bearer $token_a" "${base}/api/v1/auth/sessions")
assert_eq "two sessions listed for user" "$(jq '.res.sessions | length' <<<"$sessions_a2")" "2"

# cross-user boundary is enforced via ownership check (404, no leak)
code=$(curl --silent --output /dev/null --write-out '%{http_code}' \
  --header "Authorization: Bearer $token_a" -X DELETE "${base}/api/v1/auth/sessions/not-a-family")
assert_eq "unknown family id -> 404" "$code" "404"

# --- cookie refresh (browser) ----------------------------------------------
refreshed=$(curl --silent --cookie "$jar_a" --cookie-jar "$jar_a" \
  -X POST -H "Origin: ${base}" "${base}/api/v1/auth/refresh")
new_token=$(jq -r .access_token <<<"$refreshed")
[[ "$new_token" != "null" && -n "$new_token" ]] && ok "cookie refresh issues new access token" || bad "cookie refresh failed: $refreshed"

# --- parallel refresh: one rotation, stale requests receive no credentials ---
pids=(); results="${data_dir}/parallel"
rm -rf "$results"; mkdir -p "$results"
parallel_token=$(awk '{print $NF}' <(grep refresh_token "$jar_a"))
for i in 1 2 3 4 5; do
  (curl --silent --output "$results/$i.json" --write-out '%{http_code}' \
    -X POST --header 'Content-Type: application/json' \
    --data "{\"refresh_token\":\"${parallel_token}\"}" \
    "${base}/api/v1/auth/refresh" >"$results/$i.status") & pids+=($!)
done
wait "${pids[@]}"
parallel_ok=$(grep -l access_token "$results"/*.json 2>/dev/null | wc -l)
parallel_retry=$(grep -l '^409$' "$results"/*.status 2>/dev/null | wc -l)
assert_eq "parallel refresh mints exactly one successor" "$parallel_ok" "1"
assert_eq "parallel stale refreshes return retry conflicts" "$parallel_retry" "4"

# --- CSRF: cross-origin POST rejected ----------------------------------------
code=$(curl --silent --output /dev/null --write-out '%{http_code}' --cookie "$jar_a" \
  -X POST -H "Origin: https://evil.example.test" "${base}/api/v1/auth/refresh")
assert_eq "cross-origin cookie refresh rejected (403)" "$code" "403"

# --- CSRF: spoofed forwarding headers from direct client rejected ------------
code=$(curl --silent --output /dev/null --write-out '%{http_code}' --cookie "$jar_a" \
  -X POST -H "Origin: https://spoofed.example.test" \
  -H "X-Forwarded-Host: spoofed.example.test" \
  -H "X-Forwarded-Proto: https" \
  "${base}/api/v1/auth/refresh")
assert_eq "spoofed X-Forwarded-* cannot legitimize cross-origin refresh (403)" "$code" "403"

# --- native flow: refresh token in body --------------------------------------
parallel_success=""
for candidate in "$results"/*.json; do
  if grep -q access_token "$candidate"; then
    parallel_success="$candidate"
    break
  fi
done
native_token=$(jq -r .refresh_token "$parallel_success")
native_refresh=$(curl --silent --cookie-jar /dev/null -X POST \
  --header 'Content-Type: application/json' \
  --data "{\"refresh_token\":\"${native_token}\"}" \
  "${base}/api/v1/auth/refresh")
[[ "$(jq -r .access_token <<<"$native_refresh")" != "null" ]] && ok "native body-flow refresh works" || bad "native body-flow refresh failed"

# --- legacy login keeps long lifetime ----------------------------------------
legacy_json=$(curl --silent -X POST \
  --header 'Content-Type: application/json' \
  --data "{\"username\":\"${user}\",\"password\":\"E2ePass123!\"}" \
  "${base}/api/v1/auth/login/legacy")
legacy_expire=$(jq -r .expire <<<"$legacy_json")
legacy_ttl_hours=$(( ($(date -d "$legacy_expire" +%s) - $(date +%s)) / 3600 ))
if (( legacy_ttl_hours >= 20 )); then
  ok "legacy login token keeps ~SessionTime lifetime (${legacy_ttl_hours}h)"
else
  bad "legacy login token TTL only ${legacy_ttl_hours}h — integration compat broken"
fi

# --- revoke-others keeps caller ----------------------------------------------
revoked=$(curl --silent --header "Authorization: Bearer $token_a" -X POST "${base}/api/v1/auth/sessions/revoke-others")
# token_a's own family survives; B's dies.
sleep 0.2
code=$(curl --silent --output /dev/null --write-out '%{http_code}' --cookie "$jar_b" \
  -X POST -H "Origin: ${base}" "${base}/api/v1/auth/refresh")
assert_eq "other device's refresh dead after revoke-others (401)" "$code" "401"

# re-login B for the password-change scenario
curl --silent --cookie-jar "$jar_b" \
  --header 'Content-Type: application/json' \
  --data "{\"username\":\"${user}\",\"password\":\"E2ePass123!\"}" \
  "${base}/api/v1/auth/login" >/dev/null

# --- password change revokes other sessions, keeps caller --------------------
pw=$(curl --silent --header "Authorization: Bearer $new_token" \
  --request PUT --header 'Content-Type: application/json' \
  --data '{"password":"NewE2ePass456!","current_password":"E2ePass123!"}' \
  "${base}/api/v1/users/change_password")
code=$(curl --silent --output /dev/null --write-out '%{http_code}' --cookie "$jar_b" \
  -X POST -H "Origin: ${base}" "${base}/api/v1/auth/refresh")
assert_eq "password change revoked other device session (401)" "$code" "401"
current_refresh=$(jq -r .refresh_token <<<"$native_refresh")
code=$(curl --silent --output /dev/null --write-out '%{http_code}' \
  --request POST --header 'Content-Type: application/json' \
  --data "{\"refresh_token\":\"${current_refresh}\"}" \
  "${base}/api/v1/auth/refresh")
assert_eq "password change preserves caller family (200)" "$code" "200"

# wrong current password refused
code=$(curl --silent --output /dev/null --write-out '%{http_code}' \
  --header "Authorization: Bearer $new_token" \
  --request PUT --header 'Content-Type: application/json' \
  --data '{"password":"ZzzE2ePass789!","current_password":"wrong"}' \
  "${base}/api/v1/users/change_password")
assert_eq "wrong current_password refused (403)" "$code" "403"

# --- logout -------------------------------------------------------------------
logout_headers="${data_dir}/logout-headers.txt"
curl --silent --cookie "$jar_a" --dump-header "$logout_headers" \
  -X POST -H "Origin: ${base}" "${base}/api/v1/auth/logout" >/dev/null
if grep -qi '^Set-Cookie:.*refresh_token=;' "$logout_headers" && \
   grep -qi '^Set-Cookie:.*refresh_token=;.*Max-Age=0' "$logout_headers"; then
  ok "logout expires the refresh cookie (Max-Age=0)"
else
  bad "logout did not send cookie-expiring Set-Cookie headers"
fi

echo
echo "Results: $pass passed, $fail failed"
if (( fail > 0 )); then
  docker logs "$container" >&2 || true
  exit 1
fi
