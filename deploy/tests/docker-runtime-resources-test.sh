#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

fail() {
  printf 'docker runtime resources test failed: %s\n' "$1" >&2
  exit 1
}

assert_line() {
  file=$1
  line=$2
  grep -Fqx "$line" "$file" || fail "$file is missing: $line"
}

assert_count() {
  file=$1
  line=$2
  expected=$3
  actual=$(grep -Fxc "$line" "$file" || true)
  [ "$actual" -eq "$expected" ] || fail "$file has $actual occurrences of '$line', expected $expected"
}

test -s backend/resources/model-pricing/model_prices_and_context_window.json || \
  fail 'fallback pricing data is missing or empty'

assert_line Dockerfile.goreleaser 'COPY --chown=sub2api:sub2api backend/resources /app/resources'
assert_line deploy/Dockerfile 'COPY --from=backend-builder --chown=sub2api:sub2api /app/backend/resources /app/resources'
assert_count .goreleaser.yaml '      - backend/resources' 4
assert_count .goreleaser.simple.yaml '      - backend/resources' 1

resource_path=/app/resources/model-pricing/model_prices_and_context_window.json
image=${SUB2API_RUNTIME_RESOURCE_IMAGE:-}
built_image=
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/sub2api-runtime-resources.XXXXXX")
cleanup() {
  if [ -n "$built_image" ]; then
    docker image rm -f "$built_image" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT HUP INT TERM

if [ -z "$image" ]; then
  image="sub2api-runtime-resource-test:$$"
  built_image=$image
  mkdir -p "$tmp_dir/backend" "$tmp_dir/deploy"
  cp Dockerfile.goreleaser "$tmp_dir/Dockerfile.goreleaser"
  cp deploy/docker-entrypoint.sh "$tmp_dir/deploy/docker-entrypoint.sh"
  cp -R backend/resources "$tmp_dir/backend/resources"
  printf '#!/bin/sh\nexit 0\n' >"$tmp_dir/sub2api"
  chmod +x "$tmp_dir/sub2api"
  docker build --quiet -f "$tmp_dir/Dockerfile.goreleaser" -t "$image" "$tmp_dir" >/dev/null
fi

docker run --rm --entrypoint /bin/sh --user 1000:1000 "$image" -ec \
  "test -s '$resource_path' && test -r '$resource_path'"

docker run --rm --entrypoint /bin/cat --user 1000:1000 "$image" "$resource_path" >"$tmp_dir/model-pricing.json"
python3 - "$tmp_dir/model-pricing.json" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as handle:
    payload = json.load(handle)
if not isinstance(payload, dict) or not payload:
    raise SystemExit("runtime pricing resource must be a non-empty JSON object")
PY

printf 'docker runtime resources test passed for %s\n' "$image"
