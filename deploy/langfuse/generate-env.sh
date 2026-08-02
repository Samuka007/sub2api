#!/bin/sh
set -eu

cd "$(dirname "$0")"
if [ -e .env ]; then
  echo ".env already exists; refusing to overwrite it" >&2
  exit 1
fi

public_url="${LANGFUSE_PUBLIC_URL:-}"
case "$public_url" in
  https://*) ;;
  *) echo "LANGFUSE_PUBLIC_URL must be an explicit https:// URL" >&2; exit 1 ;;
esac
authority=${public_url#https://}
if printf '%s' "$authority" | grep -q '[[:space:]/?#@|&\\]'; then
  echo "LANGFUSE_PUBLIC_URL contains unsupported characters" >&2
  exit 1
fi
host=${authority%%:*}
case "$host" in
  ''|.*|*.|-*|*-|*[!A-Za-z0-9.-]*)
    echo "LANGFUSE_PUBLIC_URL must include a valid hostname" >&2
    exit 1
    ;;
esac
[ "${#host}" -le 253 ] || {
  echo "LANGFUSE_PUBLIC_URL hostname is too long" >&2
  exit 1
}
remaining_host=$host
while :; do
  label=${remaining_host%%.*}
  case "$label" in
    ''|-*|*-|*[!A-Za-z0-9-]*)
      echo "LANGFUSE_PUBLIC_URL must include a valid hostname" >&2
      exit 1
      ;;
  esac
  [ "${#label}" -le 63 ] || {
    echo "LANGFUSE_PUBLIC_URL hostname label is too long" >&2
    exit 1
  }
  [ "$remaining_host" != "$label" ] || break
  remaining_host=${remaining_host#*.}
done
if [ "$authority" != "$host" ]; then
  port=${authority#*:}
  case "$port" in *[!0-9]*|'') echo "LANGFUSE_PUBLIC_URL contains an invalid port" >&2; exit 1 ;; esac
  [ "$port" -ge 1 ] && [ "$port" -le 65535 ] || {
    echo "LANGFUSE_PUBLIC_URL contains an invalid port" >&2
    exit 1
  }
fi

cleanup_env=1
trap '[ "$cleanup_env" -eq 0 ] || rm -f .env' EXIT HUP INT TERM

random_hex() { openssl rand -hex "$1"; }
project_public_key="${LANGFUSE_PROJECT_PUBLIC_KEY:-pk-lf-$(random_hex 16)}"
project_secret_key="${LANGFUSE_PROJECT_SECRET_KEY:-sk-lf-$(random_hex 16)}"

sed \
  -e "s|NEXTAUTH_URL=GENERATE_ME|NEXTAUTH_URL=$public_url|" \
  -e "s/POSTGRES_PASSWORD=GENERATE_ME/POSTGRES_PASSWORD=$(random_hex 24)/" \
  -e "s/CLICKHOUSE_PASSWORD=GENERATE_ME/CLICKHOUSE_PASSWORD=$(random_hex 24)/" \
  -e "s/MINIO_ROOT_PASSWORD=GENERATE_ME/MINIO_ROOT_PASSWORD=$(random_hex 24)/" \
  -e "s/REDIS_AUTH=GENERATE_ME/REDIS_AUTH=$(random_hex 24)/" \
  -e "s/SALT=GENERATE_ME/SALT=$(random_hex 32)/" \
  -e "s/ENCRYPTION_KEY=GENERATE_64_HEX_CHARS/ENCRYPTION_KEY=$(random_hex 32)/" \
  -e "s/NEXTAUTH_SECRET=GENERATE_ME/NEXTAUTH_SECRET=$(random_hex 32)/" \
  -e "s/LANGFUSE_INIT_ORG_ID=GENERATE_ME/LANGFUSE_INIT_ORG_ID=langfuse-org-$(random_hex 8)/" \
  -e "s/LANGFUSE_INIT_PROJECT_ID=GENERATE_ME/LANGFUSE_INIT_PROJECT_ID=langfuse-project-$(random_hex 8)/" \
  -e "s/LANGFUSE_INIT_PROJECT_PUBLIC_KEY=GENERATE_ME/LANGFUSE_INIT_PROJECT_PUBLIC_KEY=$project_public_key/" \
  -e "s/LANGFUSE_INIT_PROJECT_SECRET_KEY=GENERATE_ME/LANGFUSE_INIT_PROJECT_SECRET_KEY=$project_secret_key/" \
  -e "s/LANGFUSE_INIT_USER_PASSWORD=GENERATE_ME/LANGFUSE_INIT_USER_PASSWORD=$(random_hex 20)/" \
  .env.example > .env

chmod 600 .env
./generate-xray-config.sh
cleanup_env=0
trap - EXIT HUP INT TERM
echo "Created .env and Xray configs for $public_url. Secrets were not printed."
