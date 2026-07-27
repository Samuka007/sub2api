#!/bin/sh
set -eu
cd "$(dirname "$0")"
compose() { docker compose --env-file .env -f docker-compose.yml "$@"; }

case "${1:-}" in
  start) compose up -d --build ssh-tunnel ;;
  stop) compose stop ssh-tunnel ;;
  status) compose ps ssh-tunnel ;;
  logs) compose logs --tail 100 -f ssh-tunnel ;;
  *) echo "Usage: $0 {start|stop|status|logs}" >&2; exit 2 ;;
esac
