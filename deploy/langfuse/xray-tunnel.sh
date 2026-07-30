#!/bin/sh
set -eu
cd "$(dirname "$0")"
compose() { docker compose --env-file .env -f docker-compose.yml "$@"; }

case "${1:-}" in
  render) ./generate-xray-config.sh ;;
  start) ./generate-xray-config.sh; compose up -d xray-bridge ;;
  stop) compose stop xray-bridge ;;
  status) compose ps xray-bridge ;;
  logs) compose logs --tail 100 -f xray-bridge ;;
  *) echo "Usage: $0 {render|start|stop|status|logs}" >&2; exit 2 ;;
esac
