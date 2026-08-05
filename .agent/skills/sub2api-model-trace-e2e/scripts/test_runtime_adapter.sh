#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/runtime_adapter.sh"

fail() {
  printf '[runtime-adapter-test][ERROR] %s\n' "$*" >&2
  exit 1
}

assert_eq() {
  local expected="$1"
  local actual="$2"
  local message="$3"
  [[ "$actual" == "$expected" ]] || fail "$message: expected '$expected', got '$actual'"
}

test_native_runtime_executes_directly() {
  E2E_RUNTIME=native
  resolve_runtime
  probe() { printf '%s\n' "$*"; }

  assert_eq 'native command' "$(runtime_exec probe native command)" 'native runtime must execute directly'
}

test_colima_runtime_selects_profile_and_socket() {
  E2E_RUNTIME=colima
  COLIMA_PROFILE=contract-profile
  DOCKER_HOST_SOCK=/tmp/contract-docker.sock
  DOCKER_CONTEXT=remote-context
  colima() { printf '%s\n' "$*"; }

  resolve_runtime

  assert_eq 'unix:///tmp/contract-docker.sock' "$DOCKER_HOST" 'colima runtime must select its Docker socket'
  [[ -z "${DOCKER_CONTEXT+x}" ]] || fail 'colima runtime must clear an inherited Docker context'
  assert_eq 'ssh --profile contract-profile -- docker ps' "$(runtime_exec docker ps)" 'colima runtime must execute through colima ssh'
}

test_colima_runtime_derives_socket_from_profile() (
  E2E_RUNTIME=colima
  COLIMA_PROFILE=contract-profile
  HOME=/tmp/contract-home
  unset DOCKER_HOST_SOCK DOCKER_HOST
  colima() { printf '%s\n' "$*"; }

  resolve_runtime

  assert_eq 'unix:///tmp/contract-home/.config/colima/contract-profile/docker.sock' "$DOCKER_HOST" \
    'colima runtime must derive its Docker socket from the selected profile'
  assert_eq 'ssh --profile contract-profile -- docker ps' "$(runtime_exec docker ps)" \
    'colima runtime must use the same profile for VM execution'
)

test_invalid_runtime_is_rejected() {
  local output
  if output=$(E2E_RUNTIME=unsupported resolve_runtime 2>&1); then
    fail 'invalid runtime must fail'
  fi
  [[ "$output" == *'E2E_RUNTIME must be native or colima'* ]] || fail 'invalid runtime diagnostic must be actionable'
}

stub_docker_runtime() {
  uname() { printf 'Linux\n'; }
  docker() {
    case "$1 $2" in
      'info --format')
        [[ "${DOCKER_FAIL_PROBE:-}" == info ]] && return 91
        printf '%s\n' "$DOCKER_SERVER_OS"
        ;;
      'version --format')
        [[ "${DOCKER_FAIL_PROBE:-}" == version ]] && return 91
        printf '%s\n' "$DOCKER_SERVER_ARCH"
        ;;
      'context show')
        [[ "${DOCKER_FAIL_PROBE:-}" == context-show ]] && return 91
        printf 'contract-context\n'
        ;;
      'context inspect')
        [[ "${DOCKER_FAIL_PROBE:-}" == context-inspect ]] && return 91
        printf '%s\n' "$DOCKER_ENDPOINT"
        ;;
      *) return 90 ;;
    esac
  }
}

