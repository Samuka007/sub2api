# runtime_adapter.sh — 模型追踪 E2E harness 的运行时、架构与资源归属库。
#
# 由 run_e2e.sh source；test_runtime_adapter.sh 用 docker/colima 命令桩 source 同一文件，
# 验证每个 runtime 分支选择自己的 adapter、架构与清理边界（轻量契约测试）。
#
# 清理函数只删除带当前 checkout owner label 的资源；旧版无标签资源必须显式 opt-in。

# 选择 runtime adapter：定义 runtime_exec（命令执行器）与可用时的 DOCKER_HOST。
# native   —— 直接执行宿主命令（默认，尊重当前 Docker context）。
# colima   —— 显式选择 Colima profile：设置其 docker.sock，并把 VM 内命令经 colima ssh 执行。
# 非法值   —— 打印可操作错误并返回非零。
resolve_runtime() {
  case "${E2E_RUNTIME:-native}" in
    native)
      runtime_exec() { "$@"; }
      ;;
    colima)
      unset DOCKER_CONTEXT
      export DOCKER_HOST="unix://${DOCKER_HOST_SOCK:-$HOME/.config/colima/${COLIMA_PROFILE:-swebench}/docker.sock}"
      runtime_exec() { colima ssh --profile "${COLIMA_PROFILE:-swebench}" -- "$@"; }
      ;;
    *)
      printf '[e2e][ERROR] E2E_RUNTIME must be native or colima, got %s\n' "$E2E_RUNTIME" >&2
      return 2
      ;;
  esac
}

# native 模式只允许本地 Unix Docker endpoint；清理与启动前共用此安全门禁。
validate_native_docker_endpoint() {
  local current_context docker_endpoint

  [[ "${E2E_RUNTIME:-native}" == native ]] || return 0
  if [[ -n "${DOCKER_CONTEXT:-}" ]]; then
    current_context="$DOCKER_CONTEXT"
    if ! docker_endpoint="$(docker context inspect "$current_context" --format '{{.Endpoints.docker.Host}}' 2>/dev/null)"; then
      printf '[e2e][ERROR] cannot inspect the active Docker endpoint\n' >&2
      return 2
    fi
  elif [[ -n "${DOCKER_HOST:-}" ]]; then
    docker_endpoint="$DOCKER_HOST"
  elif ! current_context="$(docker context show 2>/dev/null)" \
    || ! docker_endpoint="$(docker context inspect "$current_context" --format '{{.Endpoints.docker.Host}}' 2>/dev/null)"; then
    printf '[e2e][ERROR] cannot inspect the active Docker endpoint\n' >&2
    return 2
  fi
  case "$docker_endpoint" in
    unix://*) ;;
    *)
      printf '[e2e][ERROR] native runtime requires a local unix Docker endpoint, got %s\n' "$docker_endpoint" >&2
      return 2
      ;;
  esac
}

# native 模式只允许 Linux 宿主上的本地 Unix Docker endpoint；启动与清理共用。
validate_native_runtime() {
  local host_os

  [[ "${E2E_RUNTIME:-native}" == native ]] || return 0
  validate_native_docker_endpoint || return
  if ! host_os="$(uname -s 2>/dev/null)"; then
    printf '[e2e][ERROR] cannot inspect the host OS\n' >&2
    return 2
  fi
  if [[ "$host_os" != Linux ]]; then
    printf '[e2e][ERROR] native runtime requires a Linux host, got %s; use E2E_RUNTIME=colima on macOS\n' "$host_os" >&2
    return 2
  fi
}

# 解析 Compose 命令：优先 docker compose，回退 legacy docker-compose。
# 成功时定义 compose()；两种都不可用时已打印错误并返回非零。
resolve_compose() {
  if docker compose version >/dev/null 2>&1; then
    compose() { docker compose "$@"; }
  elif command -v docker-compose >/dev/null 2>&1; then
    compose() { docker-compose "$@"; }
  else
    printf '[e2e][ERROR] missing Docker Compose: install docker compose or docker-compose' >&2
    return 2
  fi
}

# 由 Docker server 架构推导 Go 交叉编译架构，不接受独立 override。
# 成功时设置 E2E_GOARCH；不支持的 Docker server 架构已打印错误并返回非零。
resolve_arch() {
  local docker_server_os docker_arch
  validate_native_runtime || return

  if ! docker_server_os="$(docker info --format '{{.OSType}}' 2>/dev/null)"; then
    printf '[e2e][ERROR] cannot inspect Docker server OS; verify the active Docker context\n' >&2
    return 2
  fi
  if [[ "$docker_server_os" != linux ]]; then
    printf '[e2e][ERROR] Docker server OS must be linux, got %s\n' "$docker_server_os" >&2
    return 2
  fi


  if ! docker_arch="$(docker version --format '{{.Server.Arch}}' 2>/dev/null)"; then
    printf '[e2e][ERROR] cannot inspect Docker server architecture\n' >&2
    return 2
  fi
  case "$docker_arch" in
    amd64|x86_64) E2E_GOARCH=amd64 ;;
    arm64|aarch64) E2E_GOARCH=arm64 ;;
    *)
      printf '[e2e][ERROR] unsupported Docker server architecture: %s\n' "$docker_arch" >&2
      return 2
      ;;
  esac
}

E2E_OWNER_LABEL_KEY='io.sub2api.modeltrace-e2e.owner'

