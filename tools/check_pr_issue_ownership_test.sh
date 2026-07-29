#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
subject="${repo_root}/tools/check_pr_issue_ownership.sh"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/sub2api-pr-policy-test.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT

cat >"${tmp_dir}/gh" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$*" == *"--json closingIssuesReferences"* ]]; then
  printf '%s\n' "${MOCK_CLOSING_ISSUES-32}"
elif [[ "$*" == *"--json state"* ]]; then
  printf '%s\n' "${MOCK_ISSUE_STATE-OPEN}"
else
  printf '%s\n' "${MOCK_ASSIGNEES-alice}"
fi
MOCK
chmod +x "${tmp_dir}/gh"

run_gate() {
  PATH="${tmp_dir}:${PATH}" \
    PR_NUMBER="43" \
    PR_AUTHOR="${2-alice}" \
    GITHUB_REPOSITORY="Alle-Group/sub2api" \
    MOCK_CLOSING_ISSUES="${1-32}" \
    MOCK_ISSUE_STATE="${3-OPEN}" \
    MOCK_ASSIGNEES="${4-alice}" \
    /bin/bash "${subject}"
}

run_gate 32 alice OPEN $'alice\nbob' >/dev/null

if run_gate '' alice OPEN alice >/dev/null 2>&1; then
  echo "missing closing issue unexpectedly passed" >&2
  exit 1
fi

if run_gate $'32\n33' alice OPEN alice >/dev/null 2>&1; then
  echo "multiple closing issues unexpectedly passed" >&2
  exit 1
fi

if run_gate 32 alice CLOSED alice >/dev/null 2>&1; then
  echo "closed issue unexpectedly passed" >&2
  exit 1
fi

if run_gate 32 alice OPEN bob >/dev/null 2>&1; then
  echo "unassigned author unexpectedly passed" >&2
  exit 1
fi

if PATH="${tmp_dir}:${PATH}" PR_NUMBER='43' PR_AUTHOR='' GITHUB_REPOSITORY='Alle-Group/sub2api' /bin/bash "${subject}" >/dev/null 2>&1; then
  echo "missing PR author unexpectedly passed" >&2
  exit 1
fi

printf 'PR issue ownership tests passed\n'