test_docker_architecture_mapping() {
  local output
  E2E_RUNTIME=native
  unset DOCKER_CONTEXT DOCKER_HOST E2E_GOARCH
  DOCKER_SERVER_OS=linux
  DOCKER_ENDPOINT=unix:///var/run/docker.sock
  stub_docker_runtime

  DOCKER_SERVER_ARCH=x86_64
  resolve_arch
  assert_eq amd64 "$E2E_GOARCH" 'x86_64 Docker server must map to GOARCH=amd64'

  unset E2E_GOARCH
  DOCKER_SERVER_ARCH=amd64
  resolve_arch
  assert_eq amd64 "$E2E_GOARCH" 'amd64 Docker server must remain GOARCH=amd64'

  unset E2E_GOARCH
  DOCKER_SERVER_ARCH=aarch64
  resolve_arch
  assert_eq arm64 "$E2E_GOARCH" 'aarch64 Docker server must map to GOARCH=arm64'

  unset E2E_GOARCH
  DOCKER_SERVER_ARCH=arm64
  resolve_arch
  assert_eq arm64 "$E2E_GOARCH" 'arm64 Docker server must remain GOARCH=arm64'

  E2E_GOARCH=arm64
  DOCKER_SERVER_ARCH=amd64
  resolve_arch
  assert_eq amd64 "$E2E_GOARCH" 'Docker server architecture must replace a stale explicit GOARCH'

  E2E_GOARCH=amd64
  DOCKER_SERVER_ARCH=ppc64le
  if output=$(resolve_arch 2>&1); then
    fail 'unsupported Docker server architecture must fail even with an explicit GOARCH'
  fi
  [[ "$output" == *'unsupported Docker server architecture: ppc64le'* ]] \
    || fail 'unsupported architecture diagnostic must name the Docker architecture'
}

test_unsupported_runtime_contract_is_rejected() {
  local output
  E2E_RUNTIME=native
  unset DOCKER_CONTEXT DOCKER_HOST E2E_GOARCH
  DOCKER_SERVER_ARCH=amd64
  DOCKER_ENDPOINT=unix:///var/run/docker.sock
  stub_docker_runtime

  DOCKER_SERVER_OS=windows
  if output=$(resolve_arch 2>&1); then
    fail 'non-Linux Docker server must fail before containers start'
  fi
  [[ "$output" == *'Docker server OS must be linux, got windows'* ]] \
    || fail 'unsupported OS diagnostic must name the Docker server OS'

  DOCKER_SERVER_OS=linux
  DOCKER_ENDPOINT=tcp://docker.example:2376
  if output=$(resolve_arch 2>&1); then
    fail 'remote native Docker endpoint must fail before containers start'
  fi
  [[ "$output" == *'native runtime requires a local unix Docker endpoint'* ]] \
    || fail 'unsupported endpoint diagnostic must explain the local unix requirement'
}

test_native_runtime_validates_explicit_docker_host() (
  local output
  E2E_RUNTIME=native
  unset DOCKER_CONTEXT E2E_GOARCH
  DOCKER_SERVER_OS=linux
  DOCKER_SERVER_ARCH=amd64
  DOCKER_ENDPOINT=unix:///ignored-context.sock
  stub_docker_runtime

  for DOCKER_HOST in tcp://docker.example:2376 ssh://docker.example; do
    if output=$(resolve_arch 2>&1); then
      fail "remote DOCKER_HOST must fail before containers start: $DOCKER_HOST"
    fi
    [[ "$output" == *'native runtime requires a local unix Docker endpoint'* ]] \
      || fail "remote DOCKER_HOST diagnostic must explain the local unix requirement: $DOCKER_HOST"
  done

  DOCKER_HOST=unix:///var/run/docker.sock
  resolve_arch
  assert_eq amd64 "$E2E_GOARCH" 'local Unix DOCKER_HOST must remain supported'
)

test_native_runtime_honors_docker_context_precedence() (
  local output
  E2E_RUNTIME=native
  DOCKER_HOST=unix:///var/run/docker.sock
  DOCKER_CONTEXT=remote-context
  DOCKER_SERVER_OS=linux
  DOCKER_SERVER_ARCH=amd64
  DOCKER_ENDPOINT=tcp://docker.example:2376
  stub_docker_runtime

  if output=$(resolve_arch 2>&1); then
    fail 'a remote DOCKER_CONTEXT must override and reject a local DOCKER_HOST'
  fi
  [[ "$output" == *'native runtime requires a local unix Docker endpoint'* ]] \
    || fail 'Docker context precedence failure must report the effective remote endpoint'
)