# 为每个 checkout 派生稳定 owner label 和唯一 Compose project name。
resolve_e2e_owner() {
  local owner_checksum
  if [[ -z "${REPO_ROOT:-}" ]]; then
    printf '[e2e][ERROR] REPO_ROOT is required to resolve E2E resource ownership\n' >&2
    return 2
  fi
  if ! owner_checksum="$(printf '%s' "$REPO_ROOT" | cksum)"; then
    printf '[e2e][ERROR] cannot derive E2E resource owner from repository root\n' >&2
    return 2
  fi
  owner_checksum="${owner_checksum%% *}"
  case "$owner_checksum" in
    ''|*[!0-9]*)
      printf '[e2e][ERROR] invalid E2E resource owner checksum: %s\n' "$owner_checksum" >&2
      return 2
      ;;
  esac

  E2E_OWNER_ID="repo-$owner_checksum"
  E2E_LANGFUSE_PROJECT="sub2api-modeltrace-e2e-langfuse-$owner_checksum"
  E2E_DEPS_PROJECT="sub2api-modeltrace-e2e-deps-$owner_checksum"
  export E2E_OWNER_ID E2E_LANGFUSE_PROJECT E2E_DEPS_PROJECT
}

# 验证已创建资源的 owner label，防止配置漂移导致 teardown 遗漏或越界。
assert_e2e_resource_owned() {
  local actual_owner resource_name resource_type
  resource_type="$1"
  resource_name="$2"
  case "$resource_type" in
    container)
      actual_owner="$(docker inspect --format "{{ index .Config.Labels \"$E2E_OWNER_LABEL_KEY\" }}" "$resource_name" 2>/dev/null)" || actual_owner=''
      ;;
    volume)
      actual_owner="$(docker volume inspect --format "{{ index .Labels \"$E2E_OWNER_LABEL_KEY\" }}" "$resource_name" 2>/dev/null)" || actual_owner=''
      ;;
    network)
      actual_owner="$(docker network inspect --format "{{ index .Labels \"$E2E_OWNER_LABEL_KEY\" }}" "$resource_name" 2>/dev/null)" || actual_owner=''
      ;;
    *)
      printf '[e2e][ERROR] unsupported E2E resource type: %s\n' "$resource_type" >&2
      return 2
      ;;
  esac
  if [[ "$actual_owner" != "$E2E_OWNER_ID" ]]; then
    printf '[e2e][ERROR] %s %s is not owned by %s\n' "$resource_type" "$resource_name" "$E2E_OWNER_ID" >&2
    return 2
  fi
}

# 只删除当前 checkout 用 owner label 创建的容器、卷和网络。
remove_owned_e2e_resources() {
  local id ids owner_filter
  if [[ -z "${E2E_OWNER_ID:-}" ]]; then
    printf '[e2e][ERROR] E2E_OWNER_ID is required before cleanup\n' >&2
    return 2
  fi
  owner_filter="$E2E_OWNER_LABEL_KEY=$E2E_OWNER_ID"

  if ! ids="$(docker ps -aq --filter "label=$owner_filter")"; then
    printf '[e2e][ERROR] cannot list E2E-owned containers\n' >&2
    return 2
  fi
  for id in $ids; do
    docker rm -f "$id" >/dev/null
  done

  if ! ids="$(docker volume ls -q --filter "label=$owner_filter")"; then
    printf '[e2e][ERROR] cannot list E2E-owned volumes\n' >&2
    return 2
  fi
  for id in $ids; do
    docker volume rm -f "$id" >/dev/null
  done

  if ! ids="$(docker network ls -q --filter "label=$owner_filter")"; then
    printf '[e2e][ERROR] cannot list E2E-owned networks\n' >&2
    return 2
  fi
  for id in $ids; do
    docker network rm "$id" >/dev/null
  done
}

# 迁移旧版无 owner label 的固定名称资源。只有调用者显式设置 E2E_CLEAN_LEGACY=1 才执行。
remove_legacy_e2e_resources() {
  [[ "${E2E_CLEAN_LEGACY:-0}" == 1 ]] || return 0
  printf '[e2e][WARN] E2E_CLEAN_LEGACY=1: removing unlabelled legacy E2E resource names\n' >&2
  docker rm -f \
    sub2api-e2e sub2api-e2e-upstream \
    sub2api-deps-postgres-1 sub2api-deps-redis-1 \
    sub2api-langfuse-langfuse-web-1 sub2api-langfuse-langfuse-worker-1 \
    sub2api-langfuse-postgres-1 sub2api-langfuse-redis-1 \
    sub2api-langfuse-clickhouse-1 sub2api-langfuse-minio-1 \
    langfuse-langfuse-web-1 langfuse-langfuse-worker-1 \
    langfuse-postgres-1 langfuse-redis-1 langfuse-clickhouse-1 langfuse-minio-1 \
    >/dev/null 2>&1 || true
  docker volume rm -f \
    sub2api-e2e-data \
    langfuse_langfuse_postgres_data langfuse_langfuse_clickhouse_data langfuse_langfuse_clickhouse_logs langfuse_langfuse_minio_data langfuse_langfuse_redis_data \
    langfuse_postgres_data langfuse_clickhouse_data langfuse_clickhouse_logs langfuse_minio_data langfuse_redis_data \
    sub2api-langfuse_postgres_data sub2api-langfuse_clickhouse_data sub2api-langfuse_clickhouse_logs sub2api-langfuse_minio_data sub2api-langfuse_redis_data \
    >/dev/null 2>&1 || true
  docker network rm langfuse_default deps_default sub2api-langfuse_default sub2api-deps_default \
    >/dev/null 2>&1 || true
}