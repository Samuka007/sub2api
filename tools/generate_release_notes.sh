#!/usr/bin/env bash
set -euo pipefail

# Generate a categorized "commits since the previous release" Markdown section.
#
# Usage:
#   TAG_NAME=release-1.0.5 tools/generate_release_notes.sh
#
# Enumerates commit subjects in the range (previous release-* tag, TAG_NAME] from the
# current git repository, classifies each by its Conventional Commits type into Chinese
# semantic groups (新增 / 修复 / 变更 / 文档 / 测试 / 重构 / 性能), and prints the section
# to stdout. Automated churn ("chore: sync VERSION ... [skip ci]") and merge commits
# ("Merge pull request #NN ...", "Merge branch ...") are filtered out. Commits without
# a recognized type fall into 变更. When no previous release tag is found the script
# exits with no output, so the first release simply omits the section.

for variable_name in TAG_NAME; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf 'required environment variable is missing: %s\n' "${variable_name}" >&2
    exit 1
  fi
done

if ! git rev-parse --git-dir >/dev/null 2>&1; then
  printf 'not a git repository\n' >&2
  exit 1
fi

if [[ ! "$TAG_NAME" =~ ^release-[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'invalid release tag: %s\n' "$TAG_NAME" >&2
  exit 1
fi

# release-* tags sorted by version, descending. The entry immediately after TAG_NAME
# is the previous release (the greatest version strictly less than TAG_NAME).
previous_tag=""
found_current=false
while IFS= read -r tag; do
  [[ -z "$tag" ]] && continue
  if [[ "$found_current" == true ]]; then
    previous_tag="$tag"
    break
  fi
  if [[ "$tag" == "$TAG_NAME" ]]; then
    found_current=true
  fi
done < <(git for-each-ref refs/tags --format='%(refname:short)' --sort=-v:refname \
  | grep -E '^release-[0-9]+\.[0-9]+\.[0-9]+$' || true)

if [[ -z "$previous_tag" ]]; then
  # First release or no prior release-* tag: nothing to enumerate.
  exit 0
fi

tag_commit="$(git rev-parse "${TAG_NAME}^{commit}")"
prev_commit="$(git rev-parse "${previous_tag}^{commit}")"

should_skip() {
  local subject="$1"
  case "$subject" in
    "chore: sync VERSION to "*"[skip ci]") return 0 ;;
    "Merge pull request "*|"Merge branch "*) return 0 ;;
  esac
  return 1
}

classify() {
  local subject="$1" type=""
  if [[ "$subject" =~ ^([[:alnum:]]+)(\([^()]+\))?(!)?:\ (.+) ]]; then
    type="${BASH_REMATCH[1]}"
  fi
  case "$type" in
    feat) printf '新增' ;;
    fix|hotfix) printf '修复' ;;
    docs) printf '文档' ;;
    test) printf '测试' ;;
    refactor) printf '重构' ;;
    perf) printf '性能' ;;
    *) printf '变更' ;;
  esac
}

group_new=""; group_fix=""; group_change=""; group_docs=""; group_test=""; group_refactor=""; group_perf=""

append_line() {
  local var="$1" line="$2"
  if [[ -z "${!var}" ]]; then
    printf -v "$var" '%s' "- ${line}"
  else
    printf -v "$var" '%s\n%s' "${!var}" "- ${line}"
  fi
}

add_commit() {
  local subject="$1" group
  should_skip "$subject" && return 0
  group="$(classify "$subject")"
  case "$group" in
    新增) append_line group_new "$subject" ;;
    修复) append_line group_fix "$subject" ;;
    文档) append_line group_docs "$subject" ;;
    测试) append_line group_test "$subject" ;;
    重构) append_line group_refactor "$subject" ;;
    性能) append_line group_perf "$subject" ;;
    *)   append_line group_change "$subject" ;;
  esac
}

while IFS= read -r subject; do
  add_commit "$subject"
done < <(git log --pretty=tformat:'%s' "$prev_commit".."$tag_commit")

if [[ -z "$group_new" && -z "$group_fix" && -z "$group_change" && -z "$group_docs" \
   && -z "$group_test" && -z "$group_refactor" && -z "$group_perf" ]]; then
  exit 0
fi

{
  printf '## 本次引入的提交\n\n'
  if [[ -n "$group_new" ]]; then printf '### 新增\n\n%s\n\n' "$group_new"; fi
  if [[ -n "$group_fix" ]]; then printf '### 修复\n\n%s\n\n' "$group_fix"; fi
  if [[ -n "$group_change" ]]; then printf '### 变更\n\n%s\n\n' "$group_change"; fi
  if [[ -n "$group_docs" ]]; then printf '### 文档\n\n%s\n\n' "$group_docs"; fi
  if [[ -n "$group_test" ]]; then printf '### 测试\n\n%s\n\n' "$group_test"; fi
  if [[ -n "$group_refactor" ]]; then printf '### 重构\n\n%s\n\n' "$group_refactor"; fi
  if [[ -n "$group_perf" ]]; then printf '### 性能\n\n%s\n\n' "$group_perf"; fi
}