test_docker_probe_failures_are_actionable() (
  local output
  E2E_RUNTIME=native
  unset DOCKER_CONTEXT DOCKER_HOST E2E_GOARCH
  DOCKER_SERVER_OS=linux
  DOCKER_SERVER_ARCH=amd64
  DOCKER_ENDPOINT=unix:///var/run/docker.sock
  stub_docker_runtime

  DOCKER_FAIL_PROBE=info
  if output=$(resolve_arch 2>&1); then
    fail 'Docker server OS probe failure must fail'
  fi
  [[ "$output" == *'cannot inspect Docker server OS'* ]] \
    || fail 'Docker server OS probe failure must be actionable'

  for DOCKER_FAIL_PROBE in context-show context-inspect; do
    if output=$(resolve_arch 2>&1); then
      fail "Docker context probe failure must fail: $DOCKER_FAIL_PROBE"
    fi
    [[ "$output" == *'cannot inspect the active Docker endpoint'* ]] \
      || fail "Docker context probe failure must be actionable: $DOCKER_FAIL_PROBE"
  done

  DOCKER_FAIL_PROBE=version
  if output=$(resolve_arch 2>&1); then
    fail 'Docker server architecture probe failure must fail'
  fi
  [[ "$output" == *'cannot inspect Docker server architecture'* ]] \
    || fail 'Docker server architecture probe failure must be actionable'
)

test_native_runtime_requires_linux_host() (
  local output
  E2E_RUNTIME=native
  unset DOCKER_CONTEXT DOCKER_HOST E2E_GOARCH
  DOCKER_SERVER_OS=linux
  DOCKER_SERVER_ARCH=amd64
  DOCKER_ENDPOINT=unix:///var/run/docker.sock
  stub_docker_runtime
  uname() { printf 'Darwin\n'; }

  if output=$(resolve_arch 2>&1); then
    fail 'native runtime on a non-Linux host must fail before containers start'
  fi
  [[ "$output" == *'native runtime requires a Linux host'* ]] \
    || fail 'unsupported host diagnostic must require Linux and identify the host OS'
)

test_teardown_rejects_remote_native_endpoint() (
  local docker_log output temp_dir
  temp_dir="$(mktemp -d)"
  docker_log="$temp_dir/docker.log"
  trap 'rm -rf "$temp_dir"' EXIT
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "%s\\n" "$*" >> "$DOCKER_CALL_LOG"' \
    >"$temp_dir/docker"
  chmod +x "$temp_dir/docker"

  if output=$(PATH="$temp_dir:$PATH" DOCKER_CALL_LOG="$docker_log" \
    E2E_RUNTIME=native DOCKER_HOST=tcp://docker.example:2376 \
    bash "$SCRIPT_DIR/teardown.sh" 2>&1); then
    fail 'teardown must reject a remote native Docker endpoint'
  fi
  [[ "$output" == *'native runtime requires a local unix Docker endpoint'* ]] \
    || fail 'teardown remote endpoint diagnostic must explain the local unix requirement'
  [[ ! -s "$docker_log" ]] || fail 'teardown must not delete resources through a remote Docker endpoint'
)

