#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
subject="${repo_root}/tools/check_pr_title.sh"

run_gate() {
  PR_TITLE="$1" /bin/bash "${subject}"
}

# Valid titles across all allowed types and forms.
run_gate 'feat(frontend): SCIbuddy 首页与用户控制台'
run_gate 'fix: 恢复限流账号 reset credit 自动恢复'
run_gate 'hotfix(billing): 紧急隔离计费 ID'
run_gate 'chore(ci)!: grant release quality gate permissions'
run_gate 'docs(release): 记录 release-1.0.4 生产部署'
run_gate 'perf(api): 缓存模型列表'
run_gate 'refactor: 整理计费调用'
run_gate 'test: cover concurrent capped balance billing'
run_gate 'build: 升级构建链'
run_gate 'revert: 回滚某改动'

invalid_titles=(
  '修复了 bug'
  'Update README'
  'feat no colon'
  'random: '
  '未知(type): 描述'
  'feat(): desc'
  'featscope: desc'
)
for title in "${invalid_titles[@]}"; do
  if PR_TITLE="$title" /bin/bash "${subject}" >/dev/null 2>&1; then
    printf 'invalid PR title unexpectedly passed: %s\n' "$title" >&2
    exit 1
  fi
done

# Missing env var must fail.
if /bin/bash "${subject}" >/dev/null 2>&1; then
  echo "missing PR_TITLE unexpectedly passed" >&2
  exit 1
fi

printf 'PR title gate tests passed\n'