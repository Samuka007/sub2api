#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}"

step() {
  printf '\n==> %s\n' "$1"
  shift
  "$@"
}

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    printf 'required command is missing: %s\n' "$1" >&2
    exit 1
  fi
}

for command_name in bash docker go golangci-lint node pnpm python3; do
  require_command "${command_name}"
done

if [[ "$(go env GOVERSION)" != "go1.27.0" ]]; then
  printf 'Go version must be go1.27.0; got %s\n' "$(go env GOVERSION)" >&2
  exit 1
fi

if [[ "$(node --version)" != v24.* ]]; then
  printf 'Node.js major version must be 24; got %s\n' "$(node --version)" >&2
  exit 1
fi

if [[ "$(pnpm --version)" != "9.15.9" ]]; then
  printf 'pnpm version must be 9.15.9; got %s\n' "$(pnpm --version)" >&2
  exit 1
fi

if ! golangci-lint version 2>&1 | grep -Eq 'version (v)?2\.9([.[:space:]]|$)'; then
  printf 'golangci-lint version must be 2.9.x\n' >&2
  golangci-lint version >&2 || true
  exit 1
fi

step "Repository governance tests" python3 -m unittest tools.test_check_repository_governance
step "PR issue ownership tests" /bin/bash tools/check_pr_issue_ownership_test.sh
step "PR title gate tests" /bin/bash tools/test_check_pr_title.sh
step "Release notes generator tests" /bin/bash tools/test_generate_release_notes.sh
step "Review Skill smoke tests" /bin/bash tools/test_review_skill.sh
step "Repository governance" python3 tools/check_repository_governance.py

printf '\n==> Deployment script syntax\n'
for script in deploy/apple-container.sh deploy/install.sh deploy/langfuse/*.sh deploy/tests/*.sh; do
  /bin/bash -n "${script}"
done

if [[ "$(uname -s)" == "Darwin" ]]; then
  step "Apple container deployment contract" /bin/bash deploy/tests/apple-container-test.sh
else
  printf '\n==> Apple container deployment contract (skipped: requires macOS)\n'
fi
step "Caddy cache configuration contract" /bin/sh deploy/test-caddyfile-cache.sh
step "Docker Compose security contract" /bin/sh deploy/tests/docker-compose-security-test.sh
step "Docker runtime resource build diagnostics" /bin/sh deploy/tests/docker-runtime-resources-output-test.sh
step "Docker runtime resource contract" /bin/sh deploy/tests/docker-runtime-resources-test.sh
step "Model IQ Compose contract" /bin/bash deploy/tests/model-iq-compose-env-test.sh
step "Install GitHub token contract" /bin/bash deploy/tests/install-github-token-test.sh
step "Model tracing Compose contract" /bin/bash deploy/tests/model-tracing-compose-test.sh
step "Langfuse deployment contract" /bin/bash deploy/tests/langfuse-deployment-test.sh
step "Langfuse read isolation contract" /bin/bash deploy/tests/langfuse-read-isolation-test.sh

step "Backend unit tests" make -C backend test-unit
step "Backend integration tests" make -C backend test-integration
step "Backend build" make -C backend build
step "Backend lint" bash -c 'cd backend && golangci-lint run --timeout=30m ./...'
step "Backend vulnerability scan" bash -c 'cd backend && go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...'

step "Frontend frozen install" pnpm --dir frontend install --frozen-lockfile
step "Frontend lint, typecheck, and tests" make test-frontend
step "Frontend production build" make build-frontend

audit_file="$(mktemp "${TMPDIR:-/tmp}/sub2api-audit.XXXXXX.json")"
trap 'rm -f "${audit_file}"' EXIT
printf '\n==> Frontend production dependency audit\n'
pnpm --dir frontend audit --prod --audit-level=high --json >"${audit_file}" || true
python3 tools/check_pnpm_audit_exceptions.py \
  --audit "${audit_file}" \
  --exceptions .github/audit-exceptions.yml

printf '\nPR quality gate passed\n'
