#!/usr/bin/env bash
set -euo pipefail

COMPOSE_BIN="${COMPOSE_BIN:-/data/data/com.termux/files/home/doki-test/bin/docker-compose}"
DOKI_SOCKET="${DOKI_SOCKET:-/data/data/com.termux/files/usr/var/run/doki.sock}"
PROJECT="${PROJECT:-volconformance}"
WORKDIR="${TMPDIR:-/tmp}/doki-${PROJECT}-$$"
COMPOSE_FILE="$WORKDIR/compose.yml"
VOLUME_NAME="${PROJECT}_probe_data"

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

mkdir -p "$WORKDIR"
cat > "$COMPOSE_FILE" <<'YAML'
services:
  writer:
    image: alpine:latest
    # Phase 1 isolates named-volume semantics. Doki's Compose-network
    # conformance is validated separately, so do not let an unrelated network
    # lifecycle failure mask volume regressions here.
    network_mode: host
    command:
      - sh
      - -c
      - |
        if [ ! -f /data/probe.txt ]; then
          printf 'persisted\n' > /data/probe.txt
        fi
        exec sleep 300
    volumes:
      - probe_data:/data
volumes:
  probe_data:
YAML

compose() {
  "$COMPOSE_BIN" -p "$PROJECT" -f "$COMPOSE_FILE" "$@"
}

wait_for_payload() {
  local out=""
  for _ in $(seq 1 30); do
    if out="$(compose exec -T writer cat /data/probe.txt 2>/dev/null)" && [[ "$out" == "persisted" ]]; then
      return 0
    fi
    sleep 1
  done
  echo "last payload output: ${out:-<empty>}" >&2
  compose ps -a >&2 || true
  compose logs --no-color >&2 || true
  return 1
}

volume_exists_via_api() {
  curl --fail --silent --show-error --unix-socket "$DOKI_SOCKET" \
    "http://localhost/volumes/${VOLUME_NAME}" >/dev/null
}

echo '== docker compose config =='
compose config >/dev/null

echo '== first up =='
compose up -d
wait_for_payload || fail "named volume not visible through docker compose exec after first up"
volume_exists_via_api || fail "named volume ${VOLUME_NAME} missing from Docker API after first up"

echo '== down preserves named volume =='
compose down
volume_exists_via_api || fail "named volume ${VOLUME_NAME} was removed by docker compose down"

echo '== second up reuses data =='
compose up -d
wait_for_payload || fail "persisted payload missing after docker compose down/up"

# Replace the value from inside the container and prove the same backing data is used.
printf '%s\n' 'changed-after-recreate' | compose exec -T writer sh -c 'cat > /data/probe.txt'
compose down
compose up -d
for _ in $(seq 1 30); do
  value="$(compose exec -T writer cat /data/probe.txt 2>/dev/null || true)"
  [[ "$value" == "changed-after-recreate" ]] && break
  sleep 1
done
[[ "${value:-}" == "changed-after-recreate" ]] || fail "payload did not persist across second recreate: ${value:-<empty>}"

echo '== down -v removes named volume =='
compose down -v
if volume_exists_via_api; then
  fail "named volume ${VOLUME_NAME} still exists after docker compose down -v"
fi

echo "PASS: named volume lifecycle ${VOLUME_NAME}"
trap - EXIT
rm -rf "$WORKDIR"