test_teardown_rejects_non_linux_native_host() (
  local docker_log output temp_dir
  temp_dir="$(mktemp -d)"
  docker_log="$temp_dir/docker.log"
  trap 'rm -rf "$temp_dir"' EXIT
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "%s\n" "$*" >> "$DOCKER_CALL_LOG"' \
    >"$temp_dir/docker"
  printf '%s\n' '#!/usr/bin/env bash' 'printf "Darwin\n"' >"$temp_dir/uname"
  chmod +x "$temp_dir/docker" "$temp_dir/uname"

  if output=$(PATH="$temp_dir:$PATH" DOCKER_CALL_LOG="$docker_log" \
    E2E_RUNTIME=native DOCKER_HOST=unix:///var/run/docker.sock \
    bash "$SCRIPT_DIR/teardown.sh" 2>&1); then
    fail 'teardown must reject native mode on a non-Linux host'
  fi
  [[ "$output" == *'native runtime requires a Linux host'* ]] \
    || fail 'teardown non-Linux diagnostic must require Linux'
  [[ ! -s "$docker_log" ]] || fail 'teardown must not delete resources from a non-Linux native host'
)

test_e2e_owner_is_checkout_scoped() (
  local first_owner second_owner
  REPO_ROOT=/tmp/sub2api-checkout-a
  resolve_e2e_owner
  first_owner="$E2E_OWNER_ID"
  [[ "$first_owner" == repo-* ]] || fail 'E2E owner must use the repo checksum namespace'
  [[ "$E2E_LANGFUSE_PROJECT" == sub2api-modeltrace-e2e-langfuse-* ]] \
    || fail 'Langfuse Compose project must be E2E-specific'
  [[ "$E2E_DEPS_PROJECT" == sub2api-modeltrace-e2e-deps-* ]] \
    || fail 'dependency Compose project must be E2E-specific'

  REPO_ROOT=/tmp/sub2api-checkout-b
  resolve_e2e_owner
  second_owner="$E2E_OWNER_ID"
  [[ "$first_owner" != "$second_owner" ]] || fail 'different checkouts must not share an E2E owner label'
)

test_resource_owner_assertion_rejects_mismatch() (
  local output
  E2E_OWNER_ID=repo-current
  docker() { printf 'repo-other\n'; }

  if output=$(assert_e2e_resource_owned container foreign-container 2>&1); then
    fail 'resource ownership assertion must reject a mismatched owner label'
  fi
  [[ "$output" == *'container foreign-container is not owned by repo-current'* ]] \
    || fail 'resource ownership mismatch must identify the resource and expected owner'

  docker() { printf 'repo-current\n'; }
  assert_e2e_resource_owned volume owned-volume
  assert_e2e_resource_owned network owned-network
)

test_teardown_cleans_only_owned_runtime_resources() (
  local docker_calls docker_log output repo_root temp_dir
  temp_dir="$(mktemp -d)"
  docker_log="$temp_dir/docker.log"
  trap 'rm -rf "$temp_dir"' EXIT
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "%s\n" "$*" >> "$DOCKER_CALL_LOG"' \
    'case "$*" in' \
    '  "ps -aq --filter label=io.sub2api.modeltrace-e2e.owner=repo-"*) printf "owned-container-a\nowned-container-b\n" ;;' \
    '  "volume ls -q --filter label=io.sub2api.modeltrace-e2e.owner=repo-"*) printf "owned-volume\n" ;;' \
    '  "network ls -q --filter label=io.sub2api.modeltrace-e2e.owner=repo-"*) printf "owned-network\n" ;;' \
    'esac' \
    >"$temp_dir/docker"
  chmod +x "$temp_dir/docker"
  printf '%s\n' '#!/usr/bin/env bash' 'printf "Linux\n"' >"$temp_dir/uname"
  chmod +x "$temp_dir/uname"

  output=$(PATH="$temp_dir:$PATH" DOCKER_CALL_LOG="$docker_log" \
    E2E_RUNTIME=native DOCKER_HOST=unix:///var/run/docker.sock \
    bash "$SCRIPT_DIR/teardown.sh" 2>&1)
  docker_calls="$(<"$docker_log")"
  [[ "$docker_calls" == *'ps -aq --filter label=io.sub2api.modeltrace-e2e.owner=repo-'* ]] \
    || fail 'teardown must select containers by the current checkout owner label'
  [[ "$docker_calls" == *'rm -f owned-container-a'* && "$docker_calls" == *'rm -f owned-container-b'* ]] \
    || fail 'teardown must remove containers returned by the owner label filter'
  [[ "$docker_calls" == *'volume rm -f owned-volume'* ]] \
    || fail 'teardown must remove volumes returned by the owner label filter'
  [[ "$docker_calls" == *'network rm owned-network'* ]] \
    || fail 'teardown must remove networks returned by the owner label filter'
  [[ "$docker_calls" != *'rm -f sub2api-e2e sub2api-e2e-upstream'* ]] \
    || fail 'teardown must not delete unlabelled legacy names without explicit opt-in'
  repo_root="$(cd "$SCRIPT_DIR/../../../.." && pwd)"
  [[ "$output" == *"binary still at $repo_root/.e2e-bin/sub2api"* ]] \
    || fail 'teardown must report the repository-level retained binary path'
)

