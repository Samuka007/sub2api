#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
subject="${repo_root}/tools/generate_release_notes.sh"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/sub2api-release-notes-test.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT

repo="${tmp_dir}/repo"
mkdir -p "$repo"
git -C "$repo" init -q
git -C "$repo" config user.name "Release Notes Test"
git -C "$repo" config user.email "release-notes-test@example.invalid"
git -C "$repo" config commit.gpgsign false

commit() { git -C "$repo" commit -q --allow-empty -m "$1"; }
tag() { git -C "$repo" tag -a "$1" -m "$2"; }

# First release baseline.
commit "feat(core): 首个功能 (#1)"
tag release-1.0.0 "首发"

# Commits to be enumerated between releases (note the mix of types and churn).
commit "feat(frontend): SCIbuddy 首页 (#2)"
commit "fix(billing): 稳定计费 ID (#3)"
commit "chore: sync VERSION to 1.0.1 [skip ci]"
commit "docs(release): 记录部署 (#4)"
commit "Merge pull request #5 from Alle-Group/x/y"
commit "直接的提交无前缀"
tag release-1.0.1 "第二个版本"

output="$(cd "$repo" && TAG_NAME=release-1.0.1 /bin/bash "${subject}")"

grep -F '## 本次引入的提交' <<<"$output"
grep -F '### 新增' <<<"$output"
grep -F '### 修复' <<<"$output"
grep -F '### 文档' <<<"$output"
grep -F '### 变更' <<<"$output"
grep -F 'feat(frontend): SCIbuddy 首页 (#2)' <<<"$output"
grep -F 'fix(billing): 稳定计费 ID (#3)' <<<"$output"
grep -F 'docs(release): 记录部署 (#4)' <<<"$output"
grep -F '直接的提交无前缀' <<<"$output"

# Automated churn and merge commits must be filtered out.
if grep -F 'chore: sync VERSION to 1.0.1 [skip ci]' <<<"$output"; then
  echo "auto sync commit unexpectedly included" >&2
  exit 1
fi
if grep -F 'Merge pull request #5' <<<"$output"; then
  echo "merge commit unexpectedly included" >&2
  exit 1
fi

# New groups must not be emitted when empty.
if grep -F '### 测试' <<<"$output"; then
  echo "empty group unexpectedly emitted" >&2
  exit 1
fi

# First release (no previous tag) must emit nothing.
early_output="$(cd "$repo" && TAG_NAME=release-1.0.0 /bin/bash "${subject}")"
if [[ -n "$early_output" ]]; then
  echo "first release unexpectedly emitted a section" >&2
  exit 1
fi

# perf / test / refactor groups land in their own buckets.
commit "perf(api): 缓存模型列表 (#6)"
commit "test: cover concurrent billing (#7)"
commit "refactor: 整理计费 (#8)"
tag release-1.0.2 "分层组测试"
output2="$(cd "$repo" && TAG_NAME=release-1.0.2 /bin/bash "${subject}")"
grep -F '### 性能' <<<"$output2"
grep -F '### 测试' <<<"$output2"
grep -F '### 重构' <<<"$output2"

printf 'Release notes generator tests passed\n'