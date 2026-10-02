#!/bin/bash
set -euo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
alpine_image=docker.1ms.run/library/alpine:3.21@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d
suffix=$$
network=sub2api-xray-smoke-$suffix
portal=xray-portal-$suffix
bridge=xray-bridge-$suffix
backend=langfuse-ingest-$suffix
tmp_dir=$(mktemp -d)

cleanup() {
  docker rm -f "$bridge" "$portal" "$backend" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

mkdir -p "$tmp_dir/work"
cd "$repo_root"
git ls-files -z deploy/langfuse | tar --null -T - -cf - | tar -xf - -C "$tmp_dir/work"
stack="$tmp_dir/work/deploy/langfuse"
cd "$stack"
LANGFUSE_PUBLIC_URL=https://langfuse.example.com ./generate-env.sh >/dev/null
sed -i 's/^XRAY_SERVER_ADDRESS=.*/XRAY_SERVER_ADDRESS=xray-portal/' .env
./generate-xray-config.sh >/dev/null

# Production publishes host 443 to container 31590 and targets host Caddy.
# This isolated topology listens directly on 443 and uses the real public TLS
# endpoint so REALITY can complete its target handshake without host services.
sed -i '0,/"port": 31590/s//"port": 443/' main-server-xray/config.json
sed -i 's/host\.docker\.internal:8443/api.sub2api.com:443/' main-server-xray/config.json
xray_image=$(sed -n 's/^XRAY_IMAGE=//p' .env)
test -n "$xray_image"

docker pull "$alpine_image" >/dev/null
docker pull "$xray_image" >/dev/null
docker network create "$network" >/dev/null
docker run -d --name "$backend" --network "$network" --network-alias langfuse-ingest \
  "$alpine_image" sh -c 'while true; do printf "HTTP/1.1 200 OK\r\nContent-Length: 11\r\nConnection: close\r\n\r\nxray-e2e-ok" | nc -l -p 3000; done' >/dev/null
docker run -d --name "$portal" --network "$network" --network-alias xray-portal \
  -v "$stack/main-server-xray/config.json:/usr/local/etc/xray/config.json:ro" \
  "$xray_image" run -config /usr/local/etc/xray/config.json >/dev/null
docker run -d --name "$bridge" --network "$network" \
  -v "$stack/.xray/bridge.json:/usr/local/etc/xray/config.json:ro" \
  "$xray_image" run -config /usr/local/etc/xray/config.json >/dev/null

for _ in $(seq 1 30); do
  if response=$(docker exec "$backend" wget -qO- -T 2 http://xray-portal:3100/ 2>/dev/null); then
    test "$response" = xray-e2e-ok
    echo "Xray VLESS Reverse + REALITY end-to-end smoke passed"
    exit 0
  fi
  sleep 1
done

docker logs "$portal" >&2 || true
docker logs "$bridge" >&2 || true
exit 1
