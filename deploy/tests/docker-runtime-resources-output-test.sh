#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/sub2api-runtime-build-output.XXXXXX")
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$tmp_dir/bin"
cat >"$tmp_dir/bin/docker" <<'EOF'
#!/bin/sh
if [ "${1:-}" = build ]; then
  printf '%s\n' 'runtime-build-output-sentinel'
  exit 10
fi
printf 'unexpected docker invocation: %s\n' "$*" >&2
exit 99
EOF
chmod +x "$tmp_dir/bin/docker"

set +e
output=$(PATH="$tmp_dir/bin:$PATH" /bin/sh "$repo_root/deploy/tests/docker-runtime-resources-test.sh" 2>&1)
status=$?
set -e

[ "$status" -ne 0 ] || {
  printf '%s\n' 'expected the fake docker build to fail' >&2
  exit 1
}
printf '%s\n' "$output" | grep -Fq 'runtime-build-output-sentinel' || {
  printf '%s\n' 'docker runtime resource test hid docker build output' >&2
  exit 1
}

printf '%s\n' 'docker runtime resource build output remains visible on failure'
