#!/usr/bin/env bash
set -u -o pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
log_dir="$(mktemp -d "${TMPDIR:-/tmp}/sub2api-frontend-ci.XXXXXX")"
trap 'rm -rf "$log_dir"' EXIT

names=()
pids=()
status=0

start() {
  local name=$1
  shift
  names+=("$name")
  (cd "$repo_root" && "$@") >"$log_dir/$name.log" 2>&1 &
  pids+=("$!")
}

wait_group() {
  local index
  for index in "${!pids[@]}"; do
    if ! wait "${pids[$index]}"; then
      status=1
    fi
    printf '\n===== %s =====\n' "${names[$index]}"
    cat "$log_dir/${names[$index]}.log"
  done
  names=()
  pids=()
}

mode=${1:---all}

case "$mode" in
  --all)
    # Vitest creates transient config files in the frontend root. Run ESLint
    # before Vitest so ESLint cannot race a disappearing transient file.
    start audit /bin/bash -c 'pnpm --dir frontend audit --prod --audit-level=high --json > frontend/audit.json || true; python3 tools/check_pnpm_audit_exceptions.py --audit frontend/audit.json --exceptions .github/audit-exceptions.yml'
    start lint pnpm --dir frontend run lint:check
    wait_group
    start typecheck pnpm --dir frontend run typecheck
    start test pnpm --dir frontend run test:run
    wait_group
    ;;
  --security-only)
    start audit /bin/bash -c 'pnpm --dir frontend audit --prod --audit-level=high --json > frontend/audit.json || true; python3 tools/check_pnpm_audit_exceptions.py --audit frontend/audit.json --exceptions .github/audit-exceptions.yml'
    wait_group
    ;;
  --lint-security)
    start audit /bin/bash -c 'pnpm --dir frontend audit --prod --audit-level=high --json > frontend/audit.json || true; python3 tools/check_pnpm_audit_exceptions.py --audit frontend/audit.json --exceptions .github/audit-exceptions.yml'
    start lint pnpm --dir frontend run lint:check
    wait_group
    ;;
  --type-test)
    start typecheck pnpm --dir frontend run typecheck
    start test pnpm --dir frontend run test:run
    wait_group
    ;;
  *)
    printf 'unknown mode: %s\n' "$mode" >&2
    exit 2
    ;;
esac

exit "$status"