test_legacy_cleanup_requires_explicit_opt_in() (
  local docker_calls docker_log temp_dir
  temp_dir="$(mktemp -d)"
  docker_log="$temp_dir/docker.log"
  trap 'rm -rf "$temp_dir"' EXIT
  docker() { printf '%s\n' "$*" >>"$docker_log"; }

  E2E_CLEAN_LEGACY=0
  remove_legacy_e2e_resources
  [[ ! -s "$docker_log" ]] || fail 'legacy cleanup must be a no-op without explicit opt-in'

  E2E_CLEAN_LEGACY=1
  remove_legacy_e2e_resources
  docker_calls="$(<"$docker_log")"
  [[ "$docker_calls" == *'rm -f sub2api-e2e sub2api-e2e-upstream'* ]] \
    || fail 'legacy cleanup opt-in must remove the old fixed container names'
  [[ "$docker_calls" == *'volume rm -f sub2api-e2e-data'* ]] \
    || fail 'legacy cleanup opt-in must remove the old fixed volume names'
)

test_full_e2e_preflight_rejects_invalid_inputs() (
  local command_name docker_log heavy_log output temp_dir
  temp_dir="$(mktemp -d)"
  docker_log="$temp_dir/docker.log"
  heavy_log="$temp_dir/heavy.log"
  trap 'rm -rf "$temp_dir"' EXIT
  printf '%s\n' \
    '#!/usr/bin/env bash' \
    'printf "%s\n" "$*" >> "$DOCKER_CALL_LOG"' \
    'case "${1:-} ${2:-}" in' \
    '  "compose version") exit 0 ;;' \
    '  "info --format") printf "linux\n" ;;' \
    '  "version --format") printf "%s\n" "${DOCKER_SERVER_ARCH:-amd64}" ;;' \
    '  *) exit 90 ;;' \
    'esac' \
    >"$temp_dir/docker"
  printf '%s\n' '#!/usr/bin/env bash' 'printf "Linux\n"' >"$temp_dir/uname"
  for command_name in go make pnpm; do
    printf '%s\n' \
      '#!/usr/bin/env bash' \
      'printf "%s %s\n" "$0" "$*" >> "$HEAVY_CALL_LOG"' \
      'exit 97' \
      >"$temp_dir/$command_name"
  done
  chmod +x "$temp_dir/docker" "$temp_dir/uname" "$temp_dir/go" "$temp_dir/make" "$temp_dir/pnpm"

  if output=$(PATH="$temp_dir:/usr/bin:/bin" DOCKER_CALL_LOG="$docker_log" HEAVY_CALL_LOG="$heavy_log" \
    E2E_RUNTIME=unsupported bash "$SCRIPT_DIR/run_full_e2e.sh" 2>&1); then
    fail 'full E2E must reject an invalid runtime during preflight'
  fi
  [[ "$output" == *'E2E_RUNTIME must be native or colima'* ]] \
    || fail 'full E2E invalid runtime diagnostic must come from the runtime preflight'
  [[ "$output" != *'phase 1/9'* && ! -s "$heavy_log" ]] \
    || fail 'full E2E invalid runtime must fail before phase 1 and heavy commands'

  : >"$docker_log"
  : >"$heavy_log"
  if output=$(PATH="$temp_dir:/usr/bin:/bin" DOCKER_CALL_LOG="$docker_log" HEAVY_CALL_LOG="$heavy_log" \
    E2E_RUNTIME=native DOCKER_HOST=tcp://docker.example:2376 DOCKER_SERVER_ARCH=amd64 \
    bash "$SCRIPT_DIR/run_full_e2e.sh" 2>&1); then
    fail 'full E2E must reject a remote native endpoint during preflight'
  fi
  [[ "$output" == *'native runtime requires a local unix Docker endpoint'* ]] \
    || fail 'full E2E remote endpoint diagnostic must come from the runtime preflight'
  [[ "$output" != *'phase 1/9'* && ! -s "$heavy_log" ]] \
    || fail 'full E2E remote endpoint must fail before phase 1 and heavy commands'

  : >"$docker_log"
  : >"$heavy_log"
  if output=$(PATH="$temp_dir:/usr/bin:/bin" DOCKER_CALL_LOG="$docker_log" HEAVY_CALL_LOG="$heavy_log" \
    E2E_RUNTIME=native DOCKER_HOST=unix:///var/run/docker.sock DOCKER_SERVER_ARCH=ppc64le \
    bash "$SCRIPT_DIR/run_full_e2e.sh" 2>&1); then
    fail 'full E2E must reject an unsupported Docker architecture during preflight'
  fi
  [[ "$output" == *'unsupported Docker server architecture: ppc64le'* ]] \
    || fail 'full E2E unsupported architecture diagnostic must come from the runtime preflight'
  [[ "$output" != *'phase 1/9'* && ! -s "$heavy_log" ]] \
    || fail 'full E2E unsupported architecture must fail before phase 1 and heavy commands'
)

