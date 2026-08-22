#!/usr/bin/env bash
set -euo pipefail

COMPOSE_BIN="${COMPOSE_BIN:-/data/data/com.termux/files/home/doki-test/bin/docker-compose}"
DOKI_SOCKET="${DOKI_SOCKET:-/data/data/com.termux/files/usr/var/run/doki.sock}"
PROJECT="${PROJECT:-pgproviderconformance}"
WORKDIR="${TMPDIR:-/tmp}/doki-${PROJECT}-$$"
COMPOSE_FILE="$WORKDIR/compose.yml"
VOLUME_NAME="${PROJECT}_postgres_data"
CONTAINER_NAME="${PROJECT}-postgres-1"
PROVIDER_ROOT="${PROVIDER_ROOT:-/data/data/com.termux/files/usr/var/lib/doki/providers/postgresql/16.15/arm64}"
PROVIDER_POSTGRES="${PROVIDER_ROOT}/bin/postgres"
PROVIDER_PSQL="${PROVIDER_ROOT}/bin/psql"

export DOCKER_HOST="unix://${DOKI_SOCKET}"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

cleanup() {
  if [[ -f "$COMPOSE_FILE" ]]; then
    "$COMPOSE_BIN" -p "$PROJECT" -f "$COMPOSE_FILE" down -v --remove-orphans >/dev/null 2>&1 || true
  fi
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

[[ -x "$COMPOSE_BIN" ]] || fail "Docker Compose binary not executable: $COMPOSE_BIN"
[[ -S "$DOKI_SOCKET" ]] || fail "Doki socket not available: $DOKI_SOCKET"
[[ -x "$PROVIDER_POSTGRES" ]] || fail "provider postgres binary missing: $PROVIDER_POSTGRES"
[[ -x "$PROVIDER_PSQL" ]] || fail "provider psql binary missing: $PROVIDER_PSQL"

mkdir -p "$WORKDIR"
cat > "$COMPOSE_FILE" <<'YAML'
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: techservice
      POSTGRES_USER: techservice
      POSTGRES_PASSWORD: techservice
    ports:
      - "5750:5432"
    volumes:
      - postgres_data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U techservice -d techservice"]
      interval: 2s
      timeout: 2s
      retries: 20
      start_period: 2s
volumes:
  postgres_data:
YAML

compose() {
  "$COMPOSE_BIN" -p "$PROJECT" -f "$COMPOSE_FILE" "$@"
}

container_id() {
  compose ps -q postgres | head -1
}

inspect_json() {
  local id
  id="$(container_id)"
  [[ -n "$id" ]] || return 1
  curl --fail --silent --show-error --unix-socket "$DOKI_SOCKET" "http://localhost/containers/${id}/json"
}

volume_exists_via_api() {
  curl --fail --silent --show-error --unix-socket "$DOKI_SOCKET" \
    "http://localhost/volumes/${VOLUME_NAME}" >/dev/null
}

port_open() {
  python3 - <<'PYPORT'
import socket
s=socket.socket()
s.settimeout(0.5)
try:
    s.connect(("127.0.0.1",5750))
except OSError:
    raise SystemExit(1)
finally:
    s.close()
PYPORT
}

wait_port_open() {
  for _ in $(seq 1 40); do
    port_open && return 0
    sleep 0.25
  done
  return 1
}

wait_port_closed() {
  for _ in $(seq 1 40); do
    if ! port_open; then
      return 0
    fi
    sleep 0.25
  done
  return 1
}

assert_runtime_process() {
  local json pid exe
  json="$(inspect_json)"
  pid="$(printf '%s' "$json" | python3 -c 'import json,sys; print(int((json.load(sys.stdin).get("State") or {}).get("Pid") or 0))')"
  [[ "$pid" -gt 0 ]] || fail "inspect did not expose a running PostgreSQL PID"
  exe="$(readlink -f "/proc/${pid}/exe")"
  echo "postgres process: pid=${pid} exe=${exe}"
  [[ "$exe" == "$PROVIDER_POSTGRES" ]] || fail "PostgreSQL process executable is ${exe}, want ${PROVIDER_POSTGRES}"
  [[ "$exe" != "/data/data/com.termux/files/usr/bin/postgres" ]] || fail "Termux PostgreSQL was used instead of provider runtime"
}

external_query_scalar() {
  local sql="$1"
  PGPASSWORD=techservice "$PROVIDER_PSQL" -h 127.0.0.1 -p 5750 -Atq -U techservice -d techservice -c "$sql" | tr -d '\r'
}

wait_healthy() {
  local json="" status=""
  for _ in $(seq 1 90); do
    json="$(inspect_json 2>/dev/null || true)"
    if [[ -n "$json" ]]; then
      status="$(printf '%s' "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(((d.get("State") or {}).get("Health") or {}).get("Status") or "")' 2>/dev/null || true)"
      [[ "$status" == "healthy" ]] && return 0
    fi
    sleep 2
  done
  echo "last health status: ${status:-<empty>}" >&2
  compose ps -a >&2 || true
  compose logs --no-color >&2 || true
  return 1
}

assert_version() {
  local version
  version="$(compose exec -T postgres postgres --version)"
  echo "postgres version: $version"
  [[ "$version" == *"PostgreSQL) 16.15"* ]] || fail "requested postgres:16-alpine did not run PostgreSQL 16.15: $version"
  [[ "$version" != *"18.2"* ]] || fail "Termux PostgreSQL 18.2 was substituted for requested 16.15"
}

assert_port_mapping() {
  local json
  json="$(inspect_json)"
  printf '%s' "$json" | python3 -c '
import json,sys
obj=json.load(sys.stdin)
ports=((obj.get("NetworkSettings") or {}).get("Ports") or {})
bindings=ports.get("5432/tcp") or []
if not any(str(b.get("HostPort")) == "5750" for b in bindings):
    raise SystemExit("5432/tcp is not published as host port 5750: %r" % (ports,))
'
}

query_scalar() {
  local sql="$1"
  compose exec -T postgres psql -Atq -U techservice -d techservice -c "$sql" | tr -d '\r'
}

echo '== docker compose config =='
compose config >/dev/null

echo '== first up: exact PostgreSQL provider =='
compose up -d
wait_healthy || fail "PostgreSQL did not become healthy"
assert_version
assert_port_mapping
assert_runtime_process
volume_exists_via_api || fail "named volume ${VOLUME_NAME} missing after first up"
wait_port_open || fail "published host port 5750 did not become reachable"

echo '== create persistent SQL payload =='
compose exec -T postgres psql -v ON_ERROR_STOP=1 -U techservice -d techservice <<'SQL'
CREATE TABLE IF NOT EXISTS doki_provider_probe (
  id integer PRIMARY KEY,
  payload text NOT NULL
);
INSERT INTO doki_provider_probe(id,payload)
VALUES (1,'persisted-through-doki')
ON CONFLICT (id) DO UPDATE SET payload=EXCLUDED.payload;
SQL
[[ "$(query_scalar "SELECT payload FROM doki_provider_probe WHERE id=1")" == "persisted-through-doki" ]] || fail "SQL payload not readable after insert"
[[ "$(external_query_scalar "SELECT payload FROM doki_provider_probe WHERE id=1")" == "persisted-through-doki" ]] || fail "external SQL over 127.0.0.1:5750 failed"

echo '== stop/start lifecycle and published port =='
compose stop postgres
wait_port_closed || fail "published port 5750 remained open after docker compose stop"
compose start postgres
wait_healthy || fail "PostgreSQL did not become healthy after docker compose start"
wait_port_open || fail "published port 5750 did not return after docker compose start"
assert_runtime_process
[[ "$(external_query_scalar "SELECT payload FROM doki_provider_probe WHERE id=1")" == "persisted-through-doki" ]] || fail "SQL payload unavailable after stop/start"

echo '== restart lifecycle =='
compose restart postgres
wait_healthy || fail "PostgreSQL did not become healthy after docker compose restart"
wait_port_open || fail "published port 5750 unavailable after docker compose restart"
assert_runtime_process
[[ "$(query_scalar "SELECT payload FROM doki_provider_probe WHERE id=1")" == "persisted-through-doki" ]] || fail "SQL payload unavailable after restart"

echo '== logs =='
LOGS="$(compose logs --no-color postgres)"
[[ -n "$LOGS" ]] || fail "docker compose logs returned no PostgreSQL output"
printf '%s\n' "$LOGS" | grep -q 'ready to accept connections' || fail "PostgreSQL readiness message missing from docker compose logs"

echo '== down preserves PostgreSQL named volume =='
compose down
volume_exists_via_api || fail "named volume ${VOLUME_NAME} was removed by docker compose down"

echo '== second up reuses cluster/data =='
compose up -d
wait_healthy || fail "PostgreSQL did not become healthy after down/up"
assert_version
[[ "$(query_scalar "SELECT payload FROM doki_provider_probe WHERE id=1")" == "persisted-through-doki" ]] || fail "PostgreSQL data did not survive docker compose down/up"

echo '== recreate container reuses same cluster =='
compose up -d --force-recreate
wait_healthy || fail "PostgreSQL did not become healthy after force-recreate"
[[ "$(query_scalar "SELECT payload FROM doki_provider_probe WHERE id=1")" == "persisted-through-doki" ]] || fail "PostgreSQL data did not survive container recreation"

echo '== down -v removes PostgreSQL volume =='
compose down -v
if volume_exists_via_api; then
  fail "named volume ${VOLUME_NAME} still exists after docker compose down -v"
fi
wait_port_closed || fail "published port 5750 remained open after docker compose down -v"

echo '== recreate after down -v initializes an empty database =='
compose up -d
wait_healthy || fail "PostgreSQL did not become healthy after down -v recreation"
assert_version
assert_runtime_process
wait_port_open || fail "published port 5750 unavailable after down -v recreation"
if [[ -n "$(query_scalar "SELECT to_regclass('public.doki_provider_probe')")" ]]; then
  fail "database was not empty after down -v recreation"
fi
compose down -v
if volume_exists_via_api; then
  fail "named volume ${VOLUME_NAME} still exists after final cleanup"
fi
wait_port_closed || fail "published port 5750 remained open after final cleanup"

echo "PASS: PostgreSQL Android provider 16.15 full lifecycle ${VOLUME_NAME}"
trap - EXIT
rm -rf "$WORKDIR"
