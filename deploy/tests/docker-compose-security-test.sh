#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

if docker compose version >/dev/null 2>&1; then
  compose_command='docker compose'
elif command -v docker-compose >/dev/null 2>&1; then
  compose_command='docker-compose'
else
  printf 'docker compose is required to validate deployment configuration\n' >&2
  exit 1
fi

check_application_security_opt_source() {
  file=$1
  count=$(
    awk '
      $0 == "  sub2api:" {
        in_application = 1
        next
      }
      in_application && $0 ~ /^  [A-Za-z0-9_-]+:$/ {
        in_application = 0
      }
      in_application && $0 == "    security_opt:" {
        in_security_opt = 1
        next
      }
      in_application && in_security_opt && $0 == "      - no-new-privileges:true" {
        count++
      }
      END { print count + 0 }
    ' "$file"
  )

  if [ "$count" -ne 1 ]; then
    printf '%s must declare no-new-privileges exactly once for the sub2api service\n' "$file" >&2
    exit 1
  fi
}

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
cat >"$tmp_dir/required.env" <<'ENV'
POSTGRES_PASSWORD=compose-test
DATABASE_HOST=postgres-test
DATABASE_PASSWORD=compose-test
REDIS_HOST=redis-test
TOTP_ENCRYPTION_KEY=compose-test-totp-key
MODERATION_API_KEY=compose-test-moderation-key
MODERATION_RELAY_API_KEY=compose-test-relay-key
ENV

rendered_files=''
for compose_file in \
  deploy/docker-compose.yml \
  deploy/docker-compose.local.yml \
  deploy/docker-compose.standalone.yml \
  deploy/docker-compose.dev.yml \
  deploy/docker-compose.host-infra.yml \
  deploy/docker-compose.prod-parity.yml
do
  check_application_security_opt_source "$compose_file"
  rendered_file="$tmp_dir/$(basename "$compose_file").json"
  # shellcheck disable=SC2086
  $compose_command --env-file "$tmp_dir/required.env" -f "$compose_file" config --format json >"$rendered_file"
  rendered_files="$rendered_files $rendered_file"
done

# rendered_files contains only mktemp paths produced above.
# shellcheck disable=SC2086
python3 - $rendered_files <<'PY'
import json
import pathlib
import sys

expected = "no-new-privileges:true"
failures = []
for rendered_path in map(pathlib.Path, sys.argv[1:]):
    rendered = json.loads(rendered_path.read_text(encoding="utf-8"))
    security_opt = rendered["services"]["sub2api"].get("security_opt", [])
    if security_opt.count(expected) != 1:
        failures.append(
            f"{rendered_path.name}: rendered services.sub2api.security_opt "
            f"must contain {expected!r} exactly once; got {security_opt!r}"
        )

if failures:
    raise SystemExit("\n".join(failures))

print(f"validated rendered no-new-privileges contract in {len(sys.argv) - 1} Compose files")
PY

printf 'docker compose security test passed\n'
