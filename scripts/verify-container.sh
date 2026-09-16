#!/usr/bin/env bash
set -euo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"

run_id="${GITHUB_RUN_ID:-local}-$$"
image="dydan-ci:${run_id}"
container="dydan-zero-config-${run_id}"
data_dir=$(mktemp -d "${TMPDIR:-/tmp}/dydan-container-${run_id}.XXXXXX")
compose_output=$(mktemp "${TMPDIR:-/tmp}/dydan-compose-${run_id}.XXXXXX")
signup_response="${TMPDIR:-/tmp}/dydan-signup-response-${run_id}.json"
login_response="${TMPDIR:-/tmp}/dydan-login-response-${run_id}.json"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  docker image rm "$image" >/dev/null 2>&1 || true
  rm -rf -- "$data_dir"
  rm -f -- "$compose_output" "$signup_response" "$login_response"
}
trap cleanup EXIT

assert_compose_config() {
  docker compose config >"$compose_output"
  grep -Eq '^  dydan:$' "$compose_output"
  grep -Eq 'container_name: dydan' "$compose_output"
  grep -Eq 'source: dydan-data' "$compose_output"
  grep -Fq 'name: dydan-data' "$compose_output"
  grep -Fq 'name: ${DYDAN_DOCKER_VOLUME:-dydan-data}' docker-compose.yaml
}

assert_image_runtime() {
  docker run --rm --entrypoint sh "$image" -c \
    'test -x /dydan'
}

# The runtime process must never run as root: the entrypoint prepares /data as
# root, drops privileges via su-exec, and execs Dydan (PID 1) as "dydan".
assert_non_root_runtime() {
  local uid gid
  uid=$(docker exec "$container" sh -c 'stat -c %u /proc/1')
  gid=$(docker exec "$container" stat -c %g /proc/1)
  if [[ "$uid" == "0" || "$gid" == "0" ]]; then
    echo "Dydan runs as root (uid=$uid gid=$gid); expected unprivileged dydan user" >&2
    exit 1
  fi
}

# A volume previously written by the root-running image must be migrated to
# the unprivileged user automatically on boot.
assert_root_owned_volume_migration() {
  local root_data_dir root_container
  root_data_dir=$(mktemp -d "${TMPDIR:-/tmp}/dydan-root-vol-${run_id}.XXXXXX")
  root_container="${container}-rootvol"
  # Simulate a legacy root-owned volume: db + secret + uploads owned by root.
  printf 'root-owned-jwt-secret-0123456789abcdef' >"$root_data_dir/jwt-secret"
  chmod 600 "$root_data_dir/jwt-secret"
  mkdir -p "$root_data_dir/uploads"
  printf 'x' >"$root_data_dir/uploads/root-file.txt"
  sudo chown -R root:root "$root_data_dir"

  docker run -d \
    --name "$root_container" \
    --publish 127.0.0.1::2021 \
    --volume "$root_data_dir:/data" \
    "$image" >/dev/null

  local port
  for _ in $(seq 1 60); do
    if port=$(docker port "$root_container" 2021/tcp 2>/dev/null | sed -E 's/.*:([0-9]+)$/\1/' | head -n 1) &&
      [[ -n "$port" ]] &&
      curl --fail --silent --show-error "http://127.0.0.1:${port}/api/v1/health" >/dev/null; then
      break
    fi
    sleep 1
  done
  curl --fail --silent "http://127.0.0.1:${port}/api/v1/health" >/dev/null || {
    docker logs "$root_container" >&2
    echo "container failed on root-owned volume" >&2
    exit 1
  }

  # All pre-existing files must now belong to the unprivileged runtime user.
  test "$(sudo stat -c %u "$root_data_dir/jwt-secret")" = "1000"
  test "$(sudo stat -c %u "$root_data_dir/uploads/root-file.txt")" = "1000"
  test "$(docker exec "$root_container" stat -c %u /proc/1)" = "1000"

  docker rm -f "$root_container" >/dev/null
  sudo rm -rf -- "$root_data_dir"
}

assert_compose_config
docker build --tag "$image" \
  --build-context "frontend=${DYDAN_FRONTEND_CONTEXT:-../dydan-app}" .
assert_image_runtime
assert_root_owned_volume_migration

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
assert_non_root_runtime
docker exec "$container" test ! -e /config/selfhosted.yaml
docker exec "$container" test -x /dydan

test -s "$data_dir/dydan.db"
test -s "$data_dir/jwt-secret"
test "$(docker exec "$container" stat -c '%a' /data/jwt-secret)" = "600"
secret_before=$(docker exec "$container" sha256sum /data/jwt-secret | cut -d' ' -f1)

test "$(curl --silent --output /dev/null --write-out '%{http_code}' "http://127.0.0.1:${port}/")" = "200"
test "$(curl --silent --output "$signup_response" --write-out '%{http_code}' \
  --header 'Content-Type: application/json' \
  --data '{"username":"ci-family-user","password":"DydanCIPass123!","email":"ci-family@example.test","displayName":"CI Family User"}' \
  "http://127.0.0.1:${port}/api/v1/auth/")" = "201"

docker rm -f "$container" >/dev/null
start_container
port=$(container_port)
wait_for_health "$port"
assert_clean_migrations

secret_after=$(docker exec "$container" sha256sum /data/jwt-secret | cut -d' ' -f1)
test "$secret_before" = "$secret_after"
test -s "$data_dir/dydan.db"
test "$(curl --silent --output "$login_response" --write-out '%{http_code}' \
  --header 'Content-Type: application/json' \
  --data '{"username":"ci-family-user","password":"DydanCIPass123!"}' \
  "http://127.0.0.1:${port}/api/v1/auth/login")" = "200"
