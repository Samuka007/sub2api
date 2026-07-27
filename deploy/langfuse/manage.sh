#!/bin/sh
set -eu
cd "$(dirname "$0")"
compose() { docker compose --env-file .env -f docker-compose.yml "$@"; }
require_env() { [ -f .env ] || { echo "Missing .env; run ./generate-env.sh first" >&2; exit 1; }; }

case "${1:-}" in
  start) require_env; compose pull; compose up -d ;;
  stop) require_env; compose down ;;
  restart) require_env; compose up -d --force-recreate ;;
  status) require_env; compose ps ;;
  logs) require_env; compose logs --tail 200 -f "${2:-langfuse-web}" ;;
  check)
    require_env
    compose config --quiet
    port="$(sed -n 's/^LANGFUSE_PORT=//p' .env)"
    curl --fail --silent --show-error "http://127.0.0.1:${port:-3000}/api/public/health"
    printf '\n'
    ;;
  use-official-images)
    require_env
    sed -i \
      -e 's#^LANGFUSE_WEB_IMAGE=.*#LANGFUSE_WEB_IMAGE=docker.io/langfuse/langfuse:3#' \
      -e 's#^LANGFUSE_WORKER_IMAGE=.*#LANGFUSE_WORKER_IMAGE=docker.io/langfuse/langfuse-worker:3#' \
      -e 's#^POSTGRES_IMAGE=.*#POSTGRES_IMAGE=docker.io/library/postgres:17#' \
      -e 's#^CLICKHOUSE_IMAGE=.*#CLICKHOUSE_IMAGE=docker.io/clickhouse/clickhouse-server:25.12#' \
      -e 's#^MINIO_IMAGE=.*#MINIO_IMAGE=cgr.dev/chainguard/minio:latest#' \
      -e 's#^REDIS_IMAGE=.*#REDIS_IMAGE=docker.io/library/redis:7#' .env
    echo "Switched .env to official image registries."
    ;;
  *) echo "Usage: $0 {start|stop|restart|status|logs [service]|check|use-official-images}" >&2; exit 2 ;;
esac
