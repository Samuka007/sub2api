#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_dir="$(cd "${script_dir}/../.." && pwd)"
stack_dir="${repo_dir}/deploy/langfuse"
compose_file="${stack_dir}/docker-compose.yml"
proxy_template="${stack_dir}/clickhouse-read-proxy.conf.template"
proxy_main_config="${stack_dir}/clickhouse-read-proxy-nginx.conf"
read_user_config="${stack_dir}/clickhouse-users.d/langfuse-read.xml"
xray_bridge_template="${stack_dir}/xray/bridge.json.template"

if docker compose version >/dev/null 2>&1; then
  compose=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  compose=(docker-compose)
else
  echo "docker compose is required to validate Langfuse read isolation" >&2
  exit 1
fi

[[ -f "${proxy_template}" ]] || {
  echo "missing ClickHouse read proxy template: ${proxy_template}" >&2
  exit 1
}
[[ -f "${proxy_main_config}" ]] || {
  echo "missing ClickHouse read proxy main config: ${proxy_main_config}" >&2
  exit 1
}
[[ -f "${read_user_config}" ]] || {
  echo "missing ClickHouse read user config: ${read_user_config}" >&2
  exit 1
}
[[ -f "${xray_bridge_template}" ]] || {
  echo "missing Xray bridge template: ${xray_bridge_template}" >&2
  exit 1
}

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

cat > "${tmp_dir}/langfuse.env" <<'ENV'
NEXTAUTH_URL=http://localhost:3000
MINIO_EXTERNAL_ENDPOINT=http://minio:9000
LANGFUSE_BIND_IP=127.0.0.1
LANGFUSE_PORT=3000
LANGFUSE_WEB_IMAGE=compose/langfuse-web:test
LANGFUSE_WORKER_IMAGE=compose/langfuse-worker:test
POSTGRES_IMAGE=compose/postgres:test
CLICKHOUSE_IMAGE=compose/clickhouse:test
CLICKHOUSE_READ_PROXY_IMAGE=compose/nginx:test
MINIO_IMAGE=compose/minio:test
REDIS_IMAGE=compose/redis:test
XRAY_IMAGE=compose/xray:test
SALT=compose-test
ENCRYPTION_KEY=0000000000000000000000000000000000000000000000000000000000000000
TELEMETRY_ENABLED=false
CLICKHOUSE_USER=compose
CLICKHOUSE_PASSWORD=compose
CLICKHOUSE_READ_PASSWORD=compose-read-only
POSTGRES_USER=compose
POSTGRES_DB=compose
MINIO_ROOT_USER=compose
MINIO_ROOT_PASSWORD=compose
REDIS_AUTH=compose
NEXTAUTH_SECRET=compose-test
LANGFUSE_INIT_ORG_ID=compose
LANGFUSE_INIT_ORG_NAME=compose
LANGFUSE_INIT_PROJECT_ID=compose
LANGFUSE_INIT_PROJECT_NAME=compose
LANGFUSE_INIT_PROJECT_PUBLIC_KEY=compose
LANGFUSE_INIT_PROJECT_SECRET_KEY=compose
LANGFUSE_INIT_USER_EMAIL=compose@example.invalid
LANGFUSE_INIT_USER_NAME=compose
LANGFUSE_INIT_USER_PASSWORD=compose
POSTGRES_PASSWORD=compose
LANGFUSE_SKIP_FINAL_FOR_OTEL_PROJECTS=true
CLICKHOUSE_READ_MAX_CONCURRENT=7
CLICKHOUSE_READ_RATE=13
CLICKHOUSE_READ_RATE_BURST=17
CLICKHOUSE_READ_MAX_EXECUTION_SECONDS=11
CLICKHOUSE_READ_PROXY_TIMEOUT_SECONDS=14
CLICKHOUSE_READ_MAX_MEMORY_BYTES=3221225472
CLICKHOUSE_READ_MAX_MEMORY_FOR_USER_BYTES=9663676416
CLICKHOUSE_READ_MAX_ROWS=61000000
CLICKHOUSE_READ_MAX_BYTES=6100000000
CLICKHOUSE_READ_MAX_RESULT_ROWS=120000
CLICKHOUSE_READ_MAX_RESULT_BYTES=120000000
CLICKHOUSE_READ_MAX_THREADS=5
CLICKHOUSE_READ_MAX_TEMPORARY_DATA_BYTES=3300000000
CLICKHOUSE_READ_EXTERNAL_SORT_BYTES=330000000
CLICKHOUSE_READ_EXTERNAL_GROUP_BY_BYTES=340000000
ENV

