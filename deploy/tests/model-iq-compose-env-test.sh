#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

compose_files=(
  "deploy/docker-compose.yml"
  "deploy/docker-compose.local.yml"
  "deploy/docker-compose.standalone.yml"
  "deploy/docker-compose.host-infra.yml"
  "deploy/docker-compose.prod-parity.yml"
)

expected_env=(
  'CODEX_RADAR_ENABLED|${CODEX_RADAR_ENABLED:-false}'
  'CODEX_RADAR_BASE_URL|${CODEX_RADAR_BASE_URL:-https://codexradar.com/api/v1/current}'
  'CODEX_RADAR_API_TOKEN|${CODEX_RADAR_API_TOKEN:-}'
  'CODEX_RADAR_TIMEOUT|${CODEX_RADAR_TIMEOUT:-15s}'
  'CODEX_RADAR_CACHE_TTL|${CODEX_RADAR_CACHE_TTL:-5m}'
)

fail() {
  printf 'Model IQ Compose wiring check failed: %s\n' "$*" >&2
  exit 1
}

assert_compose_env() {
  local relative_file="$1"
  local file="${repo_root}/${relative_file}"
  local entry key expansion

  [[ -f "$file" ]] || fail "missing ${relative_file}"

  for entry in "${expected_env[@]}"; do
    IFS='|' read -r key expansion <<< "$entry"
    if ! grep -Fq -- "- ${key}=${expansion}" "$file" && \
       ! grep -Fq -- "${key}: ${expansion}" "$file"; then
      fail "${relative_file} does not pass through ${key} with ${expansion}"
    fi
  done
}

for compose_file in "${compose_files[@]}"; do
  assert_compose_env "$compose_file"
done

env_example="${repo_root}/deploy/.env.example"
expected_example=(
  'CODEX_RADAR_ENABLED=false'
  'CODEX_RADAR_BASE_URL=https://codexradar.com/api/v1/current'
  'CODEX_RADAR_API_TOKEN='
  'CODEX_RADAR_TIMEOUT=15s'
  'CODEX_RADAR_CACHE_TTL=5m'
)

for expected_line in "${expected_example[@]}"; do
  grep -Fxq -- "$expected_line" "$env_example" || \
    fail "deploy/.env.example is missing ${expected_line}"
done

printf 'Model IQ Compose environment wiring is complete.\n'
