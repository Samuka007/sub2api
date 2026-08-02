#!/bin/bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

if docker compose version >/dev/null 2>&1; then
  compose=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  compose=(docker-compose)
else
  echo "docker compose is required to validate the Langfuse deployment" >&2
  exit 1
fi

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
mkdir -p "$tmp_dir/work"
git ls-files -z deploy/langfuse | tar --null -T - -cf - | tar -xf - -C "$tmp_dir/work"
stack="$tmp_dir/work/deploy/langfuse"

invalid_urls=(
  ''
  'http://langfuse.example.com'
  'https://'
  'https:///path'
  'https://user@example.com'
  'https://langfuse.example.com/path'
  'https://foo..example.com'
  'https://foo.-bar.example.com'
  'https://foo.bar-.example.com'
  'https://aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.example.com'
  'https://langfuse.example.com:0'
  'https://langfuse.example.com:65536'
)
for invalid_url in "${invalid_urls[@]}"; do
  if (cd "$stack" && LANGFUSE_PUBLIC_URL="$invalid_url" ./generate-env.sh) >"$tmp_dir/invalid-url.out" 2>"$tmp_dir/invalid-url.err"; then
    printf 'generate-env.sh accepted invalid LANGFUSE_PUBLIC_URL: %q\n' "$invalid_url" >&2
    exit 1
  fi
  test ! -e "$stack/.env"
  test ! -e "$stack/.xray/bridge.json"
  test ! -e "$stack/main-server-xray/config.json"
done

(
  cd "$stack"
  LANGFUSE_PUBLIC_URL=https://langfuse.example.com ./generate-env.sh
) >"$tmp_dir/generate.out" 2>"$tmp_dir/generate.err"

test ! -s "$tmp_dir/generate.err"
grep -qx 'Rendered Xray bridge and main-server portal configs. Secrets were not printed.' "$tmp_dir/generate.out"
grep -qx 'Created .env and Xray configs for https://langfuse.example.com. Secrets were not printed.' "$tmp_dir/generate.out"
grep -qx 'NEXTAUTH_URL=https://langfuse.example.com' "$stack/.env"
grep -qx 'LANGFUSE_BIND_IP=127.0.0.1' "$stack/.env"
test "$(stat -c %a "$stack/.env")" = 600
test "$(stat -c %a "$stack/clickhouse-config.d/resource-logging.xml")" = 644
test "$(stat -c %a "$stack/clickhouse-users.d/resource-profile.xml")" = 644
test "$(stat -c %a "$stack/.xray")" = 700
test "$(stat -c %a "$stack/main-server-xray")" = 700
test "$(stat -c %a "$stack/.xray/bridge.json")" = 644
test "$(stat -c %a "$stack/main-server-xray/config.json")" = 644
test "$(stat -c %a "$stack/main-server-xray/.env")" = 600
jq -e . "$stack/.xray/bridge.json" >/dev/null
jq -e . "$stack/main-server-xray/config.json" >/dev/null

if grep -REn 'GENERATE_ME|__XRAY_[A-Z_]+__' \
  "$stack/.env" "$stack/.xray/bridge.json" "$stack/main-server-xray/config.json"; then
  echo "generated Langfuse configuration contains an unresolved placeholder" >&2
  exit 1
fi

python3 - "$stack/.env" "$stack/main-server-xray/.env" <<'PY'
import pathlib
import re
import sys

allowed = ("docker.m.daocloud.io/", "ghcr.nju.edu.cn/")
digest = re.compile(r"@sha256:[0-9a-f]{64}$")
for env_path in map(pathlib.Path, sys.argv[1:]):
    for line in env_path.read_text(encoding="utf-8").splitlines():
        if not line.startswith(("LANGFUSE_WEB_IMAGE=", "LANGFUSE_WORKER_IMAGE=", "POSTGRES_IMAGE=", "CLICKHOUSE_IMAGE=", "MINIO_IMAGE=", "REDIS_IMAGE=", "XRAY_IMAGE=")):
            continue
        image = line.split("=", 1)[1]
        if not image.startswith(allowed):
            raise SystemExit(f"{env_path}: image does not use an approved mainland mirror: {image}")
        if not digest.search(image):
            raise SystemExit(f"{env_path}: image is not pinned by digest: {image}")
PY

(
  cd "$stack"
  "${compose[@]}" --env-file .env -f docker-compose.yml config --quiet
  "${compose[@]}" --env-file main-server-xray/.env -f main-server-xray/docker-compose.yml config --quiet
)

