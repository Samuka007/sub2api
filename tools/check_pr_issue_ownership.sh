#!/usr/bin/env bash
set -euo pipefail

for variable_name in PR_NUMBER PR_AUTHOR GITHUB_REPOSITORY; do
  if [[ -z "${!variable_name:-}" ]]; then
    printf 'required environment variable is missing: %s\n' "${variable_name}" >&2
    exit 1
  fi
done

closing_issue_numbers="$(
  gh pr view "${PR_NUMBER}" \
    --repo "${GITHUB_REPOSITORY}" \
    --json closingIssuesReferences \
    --jq '.closingIssuesReferences[] | select((.repository.owner.login + "/" + .repository.name) == "'"${GITHUB_REPOSITORY}"'") | .number'
)"

issue_number=""
issue_count=0
while IFS= read -r candidate; do
  if [[ -n "${candidate}" ]]; then
    issue_number="${candidate}"
    issue_count=$((issue_count + 1))
  fi
done <<<"${closing_issue_numbers}"

if [[ "${issue_count}" -ne 1 ]]; then
  printf 'Pull Request must close exactly one issue in %s; got %s.\n' "${GITHUB_REPOSITORY}" "${issue_count}" >&2
  exit 1
fi

issue_state="$(gh issue view "${issue_number}" --repo "${GITHUB_REPOSITORY}" --json state --jq .state)"
if [[ "${issue_state}" != "OPEN" ]]; then
  printf 'Linked issue #%s must be open before merge; got %s.\n' "${issue_number}" "${issue_state}" >&2
  exit 1
fi

if ! gh issue view "${issue_number}" --repo "${GITHUB_REPOSITORY}" --json assignees --jq '.assignees[].login' | grep -Fxq "${PR_AUTHOR}"; then
  printf 'Pull Request author %s must be assigned to issue #%s.\n' "${PR_AUTHOR}" "${issue_number}" >&2
  exit 1
fi

printf 'Issue ownership gate passed for #%s.\n' "${issue_number}"