"${compose[@]}" --env-file "${tmp_dir}/langfuse.env" \
  -f "${compose_file}" config --format json --output "${tmp_dir}/compose.rendered"

set -a
# shellcheck disable=SC1090
source "${tmp_dir}/langfuse.env"
set +a
python3 - "${proxy_template}" "${tmp_dir}/proxy.conf" <<'PY'
import os
import pathlib
import re
import sys

source = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
allowed = {
    "CLICKHOUSE_READ_PASSWORD",
    "CLICKHOUSE_READ_MAX_CONCURRENT",
    "CLICKHOUSE_READ_RATE",
    "CLICKHOUSE_READ_RATE_BURST",
    "CLICKHOUSE_READ_PROXY_TIMEOUT_SECONDS",
}

def substitute(match):
    name = match.group(1)
    return os.environ[name] if name in allowed else match.group(0)

rendered = re.sub(r"\$\{([A-Z0-9_]+)\}", substitute, source)
pathlib.Path(sys.argv[2]).write_text(rendered, encoding="utf-8")
PY

python3 - "${tmp_dir}/compose.rendered" "${tmp_dir}/proxy.conf" "${proxy_template}" "${proxy_main_config}" "${read_user_config}" "${xray_bridge_template}" <<'PY'
import json
import pathlib
import re
import stat
import sys
import xml.etree.ElementTree as ET

rendered_text = pathlib.Path(sys.argv[1]).read_text(encoding="utf-8")
try:
    compose = json.loads(rendered_text)
except json.JSONDecodeError:
    # Docker Compose 2.25 accepts --format json but still writes normalized YAML.
    try:
        import yaml
    except ImportError as exc:
        raise SystemExit(
            "Docker Compose did not render JSON and Python PyYAML is unavailable"
        ) from exc
    compose = yaml.safe_load(rendered_text)
proxy_config = pathlib.Path(sys.argv[2]).read_text(encoding="utf-8")
proxy_template = pathlib.Path(sys.argv[3])
proxy_main_config = pathlib.Path(sys.argv[4])
read_user_config = pathlib.Path(sys.argv[5])
xray_bridge_template = pathlib.Path(sys.argv[6]).read_text(encoding="utf-8")
services = compose.get("services", {})
failures = []

if not proxy_template.stat().st_mode & stat.S_IROTH:
    failures.append("read proxy template must be readable by the non-root container user")
if not proxy_main_config.stat().st_mode & stat.S_IROTH:
    failures.append("read proxy main config must be readable by the non-root container user")
proxy_main_text = proxy_main_config.read_text(encoding="utf-8")
if "worker_processes 2;" not in proxy_main_text:
    failures.append("read proxy must use a bounded worker process count")
if "error_log /dev/stderr crit;" not in proxy_main_text:
    failures.append("read proxy main error log must suppress request-bearing errors")
if not read_user_config.stat().st_mode & stat.S_IROTH:
    failures.append("ClickHouse read user config must be readable by the container user")

required_services = {
    "langfuse-web",
    "langfuse-ingest",
    "langfuse-worker",
    "clickhouse",
    "clickhouse-read-proxy",
    "xray-bridge",
}
missing = sorted(required_services - services.keys())
if missing:
    failures.append(f"missing services: {', '.join(missing)}")

