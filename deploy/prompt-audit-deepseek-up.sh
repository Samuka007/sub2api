#!/bin/sh
set -eu

image=${SUB2API_IMAGE:-}
if ! printf '%s\n' "$image" | grep -Eq '^ghcr\.io/alle-group/sub2api@sha256:[0-9a-fA-F]{64}$'; then
  printf 'SUB2API_IMAGE must be ghcr.io/alle-group/sub2api@sha256:<64 hex>\n' >&2
  exit 1
fi
if [ "${1:-}" = "--check-image" ]; then
  printf 'DeepSeek Prompt Audit image digest accepted\n'
  exit 0
fi
if [ "$#" -ne 0 ]; then
  printf 'usage: %s [--check-image]\n' "$0" >&2
  exit 2
fi
if docker compose version >/dev/null 2>&1; then
  exec docker compose -f deploy/docker-compose.prompt-audit-deepseek.yml up -d --no-build prompt-audit-deepseek-adapter
fi
if command -v docker-compose >/dev/null 2>&1; then
  exec docker-compose -f deploy/docker-compose.prompt-audit-deepseek.yml up -d --no-build prompt-audit-deepseek-adapter
fi
printf 'docker compose is required\n' >&2
exit 1