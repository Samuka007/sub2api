#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

if docker compose version >/dev/null 2>&1; then
  compose_command='docker compose'
elif command -v docker-compose >/dev/null 2>&1; then
  compose_command='docker-compose'
else
  printf 'docker compose is required to validate the DeepSeek Prompt Audit adapter\n' >&2
  exit 1
fi

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
printf '%s\n' 'deepseek-test-key' >"$tmp_dir/deepseek_api_key"
printf '%s\n' 'adapter-test-token' >"$tmp_dir/adapter_token"

expected_image='ghcr.io/alle-group/sub2api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
SUB2API_IMAGE="$expected_image" /bin/sh deploy/prompt-audit-deepseek-up.sh --check-image >/dev/null
for invalid_image in \
  'ghcr.io/alle-group/sub2api:latest' \
  'ghcr.io/alle-group/sub2api:v1.2.3' \
  'ghcr.io/alle-group/sub2api@sha256:aaaa' \
  'example.com/alle-group/sub2api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
do
  if SUB2API_IMAGE="$invalid_image" /bin/sh deploy/prompt-audit-deepseek-up.sh --check-image >/dev/null 2>&1; then
    printf 'mutable or untrusted image was accepted: %s\n' "$invalid_image" >&2
    exit 1
  fi
done

SUB2API_IMAGE="$expected_image" \
PROMPT_AUDIT_DEEPSEEK_API_KEY_FILE="$tmp_dir/deepseek_api_key" \
PROMPT_AUDIT_DEEPSEEK_ADAPTER_TOKEN_FILE="$tmp_dir/adapter_token" \
  $compose_command -f deploy/docker-compose.prompt-audit-deepseek.yml config --format json >"$tmp_dir/rendered.json"

python3 - "$tmp_dir/rendered.json" <<'PY'
import json
import pathlib
import sys

rendered = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
service = rendered["services"]["prompt-audit-deepseek-adapter"]
errors = []
if service.get("image") != "ghcr.io/alle-group/sub2api@sha256:" + "a" * 64:
    errors.append(f"adapter image is not the validated digest: {service.get('image')!r}")
if service.get("ports"):
    errors.append("adapter must not publish a host port")
if service.get("read_only") is not True:
    errors.append("adapter root filesystem must be read-only")
if service.get("user") != "1000:1000":
    errors.append("adapter must run as the sub2api uid/gid")
if service.get("security_opt", []).count("no-new-privileges:true") != 1:
    errors.append("adapter must enable no-new-privileges exactly once")
if "ALL" not in service.get("cap_drop", []):
    errors.append("adapter must drop all Linux capabilities")
if service.get("pids_limit") != 64:
    errors.append("adapter must cap child processes")
if int(service.get("mem_limit", 0)) != 256 * 1024 * 1024:
    errors.append("adapter must cap memory")
if float(service.get("cpus", 0)) != 1.0:
    errors.append("adapter must cap CPU")
tmpfs = " ".join(service.get("tmpfs", []))
if "/tmp" not in tmpfs or "noexec" not in tmpfs or "nosuid" not in tmpfs:
    errors.append("adapter must use a bounded noexec/nosuid /tmp tmpfs")
command = service.get("command", [])
if command != ["/app/sub2api", "--prompt-audit-deepseek-adapter"]:
    errors.append(f"unexpected adapter command: {command!r}")
environment = service.get("environment", {})
if environment.get("PROMPT_AUDIT_DEEPSEEK_API_KEY_FILE") != "/run/secrets/deepseek_api_key":
    errors.append("DeepSeek API key must be supplied through a file secret")
if environment.get("PROMPT_AUDIT_DEEPSEEK_ADAPTER_TOKEN_FILE") != "/run/secrets/adapter_token":
    errors.append("adapter token must be supplied through a file secret")
serialized = json.dumps(service, sort_keys=True)
for canary in ("deepseek-test-key", "adapter-test-token"):
    if canary in serialized:
        errors.append("rendered service leaked a secret value")
secret_names = {entry["source"] if isinstance(entry, dict) else entry for entry in service.get("secrets", [])}
if secret_names != {"deepseek_api_key", "adapter_token"}:
    errors.append(f"unexpected secret wiring: {service.get('secrets')!r}")
networks = service.get("networks", {})
network_names = set(networks.keys()) if isinstance(networks, dict) else set(networks)
if network_names != {"app-local"}:
    errors.append("adapter must only join the private app-local network")
if errors:
    raise SystemExit("\n".join(errors))
print("DeepSeek Prompt Audit adapter Compose contract passed")
PY

grep -Fq 'curl' Dockerfile
grep -Fq 'curl' Dockerfile.goreleaser
grep -Fxq '/secrets/' .gitignore
grep -Fxq 'secrets/' .dockerignore
printf 'DeepSeek Prompt Audit adapter runtime dependency contract passed\n'