if not missing:
    ui = services["langfuse-web"]
    ingest = services["langfuse-ingest"]
    worker = services["langfuse-worker"]
    clickhouse = services["clickhouse"]
    proxy = services["clickhouse-read-proxy"]
    xray = services["xray-bridge"]

    expected_primary = "http://clickhouse:8123"
    expected_read = "http://clickhouse-read-proxy:8080"
    if ui.get("environment", {}).get("CLICKHOUSE_URL") != expected_primary:
        failures.append("UI Web must keep writes on the primary ClickHouse endpoint")
    if ui.get("environment", {}).get("CLICKHOUSE_READ_ONLY_URL") != expected_read:
        failures.append("UI Web must route supported reads through the read proxy")
    if ingest.get("environment", {}).get("CLICKHOUSE_URL") != expected_primary:
        failures.append("ingestion Web must use the primary ClickHouse endpoint")
    if "CLICKHOUSE_READ_ONLY_URL" in ingest.get("environment", {}):
        failures.append("ingestion Web must not use the read proxy")
    if "CLICKHOUSE_READ_ONLY_URL" in worker.get("environment", {}):
        failures.append("Worker must not use the read proxy")
    if ingest.get("environment", {}).get("LANGFUSE_AUTO_POSTGRES_MIGRATION_DISABLED") != "true":
        failures.append("ingestion Web must not run Postgres migrations")
    if ingest.get("environment", {}).get("LANGFUSE_AUTO_CLICKHOUSE_MIGRATION_DISABLED") != "true":
        failures.append("ingestion Web must not run ClickHouse migrations")
    ingest_web_dependency = ingest.get("depends_on", {}).get("langfuse-web", {})
    if ingest_web_dependency.get("condition") != "service_healthy":
        failures.append("ingestion Web must wait for the migration-owning Web to become healthy")
    if worker.get("environment", {}).get("CLICKHOUSE_URL") != expected_primary:
        failures.append("Worker must use the primary ClickHouse endpoint")

    if ingest.get("ports"):
        failures.append("ingestion Web must not publish a host port")
    xray_ingest_dependency = xray.get("depends_on", {}).get("langfuse-ingest", {})
    if xray_ingest_dependency.get("condition") != "service_healthy":
        failures.append("Xray bridge must wait for the ingestion Web healthcheck")
    if '"redirect": "langfuse-ingest:3000"' not in xray_bridge_template:
        failures.append("Xray bridge must route OTLP traffic to the ingestion Web")
    if "langfuse-web:3000" in xray_bridge_template:
        failures.append("Xray bridge must not route OTLP traffic to the UI Web")

    if proxy.get("ports"):
        failures.append("read proxy must not publish a host port")
    proxy_environment = proxy.get("environment", {})
    credential_keys = {
        key
        for key in proxy_environment
        if "PASSWORD" in key or "AUTH" in key or "SECRET" in key
    }
    if credential_keys != {"CLICKHOUSE_READ_PASSWORD"}:
        failures.append("read proxy must receive only the dedicated read password")
    if proxy_environment.get("CLICKHOUSE_READ_PASSWORD") != "compose-read-only":
        failures.append("read proxy did not receive the dedicated read password")
    if clickhouse.get("environment", {}).get("CLICKHOUSE_READ_PASSWORD") != "compose-read-only":
        failures.append("ClickHouse did not receive the dedicated read password")
    if proxy_environment.get("NGINX_ENVSUBST_FILTER") != "^CLICKHOUSE_READ_":
        failures.append("read proxy must substitute only CLICKHOUSE_READ_* variables")
    if proxy.get("read_only") is not True:
        failures.append("read proxy root filesystem must be read-only")
    if "ALL" not in proxy.get("cap_drop", []):
        failures.append("read proxy must drop Linux capabilities")
    if "8080" not in {str(port) for port in proxy.get("expose", [])}:
        failures.append("read proxy must expose port 8080 only to the Compose network")
    volumes = proxy.get("volumes", [])
    if not any(
        volume.get("target") == "/etc/nginx/templates/default.conf.template"
        and volume.get("read_only") is True
        for volume in volumes
        if isinstance(volume, dict)
    ):
        failures.append("read proxy template must be mounted read-only")
    if not any(
        volume.get("target") == "/etc/nginx/nginx.conf"
        and volume.get("read_only") is True
        for volume in volumes
        if isinstance(volume, dict)
    ):
        failures.append("read proxy main config must be mounted read-only")
    clickhouse_volumes = clickhouse.get("volumes", [])
    if not any(
        volume.get("target") == "/etc/clickhouse-server/users.d/langfuse-read.xml"
        and volume.get("read_only") is True
        for volume in clickhouse_volumes
        if isinstance(volume, dict)
    ):
        failures.append("ClickHouse read user config must be mounted read-only")

    for service_name in ("langfuse-web", "langfuse-ingest", "clickhouse", "clickhouse-read-proxy"):
        if not services[service_name].get("healthcheck"):
            failures.append(f"{service_name}: missing healthcheck")

