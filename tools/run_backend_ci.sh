#!/usr/bin/env bash
set -u -o pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
log_dir="$(mktemp -d "${TMPDIR:-/tmp}/sub2api-backend-ci.XXXXXX")"
trap 'rm -rf "$log_dir"' EXIT

# 串行执行 unit → integration → build。
# 原实现三路并行（每个 make 目标各 1 个 go 进程，go 内部再按 -p 并行），
# 在 self-hosted runner 的 MemoryHigh=4G 上限下瞬时内存峰值超限，触发内核
# cgroup 节流：进程被卡在 mem_cgroup_handle_over_high / futex 等待，反而比
# 串行更慢（实测 ent 包单包 0.08s，三路并行下被节流拖到 50+ 分钟）。
# 串行时 unit 先编译，其 go-build 缓存被 integration / build 复用，
# 且瞬时内存只占一路，不再撞内存墙。
names=(unit integration build)
targets=(test-unit test-integration build)
status=0

for index in "${!names[@]}"; do
  printf '\n===== %s =====\n' "${names[$index]}"
  if ! (cd "$repo_root" && make -C backend "${targets[$index]}") \
      >"$log_dir/${names[$index]}.log" 2>&1; then
    status=1
  fi
  printf '===== %s: exit %s =====\n' "${names[$index]}" "$?"
  cat "$log_dir/${names[$index]}.log"
done

exit "$status"