env_hash=$(sha256sum "$stack/.env")
if (cd "$stack" && LANGFUSE_PUBLIC_URL=https://other.example.com ./generate-env.sh) >/dev/null 2>&1; then
  echo "generate-env.sh must not overwrite an existing .env" >&2
  exit 1
fi
test "$(sha256sum "$stack/.env")" = "$env_hash"

chmod 600 \
  "$stack/clickhouse-config.d/resource-logging.xml" \
  "$stack/clickhouse-users.d/resource-profile.xml"
sed -i \
  -e 's#^XRAY_IMAGE=.*#XRAY_IMAGE=ghcr.io/xtls/xray-core:26.5.9#' \
  -e 's/^XRAY_SERVER_PORT=.*/XRAY_SERVER_PORT=31590/' \
  -e 's#^XRAY_REALITY_TARGET=.*#XRAY_REALITY_TARGET=api.sub2api.com:443#' \
  -e 's/^XRAY_SERVER_ADDRESS=.*/XRAY_SERVER_ADDRESS=192.0.2.10/' \
  "$stack/.env"
grep -vE '^(XRAY_IMAGE|XRAY_SERVER_PORT|XRAY_REALITY_TARGET)=' "$stack/.env" >"$tmp_dir/migration-unchanged.before"
(cd "$stack" && ./generate-xray-config.sh) >/dev/null
test "$(stat -c %a "$stack/clickhouse-config.d/resource-logging.xml")" = 644
test "$(stat -c %a "$stack/clickhouse-users.d/resource-profile.xml")" = 644
grep -qx 'XRAY_SERVER_PORT=443' "$stack/.env"
grep -qx 'XRAY_REALITY_TARGET=host.docker.internal:8443' "$stack/.env"
grep -qx 'XRAY_IMAGE=ghcr.nju.edu.cn/xtls/xray-core:26.5.9@sha256:933c868cbbb1ed632198c3ffeb99454709fa13dbb8c2a6f328d6a9a19e75269c' "$stack/.env"
grep -qx 'XRAY_SERVER_ADDRESS=192.0.2.10' "$stack/.env"
grep -vE '^(XRAY_IMAGE|XRAY_SERVER_PORT|XRAY_REALITY_TARGET)=' "$stack/.env" >"$tmp_dir/migration-unchanged.after"
cmp "$tmp_dir/migration-unchanged.before" "$tmp_dir/migration-unchanged.after"

sed -i \
  -e 's/^XRAY_SERVER_PORT=.*/XRAY_SERVER_PORT=24443/' \
  -e 's#^XRAY_REALITY_TARGET=.*#XRAY_REALITY_TARGET=caddy.internal:9443#' \
  "$stack/.env"
cp "$stack/.env" "$tmp_dir/custom.env.before"
(cd "$stack" && ./generate-xray-config.sh) >/dev/null
grep -qx 'XRAY_SERVER_PORT=24443' "$stack/.env"
grep -qx 'XRAY_REALITY_TARGET=caddy.internal:9443' "$stack/.env"
cmp "$tmp_dir/custom.env.before" "$stack/.env"

exec 8>"$stack/.xray-generation.lock"
flock 8
if (cd "$stack" && ./generate-xray-config.sh) >"$tmp_dir/locked.out" 2>"$tmp_dir/locked.err"; then
  echo "generate-xray-config.sh must reject concurrent generation" >&2
  exit 1
fi
grep -q 'already running' "$tmp_dir/locked.err"
flock -u 8
exec 8>&-

cp "$stack/.env" "$tmp_dir/valid.env"
sed -i 's/^XRAY_SERVER_PORT=.*/XRAY_SERVER_PORT=0/' "$stack/.env"
if (cd "$stack" && ./generate-xray-config.sh) >/dev/null 2>&1; then
  echo "generate-xray-config.sh must reject port zero" >&2
  exit 1
fi
cp "$tmp_dir/valid.env" "$stack/.env"

mkdir -p "$tmp_dir/bin"
cat >"$tmp_dir/bin/docker" <<'SH'
#!/bin/sh
printf '%s\n' "$*" >>"$DOCKER_CALLS"
SH
chmod +x "$tmp_dir/bin/docker"
export DOCKER_CALLS="$tmp_dir/docker.calls"
: >"$DOCKER_CALLS"
(cd "$stack" && PATH="$tmp_dir/bin:$PATH" ./manage.sh start) >/dev/null
grep -qx 'compose --env-file .env -f docker-compose.yml up -d --pull never' "$DOCKER_CALLS"
if grep -q 'pull' "$DOCKER_CALLS"; then
  grep -q -- '--pull never' "$DOCKER_CALLS" || {
    echo "manage.sh start must forbid implicit image pulls" >&2
    exit 1
  }
fi

: >"$DOCKER_CALLS"
(cd "$stack" && PATH="$tmp_dir/bin:$PATH" ./manage.sh restart) >/dev/null
grep -qx 'compose --env-file .env -f docker-compose.yml up -d --pull never --force-recreate' "$DOCKER_CALLS"

: >"$DOCKER_CALLS"
(cd "$stack" && PATH="$tmp_dir/bin:$PATH" ./xray-tunnel.sh start) >/dev/null
grep -qx 'compose --env-file .env -f docker-compose.yml up -d --pull never --force-recreate xray-bridge' "$DOCKER_CALLS"
if grep -q 'compose .* pull$' "$DOCKER_CALLS"; then
  echo "Xray start must not pull or upgrade images" >&2
  exit 1
fi

: >"$DOCKER_CALLS"
(cd "$stack" && PATH="$tmp_dir/bin:$PATH" ./manage.sh pull) >/dev/null
grep -qx 'compose --env-file .env -f docker-compose.yml pull' "$DOCKER_CALLS"

echo "Langfuse deployment generation and lifecycle contracts passed"