expected_proxy_fragments = [
    "limit_conn read_queries 7;",
    "rate=13r/s",
    "burst=17",
    "proxy_read_timeout 14s;",
    'proxy_set_header Authorization "";',
    'proxy_set_header X-ClickHouse-User "langfuse_read";',
    'proxy_set_header X-ClickHouse-Key "compose-read-only";',
    'if ($arg_user != "") { return 400; }',
    'if ($arg_password != "") { return 400; }',
    "error_log /dev/stderr crit;",
    "proxy_pass http://clickhouse_primary;",
]
for fragment in expected_proxy_fragments:
    if fragment not in proxy_config:
        failures.append(f"proxy config missing: {fragment}")

if "${" in proxy_config:
    failures.append("proxy config contains unresolved environment placeholders")
if re.search(
    r"log_format[^;]*\$(?:request|request_uri|uri|args)(?![A-Za-z0-9_])",
    proxy_config,
    re.DOTALL,
):
    failures.append("proxy access log must not include URI, query arguments, or SQL")
if "error_log /dev/stderr warn;" in proxy_config:
    failures.append("proxy error log must not emit request-bearing warn/error entries")

root = ET.parse(read_user_config).getroot()
profile = root.find("./profiles/langfuse_read")
user = root.find("./users/langfuse_read")
if profile is None or user is None:
    failures.append("ClickHouse must define the langfuse_read user and profile")
else:
    expected_profile_env = {
        "max_concurrent_queries_for_user": "CLICKHOUSE_READ_MAX_CONCURRENT",
        "max_execution_time": "CLICKHOUSE_READ_MAX_EXECUTION_SECONDS",
        "max_memory_usage": "CLICKHOUSE_READ_MAX_MEMORY_BYTES",
        "max_memory_usage_for_user": "CLICKHOUSE_READ_MAX_MEMORY_FOR_USER_BYTES",
        "max_rows_to_read": "CLICKHOUSE_READ_MAX_ROWS",
        "max_bytes_to_read": "CLICKHOUSE_READ_MAX_BYTES",
        "max_result_rows": "CLICKHOUSE_READ_MAX_RESULT_ROWS",
        "max_result_bytes": "CLICKHOUSE_READ_MAX_RESULT_BYTES",
        "max_threads": "CLICKHOUSE_READ_MAX_THREADS",
        "max_temporary_data_on_disk_size_for_query": "CLICKHOUSE_READ_MAX_TEMPORARY_DATA_BYTES",
        "max_bytes_before_external_sort": "CLICKHOUSE_READ_EXTERNAL_SORT_BYTES",
        "max_bytes_before_external_group_by": "CLICKHOUSE_READ_EXTERNAL_GROUP_BY_BYTES",
    }
    for setting, env_name in expected_profile_env.items():
        element = profile.find(setting)
        if element is None or element.get("from_env") != env_name:
            failures.append(f"ClickHouse read profile missing {setting} from {env_name}")
    constraints = profile.find("constraints")
    if constraints is None:
        failures.append("ClickHouse read profile must constrain client-supplied settings")
    else:
        for setting, env_name in expected_profile_env.items():
            maximum = constraints.find(f"./{setting}/max")
            if maximum is None or maximum.get("from_env") != env_name:
                failures.append(f"ClickHouse read profile must cap {setting} with {env_name}")
            minimum = constraints.find(f"./{setting}/min")
            expected_minimum = "0.001" if setting == "max_execution_time" else "1"
            if minimum is None or minimum.text != expected_minimum:
                failures.append(f"ClickHouse read profile must reject unlimited zero for {setting}")
        if constraints.find("./readonly/readonly") is None:
            failures.append("ClickHouse read user must not be able to disable readonly mode")
    expected_literals = {
        "readonly": "2",
        "timeout_before_checking_execution_speed": "0",
        "read_overflow_mode": "throw",
        "result_overflow_mode": "throw",
        "log_comment": "langfuse_read_proxy",
    }
    for setting, expected in expected_literals.items():
        element = profile.find(setting)
        if element is None or element.text != expected:
            failures.append(f"ClickHouse read profile has invalid {setting}")
    password = user.find("password")
    if password is None or password.get("from_env") != "CLICKHOUSE_READ_PASSWORD":
        failures.append("langfuse_read password must come from CLICKHOUSE_READ_PASSWORD")
    if user.findtext("profile") != "langfuse_read":
        failures.append("langfuse_read user must use the bounded read profile")

if failures:
    raise SystemExit("\n".join(failures))

print("validated Langfuse Web isolation, read query limits, routing, and log redaction")
PY
