#!/bin/sh
set -eu

cd "$(dirname "$0")"
if [ -e .env ]; then
  echo ".env already exists; refusing to overwrite it" >&2
  exit 1
fi

host_ip="$(ip route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([^ ]*\).*/\1/p' | head -1)"
[ -n "$host_ip" ] || host_ip="127.0.0.1"
random_hex() { openssl rand -hex "$1"; }

sed \
  -e "s/SLAVE_SERVER_IP/$host_ip/g" \
  -e "s/POSTGRES_PASSWORD=GENERATE_ME/POSTGRES_PASSWORD=$(random_hex 24)/" \
  -e "s/CLICKHOUSE_PASSWORD=GENERATE_ME/CLICKHOUSE_PASSWORD=$(random_hex 24)/" \
  -e "s/MINIO_ROOT_PASSWORD=GENERATE_ME/MINIO_ROOT_PASSWORD=$(random_hex 24)/" \
  -e "s/REDIS_AUTH=GENERATE_ME/REDIS_AUTH=$(random_hex 24)/" \
  -e "s/SALT=GENERATE_ME/SALT=$(random_hex 32)/" \
  -e "s/ENCRYPTION_KEY=GENERATE_64_HEX_CHARS/ENCRYPTION_KEY=$(random_hex 32)/" \
  -e "s/NEXTAUTH_SECRET=GENERATE_ME/NEXTAUTH_SECRET=$(random_hex 32)/" \
  -e "s/LANGFUSE_INIT_ORG_ID=GENERATE_ME/LANGFUSE_INIT_ORG_ID=langfuse-org-$(random_hex 8)/" \
  -e "s/LANGFUSE_INIT_PROJECT_ID=GENERATE_ME/LANGFUSE_INIT_PROJECT_ID=langfuse-project-$(random_hex 8)/" \
  -e "s/LANGFUSE_INIT_PROJECT_PUBLIC_KEY=GENERATE_ME/LANGFUSE_INIT_PROJECT_PUBLIC_KEY=pk-lf-$(random_hex 16)/" \
  -e "s/LANGFUSE_INIT_PROJECT_SECRET_KEY=GENERATE_ME/LANGFUSE_INIT_PROJECT_SECRET_KEY=sk-lf-$(random_hex 16)/" \
  -e "s/LANGFUSE_INIT_USER_PASSWORD=GENERATE_ME/LANGFUSE_INIT_USER_PASSWORD=$(random_hex 20)/" \
  .env.example > .env

chmod 600 .env
echo "Created .env for $host_ip. Secrets were not printed."
