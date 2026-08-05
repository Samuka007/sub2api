#!/usr/bin/env bash
set -u -o pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
log_dir="$(mktemp -d "${TMPDIR:-/tmp}/sub2api-backend-ci.XXXXXX")"
trap 'rm -rf "$log_dir"' EXIT

names=(unit integration build)
pids=()
status=0

run() {
  local name=$1
  local target=$2
  (cd "$repo_root" && make -C backend "$target") >"$log_dir/$name.log" 2>&1 &
  pids+=("$!")
}

run unit test-unit
run integration test-integration
run build build

for index in "${!pids[@]}"; do
  if ! wait "${pids[$index]}"; then
    status=1
  fi
  printf '\n===== %s =====\n' "${names[$index]}"
  cat "$log_dir/${names[$index]}.log"
done

exit "$status"
