#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/sub2api-frontend-ci-test.XXXXXX")"
trap 'rm -rf "$tmp_dir"' EXIT
mkdir -p "$tmp_dir/bin"

cat >"$tmp_dir/bin/pnpm" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
name=${*: -1}
printf '%s\n' "$name" >>"$CALLS"
sleep 1
[[ "$name" != "${FAIL_NAME:-}" ]]
STUB

cat >"$tmp_dir/bin/python3" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'audit-check\n' >>"$CALLS"
sleep 1
STUB
chmod +x "$tmp_dir/bin/pnpm" "$tmp_dir/bin/python3"

export PATH="$tmp_dir/bin:$PATH"
export CALLS="$tmp_dir/calls"
start=$(date +%s)
if ! /bin/bash "$repo_root/tools/run_frontend_ci.sh"; then
  echo 'frontend gate failed with successful checks' >&2
  exit 1
fi
elapsed=$(( $(date +%s) - start ))
if (( elapsed >= 8 )); then
  echo "frontend checks were not parallel enough: ${elapsed}s" >&2
  exit 1
fi
if [[ $(wc -l <"$CALLS") -ne 5 ]]; then
  echo 'frontend gate did not run every check' >&2
  exit 1
fi

for mode_and_count in '--security-only 2' '--lint-security 3' '--type-test 2'; do
  read -r mode expected <<<"$mode_and_count"
  : >"$CALLS"
  if ! /bin/bash "$repo_root/tools/run_frontend_ci.sh" "$mode"; then
    echo "frontend gate mode failed: $mode" >&2
    exit 1
  fi
  if [[ $(wc -l <"$CALLS") -ne $expected ]]; then
    echo "frontend gate mode ran the wrong checks: $mode" >&2
    exit 1
  fi
done
if /bin/bash "$repo_root/tools/run_frontend_ci.sh" --unknown; then
  echo 'frontend gate accepted an unknown mode' >&2
  exit 1
fi

: >"$CALLS"
export FAIL_NAME=typecheck
if /bin/bash "$repo_root/tools/run_frontend_ci.sh"; then
  echo 'parallel frontend gate ignored a failed command' >&2
  exit 1
fi
if [[ $(wc -l <"$CALLS") -ne 5 ]]; then
  echo 'frontend gate stopped before collecting every failure' >&2
  exit 1
fi

printf 'Frontend CI parallel runner tests passed\n'
