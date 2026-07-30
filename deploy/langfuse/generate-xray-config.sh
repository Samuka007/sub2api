#!/bin/sh
set -eu

cd "$(dirname "$0")"
[ -f .env ] || { echo "Missing .env; run ./generate-env.sh first" >&2; exit 1; }

env_value() {
  sed -n "s/^$1=//p" .env | tail -n 1
}

set_env_value() {
  key="$1"
  value="$2"
  temp=".env.tmp.$$"
  awk -v key="$key" -v value="$value" '
    BEGIN { found = 0 }
    index($0, key "=") == 1 {
      if (!found) print key "=" value
      found = 1
      next
    }
    { print }
    END { if (!found) print key "=" value }
  ' .env > "$temp"
  chmod 600 "$temp"
  mv "$temp" .env
}

base64url_last_32() {
  tail -c 32 | openssl base64 -A | tr '+/' '-_' | tr -d '='
}

ensure_default() {
  key="$1"
  default="$2"
  [ -n "$(env_value "$key")" ] || set_env_value "$key" "$default"
}

migrate_default() {
  key="$1"
  old="$2"
  new="$3"
  [ "$(env_value "$key")" = "$old" ] || return 0
  set_env_value "$key" "$new"
}

# Populate Xray settings when upgrading an existing deployment. Xray owns the
# public 443 socket; normal HTTPS is sent to Caddy through the host gateway.
ensure_default XRAY_IMAGE ghcr.io/xtls/xray-core:26.5.9
ensure_default XRAY_SERVER_ADDRESS 38.244.20.220
ensure_default XRAY_SERVER_PORT 443
ensure_default XRAY_PORTAL_BIND_IP 172.18.0.1
ensure_default XRAY_PORTAL_PORT 3100
ensure_default XRAY_REALITY_SERVER_NAME api.sub2api.com
ensure_default XRAY_REALITY_TARGET host.docker.internal:8443

# The previous generated defaults used a dedicated 31590 listener and sent
# failed REALITY handshakes back to the public 443 address. Migrate only those
# exact defaults so an intentionally customized deployment is left untouched.
migrate_default XRAY_SERVER_PORT 31590 443
migrate_default XRAY_REALITY_TARGET api.sub2api.com:443 host.docker.internal:8443

uuid="$(env_value XRAY_UUID)"
private_key="$(env_value XRAY_REALITY_PRIVATE_KEY)"
public_key="$(env_value XRAY_REALITY_PUBLIC_KEY)"
short_id="$(env_value XRAY_REALITY_SHORT_ID)"

if [ -z "$uuid" ]; then
  uuid="$(uuidgen | tr 'A-F' 'a-f')"
  set_env_value XRAY_UUID "$uuid"
fi

if [ -z "$private_key" ] || [ -z "$public_key" ]; then
  key_file=".xray-key.$$"
  trap 'test ! -f "$key_file" || { : > "$key_file"; rm "$key_file"; }' EXIT HUP INT TERM
  openssl genpkey -algorithm X25519 -out "$key_file"
  chmod 600 "$key_file"
  private_key="$(openssl pkey -in "$key_file" -outform DER | base64url_last_32)"
  public_key="$(openssl pkey -in "$key_file" -pubout -outform DER | base64url_last_32)"
  set_env_value XRAY_REALITY_PRIVATE_KEY "$private_key"
  set_env_value XRAY_REALITY_PUBLIC_KEY "$public_key"
  : > "$key_file"
  rm "$key_file"
  trap - EXIT HUP INT TERM
fi

if [ -z "$short_id" ]; then
  short_id="$(openssl rand -hex 8)"
  set_env_value XRAY_REALITY_SHORT_ID "$short_id"
fi

server_address="$(env_value XRAY_SERVER_ADDRESS)"
server_port="$(env_value XRAY_SERVER_PORT)"
portal_bind_ip="$(env_value XRAY_PORTAL_BIND_IP)"
portal_port="$(env_value XRAY_PORTAL_PORT)"
server_name="$(env_value XRAY_REALITY_SERVER_NAME)"
target="$(env_value XRAY_REALITY_TARGET)"

case "$server_address" in *[!A-Za-z0-9.:-]*|'') echo "Invalid XRAY_SERVER_ADDRESS" >&2; exit 1 ;; esac
case "$server_port" in *[!0-9]*|'') echo "Invalid XRAY_SERVER_PORT" >&2; exit 1 ;; esac
case "$portal_bind_ip" in *[!0-9.]*|'') echo "Invalid XRAY_PORTAL_BIND_IP" >&2; exit 1 ;; esac
case "$portal_port" in *[!0-9]*|'') echo "Invalid XRAY_PORTAL_PORT" >&2; exit 1 ;; esac
case "$server_name" in *[!A-Za-z0-9.-]*|'') echo "Invalid XRAY_REALITY_SERVER_NAME" >&2; exit 1 ;; esac
case "$target" in *[!A-Za-z0-9.:-]*|'') echo "Invalid XRAY_REALITY_TARGET" >&2; exit 1 ;; esac
case "$uuid" in *[!a-f0-9-]*|'') echo "Invalid XRAY_UUID" >&2; exit 1 ;; esac
case "$private_key" in *[!A-Za-z0-9_-]*|'') echo "Invalid XRAY_REALITY_PRIVATE_KEY" >&2; exit 1 ;; esac
case "$public_key" in *[!A-Za-z0-9_-]*|'') echo "Invalid XRAY_REALITY_PUBLIC_KEY" >&2; exit 1 ;; esac
case "$short_id" in *[!a-f0-9]*|'') echo "Invalid XRAY_REALITY_SHORT_ID" >&2; exit 1 ;; esac

mkdir -p .xray main-server-xray
chmod 700 .xray main-server-xray
sed \
  -e "s/__XRAY_SERVER_ADDRESS__/$server_address/g" \
  -e "s/__XRAY_SERVER_PORT__/$server_port/g" \
  -e "s/__XRAY_UUID__/$uuid/g" \
  -e "s/__XRAY_REALITY_SERVER_NAME__/$server_name/g" \
  -e "s/__XRAY_REALITY_PUBLIC_KEY__/$public_key/g" \
  -e "s/__XRAY_REALITY_SHORT_ID__/$short_id/g" \
  xray/bridge.json.template > .xray/bridge.json

sed \
  -e "s/__XRAY_UUID__/$uuid/g" \
  -e "s/__XRAY_REALITY_SERVER_NAME__/$server_name/g" \
  -e "s/__XRAY_REALITY_TARGET__/$target/g" \
  -e "s/__XRAY_REALITY_PRIVATE_KEY__/$private_key/g" \
  -e "s/__XRAY_REALITY_SHORT_ID__/$short_id/g" \
  xray/portal.json.template > main-server-xray/config.json

# The official image runs rootless. Keep the containing directories private,
# while allowing the container UID to read the bind-mounted config files.
chmod 644 .xray/bridge.json main-server-xray/config.json
cat > main-server-xray/.env <<EOF
XRAY_IMAGE=$(env_value XRAY_IMAGE)
XRAY_SERVER_PORT=$server_port
XRAY_PORTAL_BIND_IP=$portal_bind_ip
XRAY_PORTAL_PORT=$portal_port
EOF
chmod 600 main-server-xray/.env
echo "Rendered Xray bridge and main-server portal configs. Secrets were not printed."
