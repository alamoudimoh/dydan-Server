#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"

run_id="${GITHUB_RUN_ID:-local}-$$"
image="taskier-ci:${run_id}"
container="taskier-zero-config-${run_id}"
data_dir=$(mktemp -d "${TMPDIR:-/tmp}/taskier-container-${run_id}.XXXXXX")
compose_output=$(mktemp "${TMPDIR:-/tmp}/taskier-compose-${run_id}.XXXXXX")
signup_response="${TMPDIR:-/tmp}/taskier-signup-response-${run_id}.json"
login_response="${TMPDIR:-/tmp}/taskier-login-response-${run_id}.json"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker image rm "$image" >/dev/null 2>&1 || true
  rm -rf -- "$data_dir"
  rm -f -- "$compose_output" "$signup_response" "$login_response"
}
trap cleanup EXIT

docker compose config >"$compose_output"
grep -Eq 'target: /data' "$compose_output"
grep -Eq 'source: taskier-data' "$compose_output"

docker build --tag "$image" .

start_container() {
  docker run -d \
    --name "$container" \
    --publish 127.0.0.1::2021 \
    --volume "$data_dir:/data" \
    "$image" >/dev/null
}

wait_for_health() {
  local port=$1
  for _ in $(seq 1 60); do
    if curl --fail --silent --show-error "http://127.0.0.1:${port}/api/v1/health" >/dev/null; then
      return 0
    fi
    sleep 1
  done
  docker logs "$container" >&2
  return 1
}

container_port() {
  docker port "$container" 2021/tcp | sed -E 's/.*:([0-9]+)$/\1/' | head -n 1
}

assert_clean_migrations() {
  local container_logs
  container_logs=$(docker logs "$container" 2>&1)
  if grep -Eiq 'failed to apply migration|automatic (schema|data|SQL) migration' <<<"$container_logs"; then
    printf '%s\n' "$container_logs" >&2
    echo "Migration failure found in container logs" >&2
    exit 1
  fi
  if ! grep -Eq 'Applied [0-9]+ migrations!' <<<"$container_logs"; then
    printf '%s\n' "$container_logs" >&2
    echo "Migration completion was not confirmed in container logs" >&2
    exit 1
  fi
}

start_container
port=$(container_port)
wait_for_health "$port"
assert_clean_migrations
docker exec "$container" test ! -e /config/selfhosted.yaml

test -s "$data_dir/taskier.db"
test -s "$data_dir/jwt-secret"
test "$(docker exec "$container" stat -c '%a' /data/jwt-secret)" = "600"
secret_before=$(docker exec "$container" sha256sum /data/jwt-secret | cut -d' ' -f1)

test "$(curl --silent --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:${port}/")" = "200"
test "$(curl --silent --output "$signup_response" --write-out '%{http_code}' \
  --header 'Content-Type: application/json' \
  --data '{"username":"ci-family-user","password":"DayDanCIPass123!","email":"ci-family@example.test","displayName":"CI Family User"}' \
  "http://127.0.0.1:${port}/api/v1/auth/")" = "201"

docker rm -f "$container" >/dev/null
start_container
port=$(container_port)
wait_for_health "$port"
assert_clean_migrations

secret_after=$(docker exec "$container" sha256sum /data/jwt-secret | cut -d' ' -f1)
test "$secret_before" = "$secret_after"
test -s "$data_dir/taskier.db"
test "$(curl --silent --output "$login_response" --write-out '%{http_code}' \
  --header 'Content-Type: application/json' \
  --data '{"username":"ci-family-user","password":"DayDanCIPass123!"}' \
  "http://127.0.0.1:${port}/api/v1/auth/login")" = "200"
