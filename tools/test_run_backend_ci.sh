#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/sub2api-backend-ci-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT
mkdir -p "$tmp_dir/bin"

cat >"$tmp_dir/bin/make" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
target=${*: -1}
printf '%s\n' "$target" >>"$CALLS"
sleep 1
[[ "$target" != "${FAIL_TARGET:-}" ]]
STUB
chmod +x "$tmp_dir/bin/make"

export PATH="$tmp_dir/bin:$PATH"
export CALLS="$tmp_dir/calls"
start=$(date +%s)
if ! /bin/bash "$repo_root/tools/run_backend_ci.sh"; then
  echo 'backend gate failed with successful checks' >&2
  exit 1
fi
elapsed=$(( $(date +%s) - start ))
if (( elapsed >= 4 )); then
  echo "backend checks did not run in parallel: ${elapsed}s" >&2
  exit 1
fi
if [[ $(wc -l <"$CALLS") -ne 3 ]]; then
  echo 'backend gate did not run unit, integration, and build' >&2
  exit 1
fi

: >"$CALLS"
export FAIL_TARGET=test-integration
if /bin/bash "$repo_root/tools/run_backend_ci.sh"; then
  echo 'parallel backend gate ignored a failed command' >&2
  exit 1
fi
if [[ $(wc -l <"$CALLS") -ne 3 ]]; then
  echo 'backend gate stopped before collecting every failure' >&2
  exit 1
fi

printf 'Backend CI parallel runner tests passed\n'
