#!/usr/bin/env bash
set -euo pipefail

# Validate that a Pull Request title follows Conventional Commits.
#
# Usage (environment input):
#   PR_TITLE="feat(ci): grant permissions" tools/check_pr_title.sh
#
# Required form: "type(scope)!: description" or "type: description".
# Allowed types mirror the repo branch prefixes plus standard Conventional Commits:
#   feat fix hotfix sync docs style refactor perf test build ci chore revert otel

for variable_name in PR_TITLE; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf 'required environment variable is missing: %s\n' "${variable_name}" >&2
    exit 1
  fi
done

pattern='^(feat|fix|hotfix|sync|docs|style|refactor|perf|test|build|ci|chore|revert|otel)(\([^()]+\))?(!)?: .+'

if [[ ! "$PR_TITLE" =~ $pattern ]]; then
  printf 'Pull Request 标题不符合 Conventional Commits 规范：%s\n' "$PR_TITLE" >&2
  printf '需要形如 "type(scope): 描述" 或 "type: 描述"。' >&2
  printf '允许的 type：feat fix hotfix sync docs style refactor perf test build ci chore revert otel。\n' >&2
  exit 1
fi

printf 'PR title gate passed.\n'