test_compose_adapter_rejects_missing_implementations() (
  local output
  PATH=/nonexistent
  docker() { return 1; }

  if output=$(resolve_compose 2>&1); then
    fail 'missing Compose v2 and legacy Compose must fail'
  fi
  [[ "$output" == *'missing Docker Compose: install docker compose or docker-compose'* ]] \
    || fail 'missing Compose diagnostic must name both supported implementations'
)

test_compose_adapter_prefers_plugin_and_supports_legacy() {
  docker() {
    if [[ "$1 $2" == 'compose version' ]]; then
      return 0
    fi
    printf 'plugin:%s\n' "$*"
  }
  docker-compose() { printf 'legacy:%s\n' "$*"; }

  resolve_compose
  assert_eq 'plugin:compose up -d' "$(compose up -d)" 'Compose v2 plugin must be preferred'

  docker() { return 1; }
  resolve_compose
  assert_eq 'legacy:up -d' "$(compose up -d)" 'legacy docker-compose must remain supported'
}

test_native_runtime_executes_directly
test_colima_runtime_selects_profile_and_socket
test_colima_runtime_derives_socket_from_profile
test_invalid_runtime_is_rejected
test_docker_architecture_mapping
test_unsupported_runtime_contract_is_rejected
test_native_runtime_validates_explicit_docker_host
test_native_runtime_honors_docker_context_precedence
test_docker_probe_failures_are_actionable
test_native_runtime_requires_linux_host
test_teardown_rejects_remote_native_endpoint
test_teardown_rejects_non_linux_native_host
test_e2e_owner_is_checkout_scoped
test_resource_owner_assertion_rejects_mismatch
test_teardown_cleans_only_owned_runtime_resources
test_legacy_cleanup_requires_explicit_opt_in
test_full_e2e_preflight_rejects_invalid_inputs
test_compose_adapter_rejects_missing_implementations
test_compose_adapter_prefers_plugin_and_supports_legacy

printf 'runtime adapter contracts passed\n'