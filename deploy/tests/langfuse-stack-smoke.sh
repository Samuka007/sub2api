#!/bin/bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
alpine_image=docker.1ms.run/library/alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d
project=codex-langfuse-stack-$$
tmp_dir=$(mktemp -d)

cleanup() {
  if [ -f "$tmp_dir/work/deploy/langfuse/.env" ]; then
    (
      cd "$tmp_dir/work/deploy/langfuse"
      docker compose -p "$project" --env-file .env -f docker-compose.yml \
        down -v --remove-orphans
    ) >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

mkdir -p "$tmp_dir/work"
cd "$repo_root"
git ls-files -z deploy/langfuse | tar --null -T - -cf - | tar -xf - -C "$tmp_dir/work"
stack="$tmp_dir/work/deploy/langfuse"
cd "$stack"
LANGFUSE_PUBLIC_URL=https://langfuse.example.com ./generate-env.sh >/dev/null
cat > smoke.override.yml <<'YAML'
services:
  langfuse-web:
    ports: !reset []
YAML

compose=(docker compose -p "$project" --env-file .env -f docker-compose.yml -f smoke.override.yml)
"${compose[@]}" pull langfuse-web langfuse-ingest langfuse-worker clickhouse-read-proxy postgres clickhouse minio redis
if ! "${compose[@]}" up -d --pull never langfuse-web langfuse-ingest langfuse-worker; then
  "${compose[@]}" ps >&2 || true
  web_id=$("${compose[@]}" ps -q langfuse-web)
  if [ -n "$web_id" ]; then
    docker inspect --format '{{json .State.Health}}' "$web_id" >&2 || true
  fi
  "${compose[@]}" logs --tail 200 >&2 || true
  exit 1
fi

network="${project}_default"
worker_stable() {
  worker_id=$("${compose[@]}" ps -q langfuse-worker)
  [ -n "$worker_id" ] || return 1
  [ "$(docker inspect --format '{{.State.Running}} {{.State.Restarting}} {{.RestartCount}}' "$worker_id")" = 'true false 0' ]
}

for _ in $(seq 1 60); do
  if web_response=$(docker run --rm --network "$network" "$alpine_image" \
    wget -qO- -T 3 http://langfuse-web:3000/api/public/health 2>/dev/null) && \
    ingest_response=$(docker run --rm --network "$network" "$alpine_image" \
      wget -qO- -T 3 http://langfuse-ingest:3000/api/public/health 2>/dev/null); then
    if printf '%s' "$web_response" | grep -q '"status":"OK"' && \
      printf '%s' "$ingest_response" | grep -q '"status":"OK"' && worker_stable; then
      sleep 10
      worker_stable || continue
      "${compose[@]}" ps
      echo "Langfuse full stack health smoke passed"
      exit 0
    fi
  fi
  sleep 5
done

"${compose[@]}" ps >&2 || true
"${compose[@]}" logs --tail 200 >&2 || true
exit 1
