#!/bin/sh
set -eu
cd "$(dirname "$0")"
compose() { docker compose --env-file .env -f docker-compose.yml "$@"; }
require_env() { [ -f .env ] || { echo "Missing .env; run ./generate-env.sh first" >&2; exit 1; }; }
render_xray() { ./generate-xray-config.sh; }

case "${1:-}" in
  start) require_env; render_xray; compose up -d --pull never ;;
  pull) require_env; compose pull ;;
  stop) require_env; compose down ;;
  restart) require_env; render_xray; compose up -d --pull never --force-recreate ;;
  status) require_env; compose ps ;;
  logs) require_env; compose logs --tail 200 -f "${2:-langfuse-web}" ;;
  check)
    require_env
    compose config --quiet
    port="$(sed -n 's/^LANGFUSE_PORT=//p' .env)"
    curl --fail --silent --show-error "http://127.0.0.1:${port:-3000}/api/public/health"
    printf '\n'
    ;;
  *) echo "Usage: $0 {start|pull|stop|restart|status|logs [service]|check}" >&2; exit 2 ;;
esac
