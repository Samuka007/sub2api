#!/usr/bin/env bash
# scripts/run_e2e.sh — 启动 Langfuse + sub2api deps + 编译并启动 sub2api，发测试请求，验证 trace 落库。
#
# 使用：
#   bash .agent/skills/sub2api-model-trace-e2e/scripts/run_e2e.sh
#
# 前置：
#   - Colima profile `swebench` 已启动
#   - 当前位于 sub2api 仓库根目录
#
# 成功：exit 0，stdout 最后三行为 trace_id、observation_count、VERIFY_OK
# 失败：exit != 0，stderr 含 ERROR 行
set -euo pipefail

SKILL_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$SKILL_DIR/../../.." && pwd)"

COLIMA_PROFILE="${COLIMA_PROFILE:-swebench}"
DOCKER_HOST_SOCK="${DOCKER_HOST_SOCK:-$HOME/.config/colima/${COLIMA_PROFILE}/docker.sock}"
export DOCKER_HOST="unix://$DOCKER_HOST_SOCK"

LANGFUSE_DIR="${LANGFUSE_DIR:-$REPO_ROOT/.e2e-tmp/langfuse}"
DEPS_DIR="${DEPS_DIR:-$REPO_ROOT/.e2e-tmp/deps}"
BIN_DIR="${BIN_DIR:-$REPO_ROOT/.e2e-bin}"
GEMINI_TLS_DIR="$REPO_ROOT/.e2e-tmp/gemini-tls"
SUB2API_PORT="${SUB2API_PORT:-18080}"
ADMIN_EMAIL="${ADMIN_EMAIL:-admin@e2e.local}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin12345}"
LANGFUSE_URL="${LANGFUSE_URL:-http://127.0.0.1:3000}"
LANGFUSE_TRACE_ENDPOINT="${LANGFUSE_TRACE_ENDPOINT:-$LANGFUSE_URL/api/public/otel/v1/traces}"
LANGFUSE_PK="${LANGFUSE_PK:-pk-lf-local}"
LANGFUSE_SK="${LANGFUSE_SK:-sk-lf-local}"
TOTP_ENCRYPTION_KEY="${TOTP_ENCRYPTION_KEY:-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef}"
DEFAULT_PROMPT_MAX_BYTES=16777216
DEFAULT_RESPONSE_MAX_BYTES=8388608
DEFAULT_MEDIA_MAX_BYTES=16777216

log() { printf '[e2e] %s\n' "$*" >&2; }
fail() { printf '[e2e][ERROR] %s\n' "$*" >&2; exit 1; }
BATCH_ITEM_COUNT="${BATCH_ITEM_COUNT:-200}"
[[ "$BATCH_ITEM_COUNT" =~ ^[0-9]+$ ]] && (( BATCH_ITEM_COUNT >= 2 && BATCH_ITEM_COUNT <= 200 )) \
  || fail "BATCH_ITEM_COUNT must be an integer in [2,200], got $BATCH_ITEM_COUNT"
EXPECTED_BATCH_FAILED_ITEMS=$((BATCH_ITEM_COUNT - 1))


clickhouse_query() {
  curl -fsS --user "${CLICKHOUSE_USER:-clickhouse}:${CLICKHOUSE_PASSWORD:-clickhouse}" \
    --data-binary "$1" "${CLICKHOUSE_READ_PROXY_URL:-http://127.0.0.1:18123/}"
}

need_cmd() { command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"; }
need_cmd docker
need_cmd docker-compose
need_cmd colima
need_cmd nc
need_cmd curl
need_cmd jq
need_cmd openssl
need_cmd python3
need_cmd go

# 0. 前置检查
[[ -f "$REPO_ROOT/backend/cmd/server/main.go" ]] || fail "must run from sub2api repo root, got $REPO_ROOT"
colima status "$COLIMA_PROFILE" >/dev/null 2>&1 || fail "colima profile $COLIMA_PROFILE not running; run: colima start $COLIMA_PROFILE"

# 0.1 先清理可能残留的 e2e 容器和卷（幂等，不报错）
log "cleaning up stale e2e containers"
docker rm -f \
  sub2api-e2e \
  sub2api-e2e-upstream \
  sub2api-deps-postgres-1 sub2api-deps-redis-1 \
  sub2api-langfuse-langfuse-web-1 sub2api-langfuse-langfuse-worker-1 \
  sub2api-langfuse-postgres-1 sub2api-langfuse-redis-1 \
  sub2api-langfuse-clickhouse-1 langfuse-clickhouse-read-proxy-1 sub2api-langfuse-minio-1 \
  >/dev/null 2>&1 || true
docker volume rm -f sub2api-e2e-data \
  langfuse_langfuse_postgres_data langfuse_langfuse_clickhouse_data langfuse_langfuse_clickhouse_logs langfuse_langfuse_minio_data langfuse_langfuse_redis_data \
  sub2api-langfuse_postgres_data sub2api-langfuse_clickhouse_data sub2api-langfuse_clickhouse_logs sub2api-langfuse_minio_data sub2api-langfuse_redis_data \
  >/dev/null 2>&1 || true

# 0.2 端口占用检查
for p in 3000 15432 16379 18081 18123 8080 5432 6379; do
  if nc -z 127.0.0.1 "$p" 2>/dev/null; then
    fail "port $p is occupied after e2e-owned cleanup; identify owner with: lsof -i :$p"
  fi
done
mkdir -p "$LANGFUSE_DIR" "$DEPS_DIR" "$BIN_DIR"
cp "$SKILL_DIR/assets/langfuse-compose.yml" "$LANGFUSE_DIR/docker-compose.yml"
cp "$SKILL_DIR/assets/clickhouse-read-proxy.conf" "$LANGFUSE_DIR/clickhouse-read-proxy.conf"
( cd "$LANGFUSE_DIR" && docker-compose up -d )
CLICKHOUSE_PROXY_HEALTH=""
for i in {1..60}; do
  CLICKHOUSE_PROXY_HEALTH=$(clickhouse_query "SELECT 1" 2>/dev/null || true)
  [[ "$CLICKHOUSE_PROXY_HEALTH" == "1" ]] && break
  sleep 1
done
[[ "$CLICKHOUSE_PROXY_HEALTH" == "1" ]] || fail "ClickHouse read proxy health check failed"
for i in {1..60}; do
  code=$(curl -s -o /dev/null -w '%{http_code}' "$LANGFUSE_URL/api/public/health" || true)
  [[ "$code" == "200" ]] && break
  sleep 2
done
[[ "$code" == "200" ]] || fail "langfuse health check failed (last=$code)"
LANGFUSE_HEALTH=$(curl -fsS "$LANGFUSE_URL/api/public/health")
LANGFUSE_VERSION=$(echo "$LANGFUSE_HEALTH" | jq -er '.version')
if [[ "$LANGFUSE_VERSION" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+) ]]; then
  LANGFUSE_VERSION_MAJOR=${BASH_REMATCH[1]}
  LANGFUSE_VERSION_MINOR=${BASH_REMATCH[2]}
  LANGFUSE_VERSION_PATCH=${BASH_REMATCH[3]}
else
  fail "langfuse health returned an unparseable version"
fi
if (( LANGFUSE_VERSION_MAJOR < 3 || (LANGFUSE_VERSION_MAJOR == 3 && LANGFUSE_VERSION_MINOR < 22) )); then
  fail "langfuse version $LANGFUSE_VERSION is below required 3.22.0"
fi
LANGFUSE_IMAGE_ID=$(docker inspect --format '{{.Image}}' sub2api-langfuse-langfuse-web-1)
LANGFUSE_IMAGE_DIGEST=$(docker image inspect --format '{{index .RepoDigests 0}}' "$LANGFUSE_IMAGE_ID")
LANGFUSE_IMAGE_REVISION=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$LANGFUSE_IMAGE_ID")
LANGFUSE_IMAGE_VERSION=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.version"}}' "$LANGFUSE_IMAGE_ID")
[[ "$LANGFUSE_IMAGE_DIGEST" == *@sha256:* ]] || fail "langfuse image digest is unavailable"
[[ -n "$LANGFUSE_IMAGE_REVISION" && "$LANGFUSE_IMAGE_REVISION" != "<no value>" ]] || fail "langfuse image revision is unavailable"
[[ "$LANGFUSE_IMAGE_VERSION" == "$LANGFUSE_VERSION" ]] || fail "langfuse health/image version mismatch: health=$LANGFUSE_VERSION image=$LANGFUSE_IMAGE_VERSION"
log "langfuse version=$LANGFUSE_VERSION revision=$LANGFUSE_IMAGE_REVISION digest=$LANGFUSE_IMAGE_DIGEST"

# 2. 起 sub2api deps
log "starting sub2api deps (pg+redis)"
cp "$SKILL_DIR/assets/sub2api-deps-compose.yml" "$DEPS_DIR/docker-compose.yml"
( cd "$DEPS_DIR" && docker-compose up -d )
for i in {1..30}; do
  pg_ok=$(docker exec sub2api-deps-postgres-1 pg_isready -U sub2api 2>/dev/null || true)
  rd_ok=$(docker exec sub2api-deps-redis-1 redis-cli ping 2>/dev/null || true)
  [[ "$pg_ok" == *"accepting"* && "$rd_ok" == "PONG" ]] && break
  sleep 1
done
[[ "$pg_ok" == *"accepting"* ]] || fail "sub2api postgres not ready"
[[ "$rd_ok" == "PONG" ]] || fail "sub2api redis not ready"

GEMINI_BATCH_API_KEY="e2e-gemini-$(openssl rand -hex 16)"
BATCH_MEDIA_CANARY="e2e-batch-media-$(openssl rand -hex 16)"
BATCH_MEDIA_CANARY_B64=$(printf '%s' "$BATCH_MEDIA_CANARY" | openssl base64 -A)
rm -rf "$GEMINI_TLS_DIR"
mkdir -p "$GEMINI_TLS_DIR"
printf '%s' "$GEMINI_BATCH_API_KEY" >"$GEMINI_TLS_DIR/gemini-api-key"
chmod 600 "$GEMINI_TLS_DIR/gemini-api-key"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj '/CN=sub2api-e2e-ca' \
  -keyout "$GEMINI_TLS_DIR/ca.key" -out "$GEMINI_TLS_DIR/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes \
  -subj '/CN=generativelanguage.googleapis.com' \
  -keyout "$GEMINI_TLS_DIR/server.key" -out "$GEMINI_TLS_DIR/server.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:generativelanguage.googleapis.com\nextendedKeyUsage=serverAuth\n' >"$GEMINI_TLS_DIR/server.ext"
openssl x509 -req -days 1 -sha256 \
  -in "$GEMINI_TLS_DIR/server.csr" -CA "$GEMINI_TLS_DIR/ca.crt" -CAkey "$GEMINI_TLS_DIR/ca.key" -CAcreateserial \
  -extfile "$GEMINI_TLS_DIR/server.ext" -out "$GEMINI_TLS_DIR/server.crt" >/dev/null 2>&1
openssl verify -CAfile "$GEMINI_TLS_DIR/ca.crt" "$GEMINI_TLS_DIR/server.crt" >/dev/null \
  || fail "local Gemini TLS certificate verification failed"
# 2.1 启动确定性 Anthropic 上游：低 priority 账号固定 429，高 priority 账号固定成功 SSE。
log "starting deterministic Anthropic failover fixture"
colima ssh --profile "$COLIMA_PROFILE" -- docker run -d --name sub2api-e2e-upstream \
  --network host \
  -e E2E_GEMINI_API_KEY_FILE=/tls/gemini-api-key \
  -e E2E_BATCH_MEDIA_CANARY="$BATCH_MEDIA_CANARY" \
  -e E2E_TLS_CERT_FILE=/tls/server.crt \
  -e E2E_TLS_KEY_FILE=/tls/server.key \
  -v "$GEMINI_TLS_DIR:/tls:ro" \
  -v "$SKILL_DIR/assets/upstream-fixture.go:/app/upstream-fixture.go:ro" \
  -w /app \
  golang:1.26.5 \
  go run ./upstream-fixture.go >/dev/null
for i in {1..30}; do
  fixture_code=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:18081/health || true)
  [[ "$fixture_code" == "200" ]] && break
  sleep 1
done
[[ "$fixture_code" == "200" ]] || { docker logs sub2api-e2e-upstream >&2; fail "Anthropic failover fixture not ready"; }
TLS_FIXTURE_CODE=$(curl --noproxy '*' -sS --cacert "$GEMINI_TLS_DIR/ca.crt" \
  --resolve generativelanguage.googleapis.com:443:127.0.0.1 \
  -o /dev/null -w '%{http_code}' https://generativelanguage.googleapis.com/health || true)
[[ "$TLS_FIXTURE_CODE" == "200" ]] || fail "Gemini TLS fixture verification failed (HTTP $TLS_FIXTURE_CODE)"

# 3. 编译 sub2api linux/arm64（带 embed tag）
log "compiling sub2api and E2E client binaries"
rm -rf "$BIN_DIR/sub2api"
colima ssh --profile "$COLIMA_PROFILE" -- docker run --rm \
  -e GOPROXY=https://goproxy.cn,direct \
  -v sub2api-go-mod-cache:/go/pkg/mod \
  -v sub2api-go-build-cache:/root/.cache/go-build \
  -v "$REPO_ROOT/backend:/src:ro" \
  -v "$BIN_DIR:/out" \
  -w /src \
  golang:1.26.5 \
  sh -c 'CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags embed -ldflags="-s -w -X main.Version=e2e-test" -o /out/sub2api ./cmd/server'
[[ -x "$BIN_DIR/sub2api" ]] || fail "binary not produced at $BIN_DIR/sub2api"
RAW_RST_CLIENT_BIN="$BIN_DIR/raw-http-rst-client"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$RAW_RST_CLIENT_BIN" "$SKILL_DIR/scripts/raw_http_rst_client.go"
[[ -x "$RAW_RST_CLIENT_BIN" ]] || fail "raw TCP RST client was not produced at $RAW_RST_CLIENT_BIN"
RESPONSES_WS_CLIENT_BIN="$BIN_DIR/responses-ws-client"
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$RESPONSES_WS_CLIENT_BIN" "$SKILL_DIR/scripts/responses_ws_client.go"
[[ -x "$RESPONSES_WS_CLIENT_BIN" ]] || fail "Responses WebSocket client was not produced at $RESPONSES_WS_CLIENT_BIN"

# 4. 启动 sub2api（AUTO_SETUP，--network host 共享 VM 127.0.0.1）
log "starting sub2api server"
docker rm -f sub2api-e2e >/dev/null 2>&1 || true
# 清空 DB 让 AUTO_SETUP 重建（幂等）
docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;" >/dev/null 2>&1 || true
docker volume rm sub2api-e2e-data >/dev/null 2>&1 || true
docker volume create sub2api-e2e-data >/dev/null

colima ssh --profile "$COLIMA_PROFILE" -- docker run -d --name sub2api-e2e \
  --network host \
  -e DATA_DIR=/data \
  -e AUTO_SETUP=true \
  -e ADMIN_EMAIL="$ADMIN_EMAIL" \
  -e ADMIN_PASSWORD="$ADMIN_PASSWORD" \
  -e DATABASE_HOST=127.0.0.1 \
  -e DATABASE_PORT=15432 \
  -e DATABASE_USER=sub2api \
  -e DATABASE_PASSWORD=sub2api \
  -e DATABASE_NAME=sub2api \
  -e DATABASE_SSLMODE=disable \
  -e REDIS_HOST=127.0.0.1 \
  -e REDIS_PORT=16379 \
  -e SERVER_HOST=0.0.0.0 \
  -e SERVER_PORT=8080 \
  -e JWT_SECRET=e2e-test-jwt-secret-please-change-32bytes-minimum \
  -e TOTP_ENCRYPTION_KEY="$TOTP_ENCRYPTION_KEY" \
  -e RUN_MODE=simple \
  -e MODEL_TRACING_ENABLED=true \
  -e MODEL_TRACING_DESTINATION=langfuse \
  -e MODEL_TRACING_ENDPOINT="$LANGFUSE_TRACE_ENDPOINT" \
  -e MODEL_TRACING_PUBLIC_KEY="$LANGFUSE_PK" \
  -e MODEL_TRACING_SECRET_KEY="$LANGFUSE_SK" \
  -e MODEL_TRACING_CAPTURE_MEDIA_CONTENT=false \
  -e BATCH_IMAGE_ENABLED=true \
  -e BATCH_IMAGE_QUEUE_ENABLED=true \
  -e BATCH_IMAGE_DEFAULT_REQUEUE_DELAY_SECONDS=1 \
  -e BATCH_IMAGE_DELAYED_MOVER_INTERVAL_SECONDS=1 \
  -e IMAGE_STORAGE_ENABLED=true \
  -e IMAGE_STORAGE_ENDPOINT=http://127.0.0.1:9090 \
  -e IMAGE_STORAGE_REGION=auto \
  -e IMAGE_STORAGE_BUCKET=langfuse \
  -e IMAGE_STORAGE_ACCESS_KEY_ID=minio \
  -e IMAGE_STORAGE_SECRET_ACCESS_KEY=miniosecret \
  -e IMAGE_STORAGE_PREFIX=e2e-images/ \
  -e IMAGE_STORAGE_FORCE_PATH_STYLE=true \
  --add-host generativelanguage.googleapis.com:127.0.0.1 \
  -e SSL_CERT_FILE=/etc/ssl/certs/e2e-gemini-ca.crt \
  -v "$GEMINI_TLS_DIR/ca.crt:/etc/ssl/certs/e2e-gemini-ca.crt:ro" \
  -v sub2api-e2e-data:/data \
  -v "$BIN_DIR/sub2api:/app/sub2api:ro" \
  -v "$REPO_ROOT/backend/resources:/app/resources:ro" \
  golang:1.26.5 \
  sh -c 'cd /app && ./sub2api' >/dev/null

for i in {1..60}; do
  code=$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8080/health || true)
  [[ "$code" == "200" ]] && break
  sleep 2
done
[[ "$code" == "200" ]] || { docker logs sub2api-e2e 2>&1 | tail -30 >&2; fail "sub2api health check failed"; }
log "sub2api healthy on :8080"
# 当前 AUTO_SETUP 基线未带异步 trace continuation 列；按 Ent 真源补齐，保证 API 与 worker 使用同一真实续接字段。
docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api \
  -c "ALTER TABLE batch_image_jobs ADD COLUMN IF NOT EXISTS trace_continuation jsonb;" >/dev/null
TRACE_CONTINUATION_COLUMN=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc \
  "SELECT count(*) FROM information_schema.columns WHERE table_schema='public' AND table_name='batch_image_jobs' AND column_name='trace_continuation';")
[[ "$TRACE_CONTINUATION_COLUMN" == "1" ]] || fail "batch image trace_continuation schema precondition failed"

# 5. 登录 + compliance ack + 建 group + 建 API Key + DB 更新 key
log "authenticating admin and creating api key"
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}" \
  | jq -r '.data.access_token')
[[ -n "$TOKEN" && "$TOKEN" != "null" ]] || fail "login failed"

PHRASE=$(curl -s -H "Authorization: Bearer $TOKEN" http://localhost:8080/api/v1/admin/compliance \
  | jq -r '.data.ack_phrase_en')
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"phrase\":\"$PHRASE\",\"language\":\"en\"}" \
  http://localhost:8080/api/v1/admin/compliance/accept >/dev/null

# 5.1 验证完整 endpoint、部署默认、运行时整体更新、secret 三态与 CAS
log "verifying runtime model tracing configuration"
CONFIG_URL="http://localhost:8080/api/v1/admin/model-tracing/config"

DEPLOYMENT_CONFIG=$(curl -fsS -H "Authorization: Bearer $TOKEN" "$CONFIG_URL")
echo "$DEPLOYMENT_CONFIG" | jq -e \
  --argjson prompt_max "$DEFAULT_PROMPT_MAX_BYTES" \
  --argjson response_max "$DEFAULT_RESPONSE_MAX_BYTES" \
  --argjson media_max "$DEFAULT_MEDIA_MAX_BYTES" '
  .code == 0 and
  .data.source == "deployment" and
  .data.enabled == true and
  .data.has_secret == true and
  .data.config_version == 0 and
  .data.prompt_max_bytes == $prompt_max and
  .data.response_max_bytes == $response_max and
  .data.media_max_bytes == $media_max and
  (.data | has("secret_key") | not) and
  (.data | has("secret_key_encrypted") | not)
' >/dev/null || fail "deployment model tracing config is not public or effective"
[[ "$DEPLOYMENT_CONFIG" != *"$LANGFUSE_SK"* ]] || fail "deployment config response leaked secret material"
DEPLOYMENT_ENDPOINT=$(echo "$DEPLOYMENT_CONFIG" | jq -r '.data.endpoint')
[[ "$DEPLOYMENT_ENDPOINT" == "$LANGFUSE_TRACE_ENDPOINT" ]] \
  || fail "deployment endpoint changed: got $DEPLOYMENT_ENDPOINT"


RUNTIME_CONFIG=$(jq -nc \
  --arg endpoint "$LANGFUSE_TRACE_ENDPOINT" \
  --arg public_key "$LANGFUSE_PK" \
  --arg secret_key "$LANGFUSE_SK" \
  '{expected_config_version:0,enabled:true,endpoint:$endpoint,public_key:$public_key,secret_key:$secret_key,prompt_max_bytes:4096,response_max_bytes:4096,media_max_bytes:4096,capture_media_content:false}')
RUNTIME_RESPONSE=$(curl -fsS -X PUT \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$RUNTIME_CONFIG" "$CONFIG_URL")
echo "$RUNTIME_RESPONSE" | jq -e '
  .code == 0 and
  .data.source == "runtime" and
  .data.configured == true and
  .data.enabled == true and
  .data.has_secret == true and
  .data.config_version == 1 and
  (.data | has("secret_key") | not) and
  (.data | has("secret_key_encrypted") | not)
' >/dev/null || fail "valid runtime model tracing update was not applied atomically"
[[ "$RUNTIME_RESPONSE" != *"$LANGFUSE_SK"* ]] || fail "runtime update response leaked secret material"
RUNTIME_ENDPOINT=$(echo "$RUNTIME_RESPONSE" | jq -r '.data.endpoint')
[[ "$RUNTIME_ENDPOINT" == "$LANGFUSE_TRACE_ENDPOINT" ]] \
  || fail "runtime endpoint changed: got $RUNTIME_ENDPOINT"

PRESERVE_SECRET_CONFIG=$(jq -nc \
  --arg endpoint "$LANGFUSE_TRACE_ENDPOINT" \
  --arg public_key "$LANGFUSE_PK" \
  '{expected_config_version:1,enabled:true,endpoint:$endpoint,public_key:$public_key,prompt_max_bytes:2048,response_max_bytes:3072,media_max_bytes:4096,capture_media_content:false}')
PRESERVE_RESPONSE=$(curl -fsS -X PUT \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$PRESERVE_SECRET_CONFIG" "$CONFIG_URL")
echo "$PRESERVE_RESPONSE" | jq -e '
  .data.source == "runtime" and
  .data.has_secret == true and
  .data.config_version == 2 and
  .data.prompt_max_bytes == 2048 and
  .data.response_max_bytes == 3072
' >/dev/null || fail "non-secret update did not preserve the stored secret"

STALE_STATUS=$(curl -sS -o /dev/null -w '%{http_code}' -X PUT \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$PRESERVE_SECRET_CONFIG" "$CONFIG_URL")
[[ "$STALE_STATUS" == "409" ]] || fail "stale config version returned HTTP $STALE_STATUS, want 409"
FINAL_CONFIG=$(curl -fsS -H "Authorization: Bearer $TOKEN" "$CONFIG_URL")
echo "$FINAL_CONFIG" | jq -e '.data.config_version == 2 and .data.has_secret == true' >/dev/null \
  || fail "CAS conflict changed the stored model tracing config"
log "runtime model tracing configuration verified"

GROUP_ID=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"e2e-anthropic","description":"e2e test","platform":"anthropic","rate_multiplier":1,"is_exclusive":false,"status":"active","allow_image_generation":true}' \
  http://localhost:8080/api/v1/admin/groups | jq -r '.data.id')
[[ -n "$GROUP_ID" && "$GROUP_ID" != "null" ]] || fail "group create failed"

curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"name\":\"e2e-key\",\"group_id\":$GROUP_ID}" \
  http://localhost:8080/api/v1/keys >/dev/null
curl -s -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"name\":\"e2e-disabled-key\",\"group_id\":$GROUP_ID}" \
  http://localhost:8080/api/v1/keys >/dev/null

APIKEY="sk-e2e-$(openssl rand -hex 16)"
DISABLED_APIKEY="sk-e2e-disabled-$(openssl rand -hex 16)"
docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -c "UPDATE api_keys SET key='$APIKEY' WHERE name='e2e-key'; UPDATE api_keys SET key='$DISABLED_APIKEY', status='disabled' WHERE name='e2e-disabled-key';" >/dev/null
API_KEY_ID=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc "SELECT id FROM api_keys WHERE name='e2e-key' LIMIT 1")
DISABLED_API_KEY_ID=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc "SELECT id FROM api_keys WHERE name='e2e-disabled-key' LIMIT 1")
[[ -n "$API_KEY_ID" && -n "$DISABLED_API_KEY_ID" ]] || fail "api key ids missing after creation"
log "active and disabled api keys created (masked in logs)"

# 6. 身份与路由边界：匿名/未知 Key/控制面不建 Trace，已识别失败请求建且只建一个根 Span。
RUN_ID="$(date +%s)-$(openssl rand -hex 4)"
ANON_REQUEST_ID="e2e-anonymous-$RUN_ID"
UNKNOWN_REQUEST_ID="e2e-unknown-$RUN_ID"
CONTROL_REQUEST_ID="e2e-control-$RUN_ID"
RECOGNIZED_REQUEST_ID="e2e-recognized-$RUN_ID"
DISABLED_REQUEST_ID="e2e-disabled-$RUN_ID"
SESSION_ID="e2e-session-$RUN_ID"
FAILOVER_REQUEST_ID="e2e-failover-$RUN_ID"
FAILOVER_SESSION_ID="e2e-failover-session-$RUN_ID"
SECURE_REQUEST_ID="e2e-content-security-$RUN_ID"
SECURE_SESSION_ID="e2e-content-security-session-$RUN_ID"
LARGE_REQUEST_ID="e2e-near-limit-$RUN_ID"
LARGE_SESSION_ID="e2e-near-limit-session-$RUN_ID"
STREAM_REQUEST_ID="e2e-stream-$RUN_ID"
STREAM_SESSION_ID="e2e-stream-session-$RUN_ID"
BATCH_REQUEST_ID="e2e-batch-$RUN_ID"
BATCH_TASK_NAME="e2e-batch-task-$RUN_ID"
PARTIAL_STREAM_REQUEST_ID="e2e-partial-stream-$RUN_ID"
PARTIAL_STREAM_SESSION_ID="e2e-partial-stream-session-$RUN_ID"
CLIENT_DISCONNECT_REQUEST_ID="e2e-client-disconnect-$RUN_ID"
CLIENT_DISCONNECT_SESSION_ID="e2e-client-disconnect-session-$RUN_ID"
WS_MULTI_CONNECTION_ID="e2e-ws-multi-$RUN_ID"
WS_SWITCH_CONNECTION_ID="e2e-ws-switch-$RUN_ID"
WS_DISCONNECT_CONNECTION_ID="e2e-ws-disconnect-$RUN_ID"

log "verifying anonymous and unknown credentials remain untraced"
ANON_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Client-Request-ID: $ANON_REQUEST_ID" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"anonymous must stay untraced"}]}' \
  -o /dev/null -w '%{http_code}')
[[ "$ANON_CODE" == "401" ]] || fail "anonymous candidate returned HTTP $ANON_CODE, want 401"

UNKNOWN_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Client-Request-ID: $UNKNOWN_REQUEST_ID" \
  -H "Authorization: Bearer sk-e2e-unknown-$RUN_ID" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"unknown key must stay untraced"}]}' \
  -o /dev/null -w '%{http_code}')
[[ "$UNKNOWN_CODE" == "401" ]] || fail "unknown-key candidate returned HTTP $UNKNOWN_CODE, want 401"

log "verifying authenticated control plane remains untraced"
CONTROL_CODE=$(curl -sS http://localhost:8080/v1/models \
  -H "Authorization: Bearer $APIKEY" \
  -H "X-Client-Request-ID: $CONTROL_REQUEST_ID" \
  -o /dev/null -w '%{http_code}')
[[ "$CONTROL_CODE" == "200" ]] || fail "models control returned HTTP $CONTROL_CODE, want 200"

log "sending recognized model request (expect 503 and one root Span)"
HTTP_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $APIKEY" \
  -H "X-Client-Request-ID: $RECOGNIZED_REQUEST_ID" \
  -d "{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"e2e trace test\"}],\"session_id\":\"$SESSION_ID\",\"stream\":false}" \
  -o /dev/null -w '%{http_code}')
[[ "$HTTP_CODE" == "503" ]] || fail "recognized no-account request returned HTTP $HTTP_CODE, want 503"

log "verifying bounded redacted multimodal capture"
SECURE_BODY_SECRET="e2e-body-secret-$RUN_ID"
SECURE_MEDIA_RAW="e2e-private-media-$RUN_ID"
SECURE_MEDIA_B64=$(printf '%s' "$SECURE_MEDIA_RAW" | openssl base64 -A)
SECURE_LONG_TEXT=$(printf '%3000s' '' | tr ' ' x)
SECURE_BODY=$(jq -nc \
  --arg session "$SECURE_SESSION_ID" \
  --arg secret "$SECURE_BODY_SECRET" \
  --arg media "data:image/png;base64,$SECURE_MEDIA_B64" \
  --arg content "$SECURE_LONG_TEXT" \
  '{session_id:$session,api_key:$secret,model:"gpt-4",messages:[{role:"user",content:[{type:"image_url",image_url:$media},{type:"text",text:$content}]}],stream:false}')
SECURE_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $APIKEY" \
  -H "X-Client-Request-ID: $SECURE_REQUEST_ID" \
  -d "$SECURE_BODY" -o /dev/null -w '%{http_code}')
[[ "$SECURE_CODE" == "503" ]] || fail "content-security request returned HTTP $SECURE_CODE, want 503"

log "sending recognized disabled-key request (expect 401 and one root Span)"
DISABLED_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $DISABLED_APIKEY" \
  -H "X-Client-Request-ID: $DISABLED_REQUEST_ID" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"disabled key failure"}]}' \
  -o /dev/null -w '%{http_code}')
[[ "$DISABLED_CODE" == "401" ]] || fail "disabled-key candidate returned HTTP $DISABLED_CODE, want 401"

# 6.1 小上限截断完成后恢复 16/8/16 MiB 部署默认值，并发送接近 Prompt 上限的真实请求。
log "restoring runtime limits to the 16/8/16 MiB deployment defaults"
DEFAULT_LIMIT_CONFIG=$(jq -nc \
  --arg endpoint "$LANGFUSE_TRACE_ENDPOINT" \
  --arg public_key "$LANGFUSE_PK" \
  --argjson prompt_max "$DEFAULT_PROMPT_MAX_BYTES" \
  --argjson response_max "$DEFAULT_RESPONSE_MAX_BYTES" \
  --argjson media_max "$DEFAULT_MEDIA_MAX_BYTES" \
  '{expected_config_version:2,enabled:true,endpoint:$endpoint,public_key:$public_key,prompt_max_bytes:$prompt_max,response_max_bytes:$response_max,media_max_bytes:$media_max,capture_media_content:false}')
DEFAULT_LIMIT_RESPONSE=$(curl -fsS -X PUT \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$DEFAULT_LIMIT_CONFIG" "$CONFIG_URL")
echo "$DEFAULT_LIMIT_RESPONSE" | jq -e \
  --argjson prompt_max "$DEFAULT_PROMPT_MAX_BYTES" \
  --argjson response_max "$DEFAULT_RESPONSE_MAX_BYTES" \
  --argjson media_max "$DEFAULT_MEDIA_MAX_BYTES" '
  .data.source == "runtime" and
  .data.has_secret == true and
  .data.config_version == 3 and
  .data.prompt_max_bytes == $prompt_max and
  .data.response_max_bytes == $response_max and
  .data.media_max_bytes == $media_max and
  (.data | has("secret_key") | not) and
  (.data | has("secret_key_encrypted") | not)
' >/dev/null || fail "16/8/16 MiB runtime limits were not applied"
[[ "$DEFAULT_LIMIT_RESPONSE" != *"$LANGFUSE_SK"* ]] || fail "default runtime update response leaked secret material"

LARGE_PROMPT_TEXT_BYTES=16760000
LARGE_PROMPT_HEAD="e2e-near-limit-head-$RUN_ID"
LARGE_PROMPT_TAIL="e2e-near-limit-tail-$RUN_ID"
LARGE_REQUEST_FILE="$REPO_ROOT/.e2e-tmp/near-limit-request.json"
LARGE_REQUEST_BYTES=$(python3 -c 'import json,sys; path,session,head,tail,size,limit=sys.argv[1],sys.argv[2],sys.argv[3],sys.argv[4],int(sys.argv[5]),int(sys.argv[6]); content=head+("L"*(size-len(head)-len(tail)))+tail; raw=json.dumps({"model":"gpt-4","messages":[{"role":"user","content":content}],"session_id":session,"stream":False},separators=(",",":")).encode(); assert len(content.encode()) == size and len(raw) <= limit; open(path,"wb").write(raw); print(len(raw))' "$LARGE_REQUEST_FILE" "$LARGE_SESSION_ID" "$LARGE_PROMPT_HEAD" "$LARGE_PROMPT_TAIL" "$LARGE_PROMPT_TEXT_BYTES" "$DEFAULT_PROMPT_MAX_BYTES")
(( LARGE_REQUEST_BYTES <= DEFAULT_PROMPT_MAX_BYTES )) || fail "near-limit request body exceeds the 16 MiB prompt limit: $LARGE_REQUEST_BYTES"
LARGE_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $APIKEY" \
  -H "X-Client-Request-ID: $LARGE_REQUEST_ID" \
  --data-binary "@$LARGE_REQUEST_FILE" -o /dev/null -w '%{http_code}')
[[ "$LARGE_CODE" == "503" ]] || fail "near-limit no-account request returned HTTP $LARGE_CODE, want 503"
log "near-limit prompt sent: http=$LARGE_CODE request_bytes=$LARGE_REQUEST_BYTES prompt_bytes=$LARGE_PROMPT_TEXT_BYTES"

# 6.2 两个独立请求用显式 session_id 归入同一 Session；缓存键不得误归组。
SHARED_SESSION_ID="e2e-shared-session-$RUN_ID"
SESSION_REQUEST_A="e2e-session-a-$RUN_ID"
SESSION_REQUEST_B="e2e-session-b-$RUN_ID"
CACHE_ONLY_REQUEST_ID="e2e-cache-only-$RUN_ID"
for SESSION_REQUEST_ID in "$SESSION_REQUEST_A" "$SESSION_REQUEST_B"; do
  SESSION_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
    -H "Content-Type: application/json" \
    -H "Authorization: Bearer $APIKEY" \
    -H "X-Client-Request-ID: $SESSION_REQUEST_ID" \
    -d "{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"shared session request\"}],\"session_id\":\"$SHARED_SESSION_ID\"}" \
    -o /dev/null -w '%{http_code}')
  [[ "$SESSION_CODE" == "503" ]] || fail "shared-session request returned HTTP $SESSION_CODE, want 503"
done
CACHE_ONLY_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $APIKEY" \
  -H "X-Client-Request-ID: $CACHE_ONLY_REQUEST_ID" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"cache identity is not a session"}],"prompt_cache_key":"cache-only-e2e"}' \
  -o /dev/null -w '%{http_code}')
[[ "$CACHE_ONLY_CODE" == "503" ]] || fail "cache-only request returned HTTP $CACHE_ONLY_CODE, want 503"
log "explicit session grouping and cache-key exclusion requests sent"



# 6.3 创建两个确定性 Anthropic API Key 账号：数值更小的 priority 先失败，随后切换成功账号。
FAILOVER_GROUP_ID=$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"e2e-anthropic-failover","description":"e2e failover test","platform":"anthropic","rate_multiplier":1,"is_exclusive":false,"status":"active"}' \
  http://localhost:8080/api/v1/admin/groups | jq -r '.data.id')
[[ -n "$FAILOVER_GROUP_ID" && "$FAILOVER_GROUP_ID" != "null" ]] || fail "failover group create failed"
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"name\":\"e2e-failover-key\",\"group_id\":$FAILOVER_GROUP_ID}" \
  http://localhost:8080/api/v1/keys >/dev/null
FAILOVER_APIKEY="sk-e2e-failover-$(openssl rand -hex 16)"
docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api \
  -c "UPDATE api_keys SET key='$FAILOVER_APIKEY' WHERE name='e2e-failover-key';" >/dev/null
FAILOVER_API_KEY_ID=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc "SELECT id FROM api_keys WHERE name='e2e-failover-key' LIMIT 1")
[[ -n "$FAILOVER_API_KEY_ID" ]] || fail "failover api key id missing"
FAIL_ACCOUNT_PAYLOAD=$(jq -nc --argjson group_id "$FAILOVER_GROUP_ID" '{
  name:"e2e-fail-account", platform:"anthropic", type:"apikey", concurrency:1, priority:1,
  group_ids:[$group_id], credentials:{api_key:"e2e-upstream-fail-key", base_url:"http://127.0.0.1:18081/fail", model_mapping:{"gpt-4":"claude-e2e"}}, extra:{}
}')
FAIL_ACCOUNT_ID=$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$FAIL_ACCOUNT_PAYLOAD" http://localhost:8080/api/v1/admin/accounts | jq -r '.data.id')
[[ -n "$FAIL_ACCOUNT_ID" && "$FAIL_ACCOUNT_ID" != "null" ]] || fail "failed Anthropic account creation failed"

SUCCESS_ACCOUNT_PAYLOAD=$(jq -nc --argjson group_id "$FAILOVER_GROUP_ID" '{
  name:"e2e-success-account", platform:"anthropic", type:"apikey", concurrency:1, priority:2,
  group_ids:[$group_id], credentials:{api_key:"e2e-upstream-success-key", base_url:"http://127.0.0.1:18081/ok", model_mapping:{"gpt-4":"claude-e2e"}}, extra:{}
}')
SUCCESS_ACCOUNT_ID=$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$SUCCESS_ACCOUNT_PAYLOAD" http://localhost:8080/api/v1/admin/accounts | jq -r '.data.id')
[[ -n "$SUCCESS_ACCOUNT_ID" && "$SUCCESS_ACCOUNT_ID" != "null" ]] || fail "success Anthropic account creation failed"
SCHEDULER_OUTBOX_TARGET=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc \
  "SELECT COALESCE(MAX(id), 0) FROM scheduler_outbox WHERE account_id IN ($FAIL_ACCOUNT_ID, $SUCCESS_ACCOUNT_ID)")
SCHEDULER_OUTBOX_WATERMARK=""
for _ in {1..100}; do
  SCHEDULER_OUTBOX_WATERMARK=$(docker exec sub2api-deps-redis-1 redis-cli GET sched:outbox:watermark)
  [[ "$SCHEDULER_OUTBOX_WATERMARK" =~ ^[0-9]+$ ]] && (( SCHEDULER_OUTBOX_WATERMARK >= SCHEDULER_OUTBOX_TARGET )) && break
  sleep 0.1
done
[[ "$SCHEDULER_OUTBOX_WATERMARK" =~ ^[0-9]+$ ]] && (( SCHEDULER_OUTBOX_WATERMARK >= SCHEDULER_OUTBOX_TARGET )) || fail "failover account scheduler snapshots were not published"
log "failover account scheduler snapshots published"

ALL_FAIL_REQUEST_ID="e2e-all-fail-$RUN_ID"
ALL_FAIL_SESSION_ID="e2e-all-fail-session-$RUN_ID"
ALL_FAIL_APIKEY="$FAILOVER_APIKEY"
HOLD_APIKEY="$FAILOVER_APIKEY"

update_success_account_endpoint() {
  local endpoint="$1" payload response
  payload=$(jq -nc --argjson group_id "$FAILOVER_GROUP_ID" --arg endpoint "$endpoint" '{
    name:"e2e-success-account",type:"apikey",status:"active",concurrency:1,priority:2,
    group_ids:[$group_id],credentials:{api_key:"e2e-upstream-success-key",base_url:$endpoint,model_mapping:{"gpt-4":"claude-e2e"}},extra:{}
  }')
  response=$(curl -fsS -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "$payload" "http://localhost:8080/api/v1/admin/accounts/$SUCCESS_ACCOUNT_ID")
  echo "$response" | jq -e --argjson id "$SUCCESS_ACCOUNT_ID" '.data.id == $id and .data.status == "active"' >/dev/null \
    || fail "failed to move success fixture account to $endpoint"
}


log "sending recognized failover request (expect 429 attempt then successful attempt)"
FAILOVER_RESPONSE_FILE="$REPO_ROOT/.e2e-tmp/failover-response.json"
FAILOVER_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $FAILOVER_APIKEY" \
  -H "X-Client-Request-ID: $FAILOVER_REQUEST_ID" \
  -d "{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"e2e failover trace\"}],\"session_id\":\"$FAILOVER_SESSION_ID\",\"stream\":false}" \
  -o "$FAILOVER_RESPONSE_FILE" -w '%{http_code}')
[[ "$FAILOVER_CODE" == "200" ]] || { cat "$FAILOVER_RESPONSE_FILE" >&2; fail "failover request returned HTTP $FAILOVER_CODE, want 200"; }
jq -e '.choices[0].message.content == "e2e upstream success"' "$FAILOVER_RESPONSE_FILE" >/dev/null \
  || fail "failover response did not come from successful fixture account"

# 隔离已完成的失败账号；复用已加载的成功账号，先切到失败端点验证错误终态。
curl -fsS -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"status":"inactive"}' "http://localhost:8080/api/v1/admin/accounts/$FAIL_ACCOUNT_ID" >/dev/null
update_success_account_endpoint "http://127.0.0.1:18081/fail"

ALL_FAIL_RESPONSE_FILE="$REPO_ROOT/.e2e-tmp/all-fail-response.json"
ALL_FAIL_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ALL_FAIL_APIKEY" \
  -H "X-Client-Request-ID: $ALL_FAIL_REQUEST_ID" \
  -d "{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"exhaust available upstream attempts\"}],\"session_id\":\"$ALL_FAIL_SESSION_ID\"}" \
  -o "$ALL_FAIL_RESPONSE_FILE" -w '%{http_code}')
[[ "$ALL_FAIL_CODE" == "429" ]] || { cat "$ALL_FAIL_RESPONSE_FILE" >&2; fail "all-fail request returned HTTP $ALL_FAIL_CODE, want 429"; }
log "all-attempts-fail request verified: http=$ALL_FAIL_CODE attempts=1"
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" \
  "http://localhost:8080/api/v1/admin/accounts/$SUCCESS_ACCOUNT_ID/clear-rate-limit" >/dev/null
update_success_account_endpoint "http://127.0.0.1:18081/ok"



log "sending deterministic Anthropic SSE request"
STREAM_HEADERS_FILE="$REPO_ROOT/.e2e-tmp/stream-response.headers"
STREAM_RESPONSE_FILE="$REPO_ROOT/.e2e-tmp/stream-response.sse"
STREAM_CODE=$(curl -N -sS -D "$STREAM_HEADERS_FILE" -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $FAILOVER_APIKEY" \
  -H "X-Client-Request-ID: $STREAM_REQUEST_ID" \
  -d "{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"e2e streaming trace\"}],\"session_id\":\"$STREAM_SESSION_ID\",\"stream\":true}" \
  -o "$STREAM_RESPONSE_FILE" -w '%{http_code}')
[[ "$STREAM_CODE" == "200" ]] || fail "stream request returned HTTP $STREAM_CODE, want 200"
STREAM_HEADERS=$(<"$STREAM_HEADERS_FILE")
STREAM_BODY=$(<"$STREAM_RESPONSE_FILE")
[[ "$STREAM_HEADERS" == *"Content-Type: text/event-stream"* ]] || fail "stream response Content-Type is not text/event-stream"
[[ "$STREAM_BODY" == *'"content":"e2e upstream success"'* ]] || fail "stream response missing expected content delta"
[[ "$STREAM_BODY" == *'"finish_reason":"stop"'* ]] || fail "stream response missing successful terminal chunk"
[[ "$STREAM_BODY" == *'data: [DONE]'* ]] || fail "stream response missing [DONE] terminal frame"
log "stream client frames verified: http=$STREAM_CODE content_delta=1 terminal=1"

# 6.4 客户端可见部分输出和实际传输错误必须落为已结束的 Langfuse Trace。
PARTIAL_FIXTURE_BEFORE=$(curl -fsS http://localhost:18081/stats | jq -r '.partial')
update_success_account_endpoint "http://127.0.0.1:18081/partial"
PARTIAL_STREAM_RESPONSE_FILE="$REPO_ROOT/.e2e-tmp/partial-stream-response.sse"
PARTIAL_STREAM_CODE=$(curl -N -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" -H "Authorization: Bearer $FAILOVER_APIKEY" \
  -H "X-Client-Request-ID: $PARTIAL_STREAM_REQUEST_ID" \
  -d "{\"model\":\"gpt-4\",\"messages\":[{\"role\":\"user\",\"content\":\"partial upstream stream must remain observable\"}],\"session_id\":\"$PARTIAL_STREAM_SESSION_ID\",\"stream\":true}" \
  -o "$PARTIAL_STREAM_RESPONSE_FILE" -w '%{http_code}' || true)
[[ "$PARTIAL_STREAM_CODE" == "200" ]] || { cat "$PARTIAL_STREAM_RESPONSE_FILE" >&2; fail "partial upstream stream returned HTTP $PARTIAL_STREAM_CODE, want 200"; }
PARTIAL_STREAM_BODY=$(<"$PARTIAL_STREAM_RESPONSE_FILE")
[[ "$PARTIAL_STREAM_BODY" == *"e2e upstream partial"* ]] || fail "partial upstream stream lost client-visible partial output"
PARTIAL_FIXTURE_AFTER=$(curl -fsS http://localhost:18081/stats | jq -r '.partial')
[[ "$PARTIAL_FIXTURE_AFTER" == "$((PARTIAL_FIXTURE_BEFORE + 1))" ]] || fail "partial upstream fixture call delta=$((PARTIAL_FIXTURE_AFTER - PARTIAL_FIXTURE_BEFORE)), want 1"

SLOW_FIXTURE_BEFORE=$(curl -fsS http://localhost:18081/stats | jq -r '.slow')
update_success_account_endpoint "http://127.0.0.1:18081/slow"
CLIENT_DISCONNECT_OUTPUT_FILE="$REPO_ROOT/.e2e-tmp/client-disconnect-rst.out"
CLIENT_DISCONNECT_ERROR_FILE="$REPO_ROOT/.e2e-tmp/client-disconnect-rst.err"
if colima ssh --profile "$COLIMA_PROFILE" -- "$RAW_RST_CLIENT_BIN" \
  -addr 127.0.0.1:8080 \
  -token "$FAILOVER_APIKEY" \
  -request-id "$CLIENT_DISCONNECT_REQUEST_ID" \
  -session-id "$CLIENT_DISCONNECT_SESSION_ID" \
  >"$CLIENT_DISCONNECT_OUTPUT_FILE" 2>"$CLIENT_DISCONNECT_ERROR_FILE"; then
  CLIENT_DISCONNECT_RST_EXIT=0
else
  CLIENT_DISCONNECT_RST_EXIT=$?
fi
[[ "$CLIENT_DISCONNECT_RST_EXIT" == "0" ]] || { cat "$CLIENT_DISCONNECT_ERROR_FILE" >&2; fail "raw TCP RST client failed after delayed prefix: exit=$CLIENT_DISCONNECT_RST_EXIT"; }
CLIENT_DISCONNECT_OUTPUT=$(<"$CLIENT_DISCONNECT_OUTPUT_FILE")
[[ "$CLIENT_DISCONNECT_OUTPUT" == *"RST_CLIENT_OK addr=127.0.0.1:8080 request_id=$CLIENT_DISCONNECT_REQUEST_ID received_prefix=1"* ]] \
  || fail "raw TCP RST client did not confirm delayed prefix and RST close"
sleep 2
SLOW_FIXTURE_AFTER=$(curl -fsS http://localhost:18081/stats | jq -r '.slow')
[[ "$SLOW_FIXTURE_AFTER" == "$((SLOW_FIXTURE_BEFORE + 1))" ]] || fail "slow stream fixture call delta=$((SLOW_FIXTURE_AFTER - SLOW_FIXTURE_BEFORE)), want 1"
log "partial upstream and real client TCP RST disconnect verified: partial_calls=1 slow_calls=1 rst_client=1"

update_success_account_endpoint "http://127.0.0.1:18081/hold"

# 6.5 运行时快照：请求处理中关闭追踪仍完成原 Trace；后续请求立即不追踪。
apply_runtime_tracing_config() {
  local expected_version="$1" enabled="$2" endpoint="$3" next_version payload response
  next_version=$((expected_version + 1))
  payload=$(jq -nc \
    --argjson expected "$expected_version" \
    --argjson enabled "$enabled" \
    --arg endpoint "$endpoint" \
    --arg public_key "$LANGFUSE_PK" \
    --argjson prompt_max "$DEFAULT_PROMPT_MAX_BYTES" \
    --argjson response_max "$DEFAULT_RESPONSE_MAX_BYTES" \
    --argjson media_max "$DEFAULT_MEDIA_MAX_BYTES" \
    '{expected_config_version:$expected,enabled:$enabled,endpoint:$endpoint,public_key:$public_key,prompt_max_bytes:$prompt_max,response_max_bytes:$response_max,media_max_bytes:$media_max,capture_media_content:false}')
  response=$(curl -fsS -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d "$payload" "$CONFIG_URL")
  echo "$response" | jq -e --argjson version "$next_version" --argjson enabled "$enabled" --arg endpoint "$endpoint" '
    .data.config_version == $version and .data.enabled == $enabled and .data.endpoint == $endpoint and
    .data.has_secret == true and (.data | has("secret_key") | not) and (.data | has("secret_key_encrypted") | not)
  ' >/dev/null || fail "runtime trace config update to version $next_version failed"
  [[ "$response" != *"$LANGFUSE_SK"* ]] || fail "runtime trace config update leaked secret material"
}


HOLD_REQUEST_ID="e2e-in-flight-$RUN_ID"
POST_DISABLE_REQUEST_ID="e2e-post-disable-$RUN_ID"
HOLD_RESPONSE_FILE="$REPO_ROOT/.e2e-tmp/hold-response.json"
HOLD_CODE_FILE="$REPO_ROOT/.e2e-tmp/hold-response.code"
curl -fsS -X POST http://localhost:18081/control/hold/reset >/dev/null
(
  curl -sS -X POST http://localhost:8080/v1/chat/completions \
    -H "Content-Type: application/json" -H "Authorization: Bearer $HOLD_APIKEY" \
    -H "X-Client-Request-ID: $HOLD_REQUEST_ID" \
    -d '{"model":"gpt-4","messages":[{"role":"user","content":"hold across config update"}]}' \
    -o "$HOLD_RESPONSE_FILE" -w '%{http_code}' >"$HOLD_CODE_FILE"
) &
HOLD_PID=$!
HOLD_READY=0
for i in {1..50}; do
  HOLD_READY=$(curl -fsS http://localhost:18081/stats | jq -r '.hold')
  (( HOLD_READY >= 1 )) && break
  sleep 0.1
done
(( HOLD_READY >= 1 )) || fail "in-flight request did not reach hold fixture"
apply_runtime_tracing_config 3 false "$LANGFUSE_TRACE_ENDPOINT"
curl -fsS -X POST http://localhost:18081/control/hold/release >/dev/null
wait "$HOLD_PID"
HOLD_CODE=$(<"$HOLD_CODE_FILE")
[[ "$HOLD_CODE" == "200" ]] || { cat "$HOLD_RESPONSE_FILE" >&2; fail "in-flight request returned HTTP $HOLD_CODE, want 200"; }
jq -e '.choices[0].message.content == "e2e upstream success"' "$HOLD_RESPONSE_FILE" >/dev/null \
  || fail "in-flight request did not retain its successful upstream response"
POST_DISABLE_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" -H "Authorization: Bearer $HOLD_APIKEY" \
  -H "X-Client-Request-ID: $POST_DISABLE_REQUEST_ID" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"new request while tracing disabled"}]}' \
  -o /dev/null -w '%{http_code}')
[[ "$POST_DISABLE_CODE" == "200" ]] || fail "post-disable business request returned HTTP $POST_DISABLE_CODE, want 200"
apply_runtime_tracing_config 4 true "$LANGFUSE_TRACE_ENDPOINT"
log "in-flight snapshot verified: old_request=completed new_request=untraced"

# 6.6 真实 OTLP 500 与慢导出器不得改变完整业务响应、上游调用、Usage 或计费。
assert_fail_open_response() {
  local label="$1" response_file="$2"
  jq -e '.choices[0].message.content == "e2e upstream success" and .usage.prompt_tokens == 7 and .usage.completion_tokens == 3 and .usage.total_tokens == 10' "$response_file" >/dev/null \
    || fail "$label business response or deterministic usage changed"
}
wait_fail_open_usage_after() {
  local cursor="$1" row count input output total total_cost actual_cost
  for _ in {1..100}; do
    row=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc "SELECT count(*), COALESCE(sum(input_tokens), 0), COALESCE(sum(output_tokens), 0), COALESCE(sum(total_cost), 0), COALESCE(sum(actual_cost), 0) FROM usage_logs WHERE api_key_id = $FAILOVER_API_KEY_ID AND id > $cursor")
    IFS='|' read -r count input output total_cost actual_cost <<<"$row"
    total=$((input + output))
    if [[ "$count" == "1" && "$input" == "7" && "$output" == "3" && "$total" == "10" ]]; then
      printf '%s|%s|%s|%s|%s\n' "$input" "$output" "$total" "$total_cost" "$actual_cost"
      return
    fi
    sleep 0.1
  done
  fail "Usage record missing or unstable after cursor=$cursor: $row"
}
usage_log_cursor() {
  docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc "SELECT COALESCE(max(id), 0) FROM usage_logs WHERE api_key_id = $FAILOVER_API_KEY_ID"
}

FAIL_OPEN_UPSTREAM_BEFORE=$(curl -fsS http://localhost:18081/stats | jq -r '.fail_open')
update_success_account_endpoint "http://127.0.0.1:18081/fail-open-baseline"
FAIL_OPEN_BASELINE_CURSOR=$(usage_log_cursor)
FAIL_OPEN_BASELINE_RESPONSE="$REPO_ROOT/.e2e-tmp/fail-open-baseline.json"
FAIL_OPEN_BASELINE_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" -H "Authorization: Bearer $HOLD_APIKEY" \
  -H "X-Client-Request-ID: e2e-export-baseline-$RUN_ID" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"fail-open baseline usage and billing"}]}' \
  -o "$FAIL_OPEN_BASELINE_RESPONSE" -w '%{http_code}')
[[ "$FAIL_OPEN_BASELINE_CODE" == "200" ]] || fail "fail-open baseline business request returned HTTP $FAIL_OPEN_BASELINE_CODE, want 200"
assert_fail_open_response "fail-open baseline" "$FAIL_OPEN_BASELINE_RESPONSE"
FAIL_OPEN_UPSTREAM_AFTER=$(curl -fsS http://localhost:18081/stats | jq -r '.fail_open')
[[ "$FAIL_OPEN_UPSTREAM_AFTER" == "$((FAIL_OPEN_UPSTREAM_BEFORE + 1))" ]] || fail "fail-open baseline upstream call delta=$((FAIL_OPEN_UPSTREAM_AFTER - FAIL_OPEN_UPSTREAM_BEFORE)), want 1"
FAIL_OPEN_BASELINE_USAGE=$(wait_fail_open_usage_after "$FAIL_OPEN_BASELINE_CURSOR")

EXPORT_FAIL_REQUEST_ID="e2e-export-fail-$RUN_ID"
apply_runtime_tracing_config 5 true "http://127.0.0.1:18081/otlp-500/v1/traces"
update_success_account_endpoint "http://127.0.0.1:18081/fail-open-500"
EXPORT_FAIL_UPSTREAM_BEFORE=$(curl -fsS http://localhost:18081/stats | jq -r '.fail_open')
EXPORT_FAIL_CURSOR=$(usage_log_cursor)
EXPORT_FAIL_RESPONSE="$REPO_ROOT/.e2e-tmp/export-fail-response.json"
EXPORT_FAIL_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" -H "Authorization: Bearer $HOLD_APIKEY" \
  -H "X-Client-Request-ID: $EXPORT_FAIL_REQUEST_ID" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"exporter 500 must fail open"}]}' \
  -o "$EXPORT_FAIL_RESPONSE" -w '%{http_code}')
[[ "$EXPORT_FAIL_CODE" == "200" ]] || fail "OTLP-500 business request returned HTTP $EXPORT_FAIL_CODE, want 200"
assert_fail_open_response "OTLP-500" "$EXPORT_FAIL_RESPONSE"
EXPORT_FAIL_UPSTREAM_AFTER=$(curl -fsS http://localhost:18081/stats | jq -r '.fail_open')
[[ "$EXPORT_FAIL_UPSTREAM_AFTER" == "$((EXPORT_FAIL_UPSTREAM_BEFORE + 1))" ]] || fail "OTLP-500 upstream call delta=$((EXPORT_FAIL_UPSTREAM_AFTER - EXPORT_FAIL_UPSTREAM_BEFORE)), want 1"
EXPORT_FAIL_USAGE=$(wait_fail_open_usage_after "$EXPORT_FAIL_CURSOR")
[[ "$EXPORT_FAIL_USAGE" == "$FAIL_OPEN_BASELINE_USAGE" ]] || fail "OTLP-500 changed Usage or billing: baseline=$FAIL_OPEN_BASELINE_USAGE failed=$EXPORT_FAIL_USAGE"
OTLP_ERROR_COUNT=0
for i in {1..100}; do
  OTLP_ERROR_COUNT=$(curl -fsS http://localhost:18081/stats | jq -r '.otlp_error')
  (( OTLP_ERROR_COUNT >= 1 )) && break
  sleep 0.1
done
(( OTLP_ERROR_COUNT >= 1 )) || fail "OTLP-500 fixture did not receive an export"
apply_runtime_tracing_config 6 true "$LANGFUSE_TRACE_ENDPOINT"

SLOW_EXPORT_REQUEST_ID="e2e-export-slow-$RUN_ID"
apply_runtime_tracing_config 7 true "http://127.0.0.1:18081/otlp-slow/v1/traces"
update_success_account_endpoint "http://127.0.0.1:18081/fail-open-slow"
SLOW_EXPORT_UPSTREAM_BEFORE=$(curl -fsS http://localhost:18081/stats | jq -r '.fail_open')
SLOW_EXPORT_CURSOR=$(usage_log_cursor)
SLOW_EXPORT_RESPONSE="$REPO_ROOT/.e2e-tmp/export-slow-response.json"
SLOW_STARTED_NS=$(python3 -c 'import time; print(time.time_ns())')
SLOW_EXPORT_CODE=$(curl -sS -X POST http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" -H "Authorization: Bearer $HOLD_APIKEY" \
  -H "X-Client-Request-ID: $SLOW_EXPORT_REQUEST_ID" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"slow exporter must not block"}]}' \
  -o "$SLOW_EXPORT_RESPONSE" -w '%{http_code}')
SLOW_FINISHED_NS=$(python3 -c 'import time; print(time.time_ns())')
SLOW_ELAPSED_MS=$(python3 -c 'import sys; print((int(sys.argv[2])-int(sys.argv[1]))//1000000)' "$SLOW_STARTED_NS" "$SLOW_FINISHED_NS")
[[ "$SLOW_EXPORT_CODE" == "200" ]] || fail "slow-export business request returned HTTP $SLOW_EXPORT_CODE, want 200"
assert_fail_open_response "slow OTLP" "$SLOW_EXPORT_RESPONSE"
python3 -c 'import sys; raise SystemExit(0 if int(sys.argv[1]) < 2000 else 1)' "$SLOW_ELAPSED_MS" \
  || fail "slow exporter blocked business response for ${SLOW_ELAPSED_MS}ms"
SLOW_EXPORT_UPSTREAM_AFTER=$(curl -fsS http://localhost:18081/stats | jq -r '.fail_open')
[[ "$SLOW_EXPORT_UPSTREAM_AFTER" == "$((SLOW_EXPORT_UPSTREAM_BEFORE + 1))" ]] || fail "slow OTLP upstream call delta=$((SLOW_EXPORT_UPSTREAM_AFTER - SLOW_EXPORT_UPSTREAM_BEFORE)), want 1"
SLOW_EXPORT_USAGE=$(wait_fail_open_usage_after "$SLOW_EXPORT_CURSOR")
[[ "$SLOW_EXPORT_USAGE" == "$FAIL_OPEN_BASELINE_USAGE" ]] || fail "slow OTLP changed Usage or billing: baseline=$FAIL_OPEN_BASELINE_USAGE slow=$SLOW_EXPORT_USAGE"
OTLP_SLOW_COUNT=0
for i in {1..100}; do
  OTLP_SLOW_COUNT=$(curl -fsS http://localhost:18081/stats | jq -r '.otlp_slow')
  (( OTLP_SLOW_COUNT >= 1 )) && break
  sleep 0.1
done
(( OTLP_SLOW_COUNT >= 1 )) || fail "slow OTLP fixture did not receive an export"
apply_runtime_tracing_config 8 true "$LANGFUSE_TRACE_ENDPOINT"
log "export fail-open verified: otlp_500=$OTLP_ERROR_COUNT slow_exports=$OTLP_SLOW_COUNT business_ms=$SLOW_ELAPSED_MS upstream_calls=1 usage_billing=stable"

# 6.7 真实 Responses WebSocket：同一连接多回合、回合间配置切换和客户端断连。
log "creating deterministic OpenAI Responses WebSocket account"
WS_GROUP_ID=$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name":"e2e-openai-ws","description":"e2e responses websocket tracing","platform":"openai","rate_multiplier":1,"is_exclusive":false,"status":"active","allow_messages_dispatch":true}' \
  http://localhost:8080/api/v1/admin/groups | jq -er '.data.id')
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"name\":\"e2e-openai-ws-key\",\"group_id\":$WS_GROUP_ID}" http://localhost:8080/api/v1/keys >/dev/null
WS_APIKEY="sk-e2e-openai-ws-$(openssl rand -hex 16)"
docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api \
  -c "UPDATE api_keys SET key='$WS_APIKEY' WHERE name='e2e-openai-ws-key';" >/dev/null
WS_ACCOUNT_PAYLOAD=$(jq -nc --argjson group_id "$WS_GROUP_ID" '{
  name:"e2e-openai-ws-account",platform:"openai",type:"apikey",concurrency:1,priority:1,status:"active",schedulable:true,
  group_ids:[$group_id],credentials:{api_key:"e2e-ws-upstream-key",base_url:"http://127.0.0.1:18081/ws",model_mapping:{"gpt-e2e-ws":"gpt-e2e-ws-upstream"}},
  extra:{openai_responses_supported:true,openai_responses_mode:"force_responses",openai_apikey_responses_websockets_v2_enabled:true}
}')
WS_ACCOUNT_ID=$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$WS_ACCOUNT_PAYLOAD" http://localhost:8080/api/v1/admin/accounts | jq -er '.data.id')
[[ -n "$WS_ACCOUNT_ID" ]] || fail "Responses WebSocket account creation failed"
WS_CLIENT_URL="ws://127.0.0.1:8080/v1/responses"
WS_CLIENT_ERROR_FILE="$REPO_ROOT/.e2e-tmp/responses-ws-client.err"
WS_FIXTURE_BEFORE=$(curl -fsS http://localhost:18081/stats | jq -r '.ws_turns')
WS_MULTI_CLIENT_OUTPUT=$(colima ssh --profile "$COLIMA_PROFILE" -- "$RESPONSES_WS_CLIENT_BIN" --url "$WS_CLIENT_URL" --token "$WS_APIKEY" --request-id "$WS_MULTI_CONNECTION_ID" --mode multi 2>"$WS_CLIENT_ERROR_FILE") \
  || { cat "$WS_CLIENT_ERROR_FILE" >&2; fail "Responses WebSocket multi-turn client failed"; }
[[ "$WS_MULTI_CLIENT_OUTPUT" == *"turn_index=1 response_id=resp_e2e_ws_"* && "$WS_MULTI_CLIENT_OUTPUT" == *"turn_index=2 response_id=resp_e2e_ws_"* ]] \
  || fail "Responses WebSocket multi-turn client output mismatch"

WS_SWITCH_READY_FILE="$REPO_ROOT/.e2e-tmp/responses-ws-switch.ready"
WS_SWITCH_CONTINUE_FILE="$REPO_ROOT/.e2e-tmp/responses-ws-switch.continue"
WS_SWITCH_CLIENT_OUTPUT_FILE="$REPO_ROOT/.e2e-tmp/responses-ws-switch.out"
rm -f "$WS_SWITCH_READY_FILE" "$WS_SWITCH_CONTINUE_FILE" "$WS_SWITCH_CLIENT_OUTPUT_FILE"
colima ssh --profile "$COLIMA_PROFILE" -- "$RESPONSES_WS_CLIENT_BIN" --url "$WS_CLIENT_URL" --token "$WS_APIKEY" --request-id "$WS_SWITCH_CONNECTION_ID" --mode pause \
  --ready-file "$WS_SWITCH_READY_FILE" --continue-file "$WS_SWITCH_CONTINUE_FILE" >"$WS_SWITCH_CLIENT_OUTPUT_FILE" 2>"$WS_CLIENT_ERROR_FILE" &
WS_SWITCH_CLIENT_PID=$!
for _ in {1..100}; do
  [[ -f "$WS_SWITCH_READY_FILE" ]] && break
  sleep 0.1
done
[[ -f "$WS_SWITCH_READY_FILE" ]] || { cat "$WS_CLIENT_ERROR_FILE" >&2; fail "Responses WebSocket first turn did not complete before config switch"; }
apply_runtime_tracing_config 9 false "$LANGFUSE_TRACE_ENDPOINT"
: >"$WS_SWITCH_CONTINUE_FILE"
wait "$WS_SWITCH_CLIENT_PID" || { cat "$WS_CLIENT_ERROR_FILE" >&2; fail "Responses WebSocket second turn failed after config switch"; }
WS_SWITCH_CLIENT_OUTPUT=$(<"$WS_SWITCH_CLIENT_OUTPUT_FILE")
[[ "$WS_SWITCH_CLIENT_OUTPUT" == *"turn_index=1 response_id=resp_e2e_ws_"* && "$WS_SWITCH_CLIENT_OUTPUT" == *"turn_index=2 response_id=resp_e2e_ws_"* ]] \
  || fail "Responses WebSocket config-switch client output mismatch"
apply_runtime_tracing_config 10 true "$LANGFUSE_TRACE_ENDPOINT"

WS_DISCONNECT_CLIENT_OUTPUT=$(colima ssh --profile "$COLIMA_PROFILE" -- "$RESPONSES_WS_CLIENT_BIN" --url "$WS_CLIENT_URL" --token "$WS_APIKEY" --request-id "$WS_DISCONNECT_CONNECTION_ID" --mode disconnect 2>"$WS_CLIENT_ERROR_FILE") \
  || { cat "$WS_CLIENT_ERROR_FILE" >&2; fail "Responses WebSocket disconnect client failed"; }
[[ "$WS_DISCONNECT_CLIENT_OUTPUT" == *"WS_DISCONNECTED connection_request_id=$WS_DISCONNECT_CONNECTION_ID partial_response_id=resp_e2e_ws_disconnect"* ]] \
  || fail "Responses WebSocket disconnect client did not close after partial output"
sleep 2
WS_FIXTURE_AFTER=$(curl -fsS http://localhost:18081/stats | jq -r '.ws_turns')
[[ "$WS_FIXTURE_AFTER" == "$((WS_FIXTURE_BEFORE + 5))" ]] || fail "Responses WebSocket fixture turn delta=$((WS_FIXTURE_AFTER - WS_FIXTURE_BEFORE)), want 5"
WS_DISCONNECT_TURNS=$(curl -fsS http://localhost:18081/stats | jq -r '.ws_disconnect_turns')
[[ "$WS_DISCONNECT_TURNS" == "1" ]] || fail "Responses WebSocket fixture did not observe one disconnect turn: got=$WS_DISCONNECT_TURNS"
log "Responses WebSocket multi-turn, config switch, and disconnect clients verified: turns=5"


# 6.4 真实 batch image API -> queue -> worker -> Langfuse continuation。
log "sending deterministic Gemini batch image request"
BATCH_GROUP_PAYLOAD=$(jq -nc '{
  name:"e2e-gemini-batch", description:"e2e batch worker tracing", platform:"gemini",
  rate_multiplier:1, is_exclusive:false, allow_image_generation:true,
  allow_batch_image_generation:true, image_price_1k:0,
  batch_image_discount_multiplier:0.5, batch_image_hold_multiplier:0.6
}')
BATCH_GROUP_ID=$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$BATCH_GROUP_PAYLOAD" http://localhost:8080/api/v1/admin/groups | jq -er '.data.id')
curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "{\"name\":\"e2e-batch-key\",\"group_id\":$BATCH_GROUP_ID}" \
  http://localhost:8080/api/v1/keys >/dev/null
BATCH_APIKEY="sk-e2e-batch-$(openssl rand -hex 16)"
docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api \
  -c "UPDATE api_keys SET key='$BATCH_APIKEY' WHERE name='e2e-batch-key';" >/dev/null
BATCH_API_KEY_ID=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc \
  "SELECT id FROM api_keys WHERE name='e2e-batch-key' LIMIT 1")
BATCH_USER_ID=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc \
  "SELECT user_id FROM api_keys WHERE id=$BATCH_API_KEY_ID")
BATCH_ACCOUNT_PAYLOAD=$(jq -nc --argjson group_id "$BATCH_GROUP_ID" --arg api_key "$GEMINI_BATCH_API_KEY" '{
  name:"e2e-gemini-batch-account", platform:"gemini", type:"apikey", concurrency:1, priority:100,
  group_ids:[$group_id], credentials:{api_key:$api_key,model_mapping:{"gemini-2.5-flash-image":"gemini-2.5-flash-image"}}, extra:{}
}')
BATCH_ACCOUNT_ID=$(curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d "$BATCH_ACCOUNT_PAYLOAD" http://localhost:8080/api/v1/admin/accounts | jq -er '.data.id')
BATCH_RESPONSE_FILE="$REPO_ROOT/.e2e-tmp/batch-response.json"
BATCH_SUBMIT_PAYLOAD=$(jq -nc --arg task "$BATCH_TASK_NAME" --argjson count "$BATCH_ITEM_COUNT" '{
  model:"gemini-2.5-flash-image", task_name:$task, provider:"gemini_api", image_size:"1K",
  items:[range(0;$count) as $i | {
    custom_id:("item-"+($i|tostring)),
    prompt:(if $i == 0 then "e2e batch successful item" else "e2e batch failed item "+($i|tostring) end)
  }]
}')
BATCH_CODE=$(curl -sS -X POST http://localhost:8080/v1/images/batches \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $BATCH_APIKEY" \
  -H "X-Client-Request-ID: $BATCH_REQUEST_ID" \
  -H "Idempotency-Key: e2e-$BATCH_REQUEST_ID" \
  -d "$BATCH_SUBMIT_PAYLOAD" -o "$BATCH_RESPONSE_FILE" -w '%{http_code}')
[[ "$BATCH_CODE" == "200" ]] || { jq -c '{status,error,code}' "$BATCH_RESPONSE_FILE" >&2 || true; fail "batch submit returned HTTP $BATCH_CODE, want 200"; }
BATCH_ID=$(jq -er '.id' "$BATCH_RESPONSE_FILE")
BATCH_STATUS=""
for i in {1..90}; do
  BATCH_GET_RESPONSE=$(curl -fsS \
    -H "Authorization: Bearer $BATCH_APIKEY" \
    "http://localhost:8080/v1/images/batches/$BATCH_ID")
  BATCH_STATUS=$(echo "$BATCH_GET_RESPONSE" | jq -r '.status')
  [[ "$BATCH_STATUS" == "completed" || "$BATCH_STATUS" == "failed" || "$BATCH_STATUS" == "cancelled" ]] && break
  sleep 1
done
[[ "$BATCH_STATUS" == "completed" ]] || fail "batch worker terminal status=$BATCH_STATUS, want completed"
echo "$BATCH_GET_RESPONSE" | jq -e --argjson total "$BATCH_ITEM_COUNT" --argjson failed "$EXPECTED_BATCH_FAILED_ITEMS" '
  .item_count == $total and .success_count == 1 and .fail_count == $failed and .actual_cost == 0
' >/dev/null || fail "batch terminal counters or zero-cost settlement mismatch"
BATCH_ITEMS_RESPONSE=$(curl -fsS \
  -H "Authorization: Bearer $BATCH_APIKEY" \
  "http://localhost:8080/v1/images/batches/$BATCH_ID/items?limit=$BATCH_ITEM_COUNT")
echo "$BATCH_ITEMS_RESPONSE" | jq -e --argjson total "$BATCH_ITEM_COUNT" --argjson failed "$EXPECTED_BATCH_FAILED_ITEMS" '
  (.data | length) == $total and
  ([.data[].custom_id] | unique | length) == $total and
  ([.data[].custom_id] | sort) == ([range(0;$total) | "item-\(.)"] | sort) and
  (any(.data[]; .custom_id == "item-0" and .status == "succeeded" and .image_count == 1)) and
  ([.data[] | select(.status == "failed" and .error.code == "SAFETY_BLOCKED")] | length) == $failed
' >/dev/null || fail "batch item identities or terminal results mismatch"
BATCH_FIXTURE_STATS=$(curl -fsS http://localhost:18081/stats)
echo "$BATCH_FIXTURE_STATS" | jq -e --argjson total "$BATCH_ITEM_COUNT" '
  .batch_auth_fail == 0 and .batch_auth_ok >= 5 and .batch_input_items == $total and
  .batch_upload == 1 and .batch_create == 1 and .batch_get >= 1 and
  .batch_metadata >= 1 and .batch_download >= 1
' >/dev/null || fail "Gemini fixture did not observe the authenticated upload/create/get/download chain"
log "batch API and worker verified at configured scale: http=$BATCH_CODE status=$BATCH_STATUS items=$BATCH_ITEM_COUNT success=1 failed=$EXPECTED_BATCH_FAILED_ITEMS upstream_auth=verified"


# 7. 等 BatchSpanProcessor flush 并查 ClickHouse。
log "querying langfuse clickhouse for identity and route boundaries"
sleep 6
TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE session_id = '$SESSION_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$TRACE_ID" ]] || fail "no trace found in langfuse for session_id=$SESSION_ID"

TRACE_COUNT=$(clickhouse_query "SELECT count() FROM traces WHERE session_id = '$SESSION_ID' FORMAT TabSeparated")
[[ "$TRACE_COUNT" == "1" ]] || fail "expected exactly 1 recognized trace, got $TRACE_COUNT"
OBS_COUNT=$(clickhouse_query "SELECT count() FROM observations WHERE trace_id = '$TRACE_ID' FORMAT TabSeparated")
[[ "$OBS_COUNT" == "1" ]] || fail "expected one root observation and no synthetic attempt, got $OBS_COUNT"
GEN_COUNT=$(clickhouse_query "SELECT count() FROM observations WHERE trace_id = '$TRACE_ID' AND type = 'GENERATION' FORMAT TabSeparated")
[[ "$GEN_COUNT" == "0" ]] || fail "recognized pre-attempt failure exported $GEN_COUNT synthetic generations"

# 8. 根 Span、身份、会话与失败输出断言。
ROW=$(clickhouse_query "SELECT name, user_id, session_id, metadata['api_key_id'], metadata['group_id'], metadata['request_id'], metadata['entry_protocol'], metadata['client_model'], has(tags, 'entry_protocol:openai.chat_completions'), has(tags, 'client_model:gpt-4') FROM traces WHERE id = '$TRACE_ID' FORMAT TabSeparated")
TRACE_NAME=$(echo "$ROW" | cut -f1)
TRACE_USER=$(echo "$ROW" | cut -f2)
TRACE_SESSION=$(echo "$ROW" | cut -f3)
TRACE_AKID=$(echo "$ROW" | cut -f4)
TRACE_GID=$(echo "$ROW" | cut -f5)
TRACE_REQUEST_ID=$(echo "$ROW" | cut -f6)
TRACE_ENTRY_PROTOCOL=$(echo "$ROW" | cut -f7)
TRACE_CLIENT_MODEL=$(echo "$ROW" | cut -f8)
TRACE_PROTOCOL_TAG=$(echo "$ROW" | cut -f9)
TRACE_MODEL_TAG=$(echo "$ROW" | cut -f10)
[[ "$TRACE_NAME" == "model.request" ]] || fail "trace name mismatch: $TRACE_NAME"
[[ "$TRACE_USER" == "1" ]] || fail "user_id mismatch: $TRACE_USER"
[[ "$TRACE_SESSION" == "$SESSION_ID" ]] || fail "session_id mismatch: $TRACE_SESSION != $SESSION_ID"
[[ "$TRACE_AKID" == "$API_KEY_ID" ]] || fail "api_key_id mismatch: $TRACE_AKID != $API_KEY_ID"
[[ "$TRACE_GID" == "$GROUP_ID" ]] || fail "group_id mismatch: $TRACE_GID != $GROUP_ID"
[[ "$TRACE_REQUEST_ID" == "$RECOGNIZED_REQUEST_ID" ]] || fail "request_id mismatch: $TRACE_REQUEST_ID"
[[ "$TRACE_ENTRY_PROTOCOL" == "openai.chat_completions" ]] || fail "entry protocol mismatch: $TRACE_ENTRY_PROTOCOL"
[[ "$TRACE_CLIENT_MODEL" == "gpt-4" ]] || fail "client model mismatch: $TRACE_CLIENT_MODEL"
[[ "$TRACE_PROTOCOL_TAG" == "1" && "$TRACE_MODEL_TAG" == "1" ]] || fail "root trace missing entry protocol/client model tags"

ROOT_ROW=$(clickhouse_query "SELECT name, type, level, status_message, output FROM observations WHERE trace_id = '$TRACE_ID' AND name = 'model.request' FORMAT TabSeparated")
ROOT_NAME=$(echo "$ROOT_ROW" | cut -f1)
ROOT_TYPE=$(echo "$ROOT_ROW" | cut -f2)
ROOT_LEVEL=$(echo "$ROOT_ROW" | cut -f3)
ROOT_STATUS=$(echo "$ROOT_ROW" | cut -f4)
ROOT_OUTPUT=$(echo "$ROOT_ROW" | cut -f5-)
[[ "$ROOT_NAME" == "model.request" && "$ROOT_TYPE" == "SPAN" ]] || fail "root observation mismatch: $ROOT_NAME/$ROOT_TYPE"
[[ "$ROOT_LEVEL" == "ERROR" && "$ROOT_STATUS" == "server_error" ]] || fail "root failure status mismatch: $ROOT_LEVEL/$ROOT_STATUS"
[[ "$ROOT_OUTPUT" == *"No available accounts"* ]] || fail "root output missing upstream-selection failure"

# 8.1 有界内容：秘密和媒体原文必须隔离，超限 Prompt 必须带确定性标记。
SECURE_TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE metadata['request_id'] = '$SECURE_REQUEST_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$SECURE_TRACE_ID" ]] || fail "content-security request did not export a trace"
SECURE_INPUT=$(clickhouse_query "SELECT input FROM observations WHERE trace_id = '$SECURE_TRACE_ID' AND name = 'model.request' LIMIT 1 FORMAT TabSeparated")
[[ "$SECURE_INPUT" == *"[truncated:original_bytes="* ]] || fail "oversized prompt missing deterministic truncation marker"
[[ "$SECURE_INPUT" == *"fingerprint"* && "$SECURE_INPUT" == *"approx_bytes"* ]] || fail "default media descriptor missing fingerprint or approximate size"
[[ "$SECURE_INPUT" != *"$SECURE_BODY_SECRET"* ]] || fail "trace input leaked request body secret"
[[ "$SECURE_INPUT" != *"$SECURE_MEDIA_RAW"* && "$SECURE_INPUT" != *"$SECURE_MEDIA_B64"* ]] || fail "trace input leaked media content"

# 8.2 匿名、未知 Key 和控制面必须零 Trace；disabled 是已识别身份，必须有且仅有一个根 Span。
for pair in \
  "anonymous:$ANON_REQUEST_ID" \
  "unknown:$UNKNOWN_REQUEST_ID" \
  "control:$CONTROL_REQUEST_ID"; do
  label=${pair%%:*}
  request_id=${pair#*:}
  count=$(clickhouse_query "SELECT count() FROM traces WHERE metadata['request_id'] = '$request_id' FORMAT TabSeparated")
  [[ "$count" == "0" ]] || fail "$label request unexpectedly exported $count traces"
done

DISABLED_TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE metadata['request_id'] = '$DISABLED_REQUEST_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$DISABLED_TRACE_ID" ]] || fail "recognized disabled-key request did not export a root trace"
DISABLED_ROW=$(clickhouse_query "SELECT count(), any(t.user_id), any(t.metadata['api_key_id']), any(t.metadata['group_id']), countIf(o.type = 'GENERATION'), any(o.level) FROM traces t INNER JOIN observations o ON o.trace_id = t.id WHERE t.id = '$DISABLED_TRACE_ID' FORMAT TabSeparated")
DISABLED_TRACE_COUNT=$(echo "$DISABLED_ROW" | cut -f1)
DISABLED_USER=$(echo "$DISABLED_ROW" | cut -f2)
DISABLED_AKID=$(echo "$DISABLED_ROW" | cut -f3)
DISABLED_GID=$(echo "$DISABLED_ROW" | cut -f4)
DISABLED_GENERATIONS=$(echo "$DISABLED_ROW" | cut -f5)
DISABLED_LEVEL=$(echo "$DISABLED_ROW" | cut -f6)
[[ "$DISABLED_TRACE_COUNT" == "1" ]] || fail "disabled-key trace/observation cardinality mismatch: $DISABLED_TRACE_COUNT"
[[ "$DISABLED_USER" == "1" && "$DISABLED_AKID" == "$DISABLED_API_KEY_ID" && "$DISABLED_GID" == "$GROUP_ID" ]] || fail "disabled-key identity metadata mismatch"
[[ "$DISABLED_GENERATIONS" == "0" && "$DISABLED_LEVEL" == "ERROR" ]] || fail "disabled-key trace fabricated a generation or lost ERROR status"

# 8.3 显式会话跨两个 Trace 归组；prompt_cache_key 不得成为 Session。
SHARED_SESSION_ROW=$(clickhouse_query "SELECT count(), uniqExact(id), countIf(session_id = '$SHARED_SESSION_ID') FROM traces WHERE metadata['request_id'] IN ('$SESSION_REQUEST_A', '$SESSION_REQUEST_B') FORMAT TabSeparated")
SHARED_SESSION_TRACES=$(echo "$SHARED_SESSION_ROW" | cut -f1)
SHARED_SESSION_IDS=$(echo "$SHARED_SESSION_ROW" | cut -f2)
SHARED_SESSION_MATCHES=$(echo "$SHARED_SESSION_ROW" | cut -f3)
[[ "$SHARED_SESSION_TRACES" == "2" && "$SHARED_SESSION_IDS" == "2" && "$SHARED_SESSION_MATCHES" == "2" ]] \
  || fail "shared session grouping mismatch: traces=$SHARED_SESSION_TRACES ids=$SHARED_SESSION_IDS matches=$SHARED_SESSION_MATCHES"
CACHE_SESSION_ROW=$(clickhouse_query "SELECT count(), countIf(isNull(session_id) OR session_id = '') FROM traces WHERE metadata['request_id'] = '$CACHE_ONLY_REQUEST_ID' FORMAT TabSeparated")
CACHE_TRACE_COUNT=$(echo "$CACHE_SESSION_ROW" | cut -f1)
CACHE_EMPTY_SESSION_COUNT=$(echo "$CACHE_SESSION_ROW" | cut -f2)
[[ "$CACHE_TRACE_COUNT" == "1" && "$CACHE_EMPTY_SESSION_COUNT" == "1" ]] \
  || fail "prompt_cache_key was lost or misclassified as a session: traces=$CACHE_TRACE_COUNT empty_sessions=$CACHE_EMPTY_SESSION_COUNT"


# 8.3 同一根 Trace 下必须有两个真实 GENERATION：失败账号 attempt.1、成功账号 attempt.2。
FAILOVER_TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE session_id = '$FAILOVER_SESSION_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$FAILOVER_TRACE_ID" ]] || fail "failover request did not export a trace"
FAILOVER_OBS_ROW=$(clickhouse_query "SELECT count(), countIf(type = 'GENERATION'), countIf(name = 'upstream.attempt.1' AND type = 'GENERATION' AND level = 'ERROR' AND metadata['account_id'] = '$FAIL_ACCOUNT_ID' AND length(usage_details) = 0 AND length(cost_details) = 0), countIf(name = 'upstream.attempt.2' AND type = 'GENERATION' AND metadata['account_id'] = '$SUCCESS_ACCOUNT_ID' AND usage_details['input'] = 7 AND usage_details['output'] = 3 AND usage_details['total'] = 10 AND mapContains(cost_details, 'total')), countIf(name = 'usage.final') FROM observations WHERE trace_id = '$FAILOVER_TRACE_ID' FORMAT TabSeparated")
FAILOVER_OBS_COUNT=$(echo "$FAILOVER_OBS_ROW" | cut -f1)
FAILOVER_GEN_COUNT=$(echo "$FAILOVER_OBS_ROW" | cut -f2)
FAILOVER_FAILED_MATCH=$(echo "$FAILOVER_OBS_ROW" | cut -f3)
FAILOVER_SUCCESS_MATCH=$(echo "$FAILOVER_OBS_ROW" | cut -f4)
FAILOVER_LEGACY_USAGE_MATCH=$(echo "$FAILOVER_OBS_ROW" | cut -f5)
[[ "$FAILOVER_OBS_COUNT" == "3" && "$FAILOVER_GEN_COUNT" == "2" && "$FAILOVER_LEGACY_USAGE_MATCH" == "0" ]] \
  || fail "failover hierarchy mismatch: observations=$FAILOVER_OBS_COUNT generations=$FAILOVER_GEN_COUNT legacy_usage=$FAILOVER_LEGACY_USAGE_MATCH"
[[ "$FAILOVER_FAILED_MATCH" == "1" && "$FAILOVER_SUCCESS_MATCH" == "1" ]] \
  || fail "failover attempt account/status mapping mismatch: failed=$FAILOVER_FAILED_MATCH success=$FAILOVER_SUCCESS_MATCH"
FAILOVER_ROOT_ID=$(clickhouse_query "SELECT id FROM observations WHERE trace_id = '$FAILOVER_TRACE_ID' AND name = 'model.request' LIMIT 1 FORMAT TabSeparated")
FAILOVER_CHILDREN=$(clickhouse_query "SELECT count() FROM observations WHERE trace_id = '$FAILOVER_TRACE_ID' AND type = 'GENERATION' AND toString(parent_observation_id) = '$FAILOVER_ROOT_ID' FORMAT TabSeparated")
[[ "$FAILOVER_CHILDREN" == "2" ]] || fail "failover generations are not direct children of root: $FAILOVER_CHILDREN"
FAILOVER_OUTPUTS=$(clickhouse_query "SELECT output FROM observations WHERE trace_id = '$FAILOVER_TRACE_ID' AND type = 'GENERATION' ORDER BY name FORMAT TabSeparated")
[[ "$FAILOVER_OUTPUTS" != *"e2e-upstream-failure-secret"* && "$FAILOVER_OUTPUTS" != *"e2e-upstream-success-secret"* ]] \
  || fail "attempt output leaked upstream response secret"
[[ "$FAILOVER_OUTPUTS" == *"[REDACTED]"* ]] || fail "attempt outputs missing secret redaction marker"

# 8.5 所有实际上游尝试失败仍须保留根 Span 与错误 Generation。
ALL_FAIL_TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE metadata['request_id'] = '$ALL_FAIL_REQUEST_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$ALL_FAIL_TRACE_ID" ]] || fail "all-attempts-fail request did not export a trace"
ALL_FAIL_OBS_ROW=$(clickhouse_query "SELECT count(), countIf(type = 'GENERATION'), countIf(type = 'GENERATION' AND level = 'ERROR'), countIf(name = 'model.request' AND type = 'SPAN' AND level = 'ERROR'), countIf(end_time IS NULL) FROM observations WHERE trace_id = '$ALL_FAIL_TRACE_ID' FORMAT TabSeparated")
ALL_FAIL_OBS_COUNT=$(echo "$ALL_FAIL_OBS_ROW" | cut -f1)
ALL_FAIL_GEN_COUNT=$(echo "$ALL_FAIL_OBS_ROW" | cut -f2)
ALL_FAIL_ERROR_GENS=$(echo "$ALL_FAIL_OBS_ROW" | cut -f3)
ALL_FAIL_ERROR_ROOT=$(echo "$ALL_FAIL_OBS_ROW" | cut -f4)
ALL_FAIL_UNFINISHED=$(echo "$ALL_FAIL_OBS_ROW" | cut -f5)
ALL_FAIL_ROOT_ID=$(clickhouse_query "SELECT id FROM observations WHERE trace_id = '$ALL_FAIL_TRACE_ID' AND name = 'model.request' LIMIT 1 FORMAT TabSeparated")
ALL_FAIL_CHILDREN=$(clickhouse_query "SELECT count() FROM observations WHERE trace_id = '$ALL_FAIL_TRACE_ID' AND type = 'GENERATION' AND toString(parent_observation_id) = '$ALL_FAIL_ROOT_ID' FORMAT TabSeparated")
[[ "$ALL_FAIL_OBS_COUNT" == "2" && "$ALL_FAIL_GEN_COUNT" == "1" && "$ALL_FAIL_ERROR_GENS" == "1" && "$ALL_FAIL_ERROR_ROOT" == "1" && "$ALL_FAIL_UNFINISHED" == "0" && "$ALL_FAIL_CHILDREN" == "1" ]] \
  || fail "all-attempts-fail trace mismatch: observations=$ALL_FAIL_OBS_COUNT generations=$ALL_FAIL_GEN_COUNT error_generations=$ALL_FAIL_ERROR_GENS error_root=$ALL_FAIL_ERROR_ROOT unfinished=$ALL_FAIL_UNFINISHED children=$ALL_FAIL_CHILDREN"


# 8.4 默认 16 MiB Prompt：Langfuse 必须收到接近上限的完整内容，且没有意外截断。
LARGE_TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE metadata['request_id'] = '$LARGE_REQUEST_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$LARGE_TRACE_ID" ]] || fail "near-limit request did not export a trace"
LARGE_TRACE_COUNT=$(clickhouse_query "SELECT count() FROM traces WHERE metadata['request_id'] = '$LARGE_REQUEST_ID' FORMAT TabSeparated")
LARGE_OBS_ROW=$(clickhouse_query "SELECT count(), countIf(type = 'GENERATION') FROM observations WHERE trace_id = '$LARGE_TRACE_ID' FORMAT TabSeparated")
LARGE_OBS_COUNT=$(echo "$LARGE_OBS_ROW" | cut -f1)
LARGE_GEN_COUNT=$(echo "$LARGE_OBS_ROW" | cut -f2)
[[ "$LARGE_TRACE_COUNT" == "1" && "$LARGE_OBS_COUNT" == "1" && "$LARGE_GEN_COUNT" == "0" ]] \
  || fail "near-limit trace cardinality mismatch: traces=$LARGE_TRACE_COUNT observations=$LARGE_OBS_COUNT generations=$LARGE_GEN_COUNT"
LARGE_INPUT_ROW=$(clickhouse_query "SELECT lengthUTF8(input), position(input, '$LARGE_PROMPT_HEAD'), position(input, '$LARGE_PROMPT_TAIL'), position(input, '[truncated:original_bytes=') FROM observations WHERE trace_id = '$LARGE_TRACE_ID' AND name = 'model.request' LIMIT 1 FORMAT TabSeparated")
LARGE_INPUT_BYTES=$(echo "$LARGE_INPUT_ROW" | cut -f1)
LARGE_HEAD_MATCH=$(echo "$LARGE_INPUT_ROW" | cut -f2)
LARGE_TAIL_MATCH=$(echo "$LARGE_INPUT_ROW" | cut -f3)
LARGE_TRUNCATED_MATCH=$(echo "$LARGE_INPUT_ROW" | cut -f4)
(( LARGE_INPUT_BYTES >= LARGE_PROMPT_TEXT_BYTES && LARGE_INPUT_BYTES <= DEFAULT_PROMPT_MAX_BYTES )) \
  || fail "Langfuse near-limit input length out of range: stored=$LARGE_INPUT_BYTES prompt=$LARGE_PROMPT_TEXT_BYTES limit=$DEFAULT_PROMPT_MAX_BYTES"
[[ "$LARGE_HEAD_MATCH" != "0" && "$LARGE_TAIL_MATCH" != "0" && "$LARGE_TRUNCATED_MATCH" == "0" ]] \
  || fail "Langfuse near-limit input lost a canary or was unexpectedly truncated: head=$LARGE_HEAD_MATCH tail=$LARGE_TAIL_MATCH truncated=$LARGE_TRUNCATED_MATCH"

# 8.5 真实 SSE：一个根 Trace，至少一个真实 attempt Generation，直系父子且终态 completed。
STREAM_TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE session_id = '$STREAM_SESSION_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$STREAM_TRACE_ID" ]] || fail "stream request did not export a trace"
STREAM_TRACE_COUNT=$(clickhouse_query "SELECT count() FROM traces WHERE session_id = '$STREAM_SESSION_ID' FORMAT TabSeparated")
STREAM_OBS_ROW=$(clickhouse_query "SELECT count(), countIf(type = 'GENERATION'), countIf(name = 'model.request' AND type = 'SPAN'), countIf(name = 'upstream.attempt.1' AND type = 'GENERATION' AND metadata['endpoint'] = '127.0.0.1:18081'), countIf(end_time IS NULL) FROM observations WHERE trace_id = '$STREAM_TRACE_ID' FORMAT TabSeparated")
STREAM_OBS_COUNT=$(echo "$STREAM_OBS_ROW" | cut -f1)
STREAM_GEN_COUNT=$(echo "$STREAM_OBS_ROW" | cut -f2)
STREAM_ROOT_MATCH=$(echo "$STREAM_OBS_ROW" | cut -f3)
STREAM_ATTEMPT_MATCH=$(echo "$STREAM_OBS_ROW" | cut -f4)
STREAM_UNFINISHED=$(echo "$STREAM_OBS_ROW" | cut -f5)
[[ "$STREAM_TRACE_COUNT" == "1" && "$STREAM_ROOT_MATCH" == "1" && "$STREAM_ATTEMPT_MATCH" == "1" && "$STREAM_UNFINISHED" == "0" ]] \
  || fail "stream trace cardinality mismatch: traces=$STREAM_TRACE_COUNT observations=$STREAM_OBS_COUNT generations=$STREAM_GEN_COUNT root=$STREAM_ROOT_MATCH attempt=$STREAM_ATTEMPT_MATCH unfinished=$STREAM_UNFINISHED"
STREAM_ROOT_ID=$(clickhouse_query "SELECT id FROM observations WHERE trace_id = '$STREAM_TRACE_ID' AND name = 'model.request' LIMIT 1 FORMAT TabSeparated")
STREAM_CHILD_MATCH=$(clickhouse_query "SELECT count() FROM observations WHERE trace_id = '$STREAM_TRACE_ID' AND name = 'upstream.attempt.1' AND type = 'GENERATION' AND toString(parent_observation_id) = '$STREAM_ROOT_ID' FORMAT TabSeparated")
[[ "$STREAM_CHILD_MATCH" == "1" ]] || fail "stream Generation is not a direct child of root: $STREAM_CHILD_MATCH"
STREAM_STATE_ROW=$(clickhouse_query "SELECT countIf(name = 'model.request' AND JSONExtractString(metadata['attributes'], 'modeltrace.stream.status') = 'completed'), countIf(name = 'model.request' AND level = 'ERROR'), countIf(name = 'model.request' AND position(output, 'data: [DONE]') > 0), countIf(name = 'upstream.attempt.1' AND position(output, 'message_stop') > 0), countIf(name = 'upstream.attempt.1' AND provided_model_name = 'claude-e2e') FROM observations WHERE trace_id = '$STREAM_TRACE_ID' FORMAT TabSeparated")
STREAM_COMPLETED_MATCH=$(echo "$STREAM_STATE_ROW" | cut -f1)
STREAM_ERROR_ROOT=$(echo "$STREAM_STATE_ROW" | cut -f2)
STREAM_CLIENT_TERMINAL=$(echo "$STREAM_STATE_ROW" | cut -f3)
STREAM_UPSTREAM_TERMINAL=$(echo "$STREAM_STATE_ROW" | cut -f4)
STREAM_MODEL_MATCH=$(echo "$STREAM_STATE_ROW" | cut -f5)
[[ "$STREAM_COMPLETED_MATCH" == "1" && "$STREAM_ERROR_ROOT" == "0" && "$STREAM_CLIENT_TERMINAL" == "1" && "$STREAM_UPSTREAM_TERMINAL" == "1" && "$STREAM_MODEL_MATCH" == "1" ]] \
  || fail "stream terminal/model mismatch: completed=$STREAM_COMPLETED_MATCH error_root=$STREAM_ERROR_ROOT client_terminal=$STREAM_CLIENT_TERMINAL upstream_terminal=$STREAM_UPSTREAM_TERMINAL model=$STREAM_MODEL_MATCH"

assert_exact_root_generation() {
  local label="$1" trace_id="$2" root_id row observations roots generations direct unfinished
  root_id=$(clickhouse_query "SELECT id FROM observations WHERE trace_id = '$trace_id' AND name = 'model.request' AND type = 'SPAN' AND isNull(parent_observation_id) LIMIT 1 FORMAT TabSeparated")
  [[ -n "$root_id" ]] || fail "$label has no parentless root span"
  row=$(clickhouse_query "SELECT count(), countIf(name = 'model.request' AND type = 'SPAN' AND isNull(parent_observation_id)), countIf(type = 'GENERATION'), countIf(type = 'GENERATION' AND toString(parent_observation_id) = '$root_id'), countIf(end_time IS NULL) FROM observations WHERE trace_id = '$trace_id' FORMAT TabSeparated")
  IFS=$'\t' read -r observations roots generations direct unfinished <<<"$row"
  [[ "$observations" == "2" && "$roots" == "1" && "$generations" == "1" && "$direct" == "1" && "$unfinished" == "0" ]] \
    || fail "$label hierarchy mismatch: observations=$observations roots=$roots generations=$generations direct_generations=$direct unfinished=$unfinished"
}
assert_stream_terminal() {
  local label="$1" trace_id="$2" expected_status="$3" expected_output="$4" row status_matches errors output_matches
  row=$(clickhouse_query "SELECT countIf(name = 'model.request' AND JSONExtractString(metadata['attributes'], 'modeltrace.stream.status') = '$expected_status'), countIf(name = 'model.request' AND level = 'ERROR'), countIf(name = 'model.request' AND position(ifNull(output, ''), '$expected_output') > 0) FROM observations WHERE trace_id = '$trace_id' FORMAT TabSeparated")
  IFS=$'\t' read -r status_matches errors output_matches <<<"$row"
  [[ "$status_matches" == "1" && "$errors" == "1" && "$output_matches" == "1" ]] \
    || fail "$label terminal mismatch: status=$status_matches errors=$errors partial_output=$output_matches"
}
ws_trace_id_for_turn() {
  local connection_id="$1" turn_index="$2" count row trace_id observed_connection turn_request observed_turn
  count=$(clickhouse_query "SELECT count() FROM traces WHERE coalesce(nullIf(JSONExtractString(metadata['attributes'], 'langfuse.trace.metadata.connection_request_id'), ''), metadata['connection_request_id']) = '$connection_id' AND if(JSONExtractInt(metadata['attributes'], 'langfuse.trace.metadata.turn_index') > 0, JSONExtractInt(metadata['attributes'], 'langfuse.trace.metadata.turn_index'), toInt32OrZero(metadata['turn_index'])) = $turn_index FORMAT TabSeparated")
  [[ "$count" == "1" ]] || fail "Responses WebSocket trace count mismatch: connection=$connection_id turn=$turn_index traces=$count"
  row=$(clickhouse_query "SELECT id, coalesce(nullIf(JSONExtractString(metadata['attributes'], 'langfuse.trace.metadata.connection_request_id'), ''), metadata['connection_request_id']), coalesce(nullIf(JSONExtractString(metadata['attributes'], 'langfuse.trace.metadata.turn_request_id'), ''), metadata['turn_request_id']), if(JSONExtractInt(metadata['attributes'], 'langfuse.trace.metadata.turn_index') > 0, JSONExtractInt(metadata['attributes'], 'langfuse.trace.metadata.turn_index'), toInt32OrZero(metadata['turn_index'])) FROM traces WHERE coalesce(nullIf(JSONExtractString(metadata['attributes'], 'langfuse.trace.metadata.connection_request_id'), ''), metadata['connection_request_id']) = '$connection_id' AND if(JSONExtractInt(metadata['attributes'], 'langfuse.trace.metadata.turn_index') > 0, JSONExtractInt(metadata['attributes'], 'langfuse.trace.metadata.turn_index'), toInt32OrZero(metadata['turn_index'])) = $turn_index FORMAT TabSeparated")
  IFS=$'\t' read -r trace_id observed_connection turn_request observed_turn <<<"$row"
  [[ -n "$trace_id" && "$observed_connection" == "$connection_id" && -n "$turn_request" && "$observed_turn" == "$turn_index" ]] \
    || fail "Responses WebSocket trace metadata mismatch: connection=$connection_id turn=$turn_index row=$row"
  printf '%s\n' "$trace_id"
}

# F-024：真实部分流错误与真实客户端中途断连都保留部分输出、错误终态和已结束父子层级。
PARTIAL_STREAM_TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE metadata['request_id'] = '$PARTIAL_STREAM_REQUEST_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$PARTIAL_STREAM_TRACE_ID" ]] || fail "partial upstream stream did not export a trace"
assert_exact_root_generation "partial upstream stream" "$PARTIAL_STREAM_TRACE_ID"
assert_stream_terminal "partial upstream stream" "$PARTIAL_STREAM_TRACE_ID" "stream_error" "e2e upstream partial"
PARTIAL_ATTEMPT_OUTPUT=$(clickhouse_query "SELECT output FROM observations WHERE trace_id = '$PARTIAL_STREAM_TRACE_ID' AND type = 'GENERATION' FORMAT TabSeparated")
[[ "$PARTIAL_ATTEMPT_OUTPUT" == *"e2e upstream partial"* ]] || fail "partial upstream Generation lost partial output"

CLIENT_DISCONNECT_TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE metadata['request_id'] = '$CLIENT_DISCONNECT_REQUEST_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$CLIENT_DISCONNECT_TRACE_ID" ]] || fail "real client disconnect did not export a trace"
assert_exact_root_generation "real client disconnect" "$CLIENT_DISCONNECT_TRACE_ID"
assert_stream_terminal "real client disconnect" "$CLIENT_DISCONNECT_TRACE_ID" "client_disconnected" "e2e client disconnect partial"

# F-026：按 connection request id、每回合独立 request id 与 turn index 精确检索 WebSocket Trace。
WS_MULTI_TURN_ONE_TRACE_ID=$(ws_trace_id_for_turn "$WS_MULTI_CONNECTION_ID" 1)
WS_MULTI_TURN_TWO_TRACE_ID=$(ws_trace_id_for_turn "$WS_MULTI_CONNECTION_ID" 2)
[[ "$WS_MULTI_TURN_ONE_TRACE_ID" != "$WS_MULTI_TURN_TWO_TRACE_ID" ]] || fail "Responses WebSocket turns shared a Trace unexpectedly"
for pair in "turn-one:$WS_MULTI_TURN_ONE_TRACE_ID:e2e ws turn 1" "turn-two:$WS_MULTI_TURN_TWO_TRACE_ID:e2e ws turn 2"; do
  label=${pair%%:*}
  remainder=${pair#*:}
  trace_id=${remainder%%:*}
  expected_output=${remainder#*:}
  assert_exact_root_generation "Responses WebSocket $label" "$trace_id"
  ws_completed=$(clickhouse_query "SELECT countIf(name = 'model.request' AND JSONExtractString(metadata['attributes'], 'modeltrace.stream.status') = 'completed'), countIf(name = 'model.request' AND level = 'ERROR'), countIf(name = 'model.request' AND position(ifNull(output, ''), '$expected_output') > 0) FROM observations WHERE trace_id = '$trace_id' FORMAT TabSeparated")
  IFS=$'\t' read -r ws_status ws_errors ws_output <<<"$ws_completed"
  [[ "$ws_status" == "1" && "$ws_errors" == "0" && "$ws_output" == "1" ]] || fail "Responses WebSocket $label terminal mismatch: completed=$ws_status errors=$ws_errors output=$ws_output"
done

WS_SWITCH_TURN_ONE_TRACE_ID=$(ws_trace_id_for_turn "$WS_SWITCH_CONNECTION_ID" 1)
assert_exact_root_generation "Responses WebSocket pre-switch turn" "$WS_SWITCH_TURN_ONE_TRACE_ID"
WS_SWITCH_TURN_ONE_STATE=$(clickhouse_query "SELECT countIf(name = 'model.request' AND JSONExtractString(metadata['attributes'], 'modeltrace.stream.status') = 'completed') FROM observations WHERE trace_id = '$WS_SWITCH_TURN_ONE_TRACE_ID' FORMAT TabSeparated")
[[ "$WS_SWITCH_TURN_ONE_STATE" == "1" ]] || fail "Responses WebSocket pre-switch turn did not complete"
WS_SWITCH_TURN_TWO_TRACE_COUNT=$(clickhouse_query "SELECT count() FROM traces WHERE coalesce(nullIf(JSONExtractString(metadata['attributes'], 'langfuse.trace.metadata.connection_request_id'), ''), metadata['connection_request_id']) = '$WS_SWITCH_CONNECTION_ID' AND if(JSONExtractInt(metadata['attributes'], 'langfuse.trace.metadata.turn_index') > 0, JSONExtractInt(metadata['attributes'], 'langfuse.trace.metadata.turn_index'), toInt32OrZero(metadata['turn_index'])) = 2 FORMAT TabSeparated")
[[ "$WS_SWITCH_TURN_TWO_TRACE_COUNT" == "0" ]] || fail "Responses WebSocket post-disable turn unexpectedly exported $WS_SWITCH_TURN_TWO_TRACE_COUNT traces"

WS_DISCONNECT_TRACE_ID=$(ws_trace_id_for_turn "$WS_DISCONNECT_CONNECTION_ID" 1)
assert_exact_root_generation "Responses WebSocket disconnect" "$WS_DISCONNECT_TRACE_ID"
assert_stream_terminal "Responses WebSocket disconnect" "$WS_DISCONNECT_TRACE_ID" "client_disconnected" "e2e ws disconnect partial"

# 8.6 配置切换边界：在途 Trace 使用旧快照完成；关闭后的新请求不落 Trace。
HOLD_TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE metadata['request_id'] = '$HOLD_REQUEST_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$HOLD_TRACE_ID" ]] || fail "in-flight request lost its pre-update trace snapshot"
HOLD_OBS_ROW=$(clickhouse_query "SELECT count(), countIf(name = 'model.request' AND type = 'SPAN'), countIf(type = 'GENERATION'), countIf(end_time IS NULL) FROM observations WHERE trace_id = '$HOLD_TRACE_ID' FORMAT TabSeparated")
HOLD_OBS_COUNT=$(echo "$HOLD_OBS_ROW" | cut -f1)
HOLD_ROOT_COUNT=$(echo "$HOLD_OBS_ROW" | cut -f2)
HOLD_GEN_COUNT=$(echo "$HOLD_OBS_ROW" | cut -f3)
HOLD_UNFINISHED=$(echo "$HOLD_OBS_ROW" | cut -f4)
HOLD_ROOT_ID=$(clickhouse_query "SELECT id FROM observations WHERE trace_id = '$HOLD_TRACE_ID' AND name = 'model.request' LIMIT 1 FORMAT TabSeparated")
HOLD_DIRECT_CHILDREN=$(clickhouse_query "SELECT count() FROM observations WHERE trace_id = '$HOLD_TRACE_ID' AND type = 'GENERATION' AND toString(parent_observation_id) = '$HOLD_ROOT_ID' FORMAT TabSeparated")
[[ "$HOLD_OBS_COUNT" == "2" && "$HOLD_ROOT_COUNT" == "1" && "$HOLD_GEN_COUNT" == "1" && "$HOLD_UNFINISHED" == "0" && "$HOLD_DIRECT_CHILDREN" == "1" ]] \
  || fail "in-flight snapshot trace mismatch: observations=$HOLD_OBS_COUNT root=$HOLD_ROOT_COUNT generations=$HOLD_GEN_COUNT unfinished=$HOLD_UNFINISHED children=$HOLD_DIRECT_CHILDREN"
POST_DISABLE_TRACE_COUNT=$(clickhouse_query "SELECT count() FROM traces WHERE metadata['request_id'] = '$POST_DISABLE_REQUEST_ID' FORMAT TabSeparated")
[[ "$POST_DISABLE_TRACE_COUNT" == "0" ]] || fail "post-disable request unexpectedly exported $POST_DISABLE_TRACE_COUNT traces"
for EXPORT_REQUEST_ID in "$EXPORT_FAIL_REQUEST_ID" "$SLOW_EXPORT_REQUEST_ID"; do
  EXPORT_TO_LANGFUSE_COUNT=$(clickhouse_query "SELECT count() FROM traces WHERE metadata['request_id'] = '$EXPORT_REQUEST_ID' FORMAT TabSeparated")
  [[ "$EXPORT_TO_LANGFUSE_COUNT" == "0" ]] || fail "fixture-targeted export unexpectedly reached Langfuse: request=$EXPORT_REQUEST_ID traces=$EXPORT_TO_LANGFUSE_COUNT"
done


# 8.6 真实异步 batch：配置规模（上限 200）item 的提交根 Span 与 worker Generation 共享一个 Trace，续接、身份及终态正确。
BATCH_TRACE_ID=$(clickhouse_query "SELECT id FROM traces WHERE metadata['task_id'] = '$BATCH_ID' AND metadata['request_id'] = '$BATCH_REQUEST_ID' ORDER BY timestamp DESC LIMIT 1 FORMAT TabSeparated" 2>/dev/null || true)
[[ -n "$BATCH_TRACE_ID" ]] || fail "batch request did not export a continued trace"
BATCH_TRACE_COUNT=$(clickhouse_query "SELECT uniqExact(id) FROM traces WHERE metadata['task_id'] = '$BATCH_ID' AND metadata['request_id'] = '$BATCH_REQUEST_ID' FORMAT TabSeparated")
BATCH_TRACE_IDENTITY=$(clickhouse_query "SELECT user_id, metadata['api_key_id'], metadata['group_id'] FROM traces WHERE id = '$BATCH_TRACE_ID' FORMAT TabSeparated")
IFS=$'\t' read -r BATCH_TRACE_USER_ID BATCH_TRACE_API_KEY_ID BATCH_TRACE_GROUP_ID <<<"$BATCH_TRACE_IDENTITY"
[[ "$BATCH_TRACE_USER_ID" == "$BATCH_USER_ID" && "$BATCH_TRACE_API_KEY_ID" == "$BATCH_API_KEY_ID" && "$BATCH_TRACE_GROUP_ID" == "$BATCH_GROUP_ID" ]] \
  || fail "batch root identity mismatch: user=$BATCH_TRACE_USER_ID api_key=$BATCH_TRACE_API_KEY_ID group=$BATCH_TRACE_GROUP_ID"
BATCH_GENERATION_IDENTITY_COUNT=$(clickhouse_query "SELECT countIf(name = 'model.async.execution' AND type = 'GENERATION' AND metadata['account_id'] = '$BATCH_ACCOUNT_ID' AND metadata['api_key_id'] = '$BATCH_API_KEY_ID' AND metadata['group_id'] = '$BATCH_GROUP_ID') FROM observations WHERE trace_id = '$BATCH_TRACE_ID' FORMAT TabSeparated")
[[ "$BATCH_GENERATION_IDENTITY_COUNT" == "$BATCH_ITEM_COUNT" ]] \
  || fail "batch Generation identity mismatch: matched=$BATCH_GENERATION_IDENTITY_COUNT expected=$BATCH_ITEM_COUNT account=$BATCH_ACCOUNT_ID api_key=$BATCH_API_KEY_ID group=$BATCH_GROUP_ID"
BATCH_OBS_ROW=$(clickhouse_query "SELECT count(), countIf(name = 'model.request' AND type = 'SPAN'), countIf(name = 'model.async.execution' AND type = 'GENERATION'), countIf(name = 'model.async.execution' AND type = 'GENERATION' AND toString(parent_observation_id) = (SELECT id FROM observations WHERE trace_id = '$BATCH_TRACE_ID' AND name = 'model.request' AND type = 'SPAN' LIMIT 1)), countIf(name = 'model.async.execution' AND JSONExtractString(metadata['attributes'], 'modeltrace.async.continuation_matched') = 'true'), countIf(name = 'model.async.execution' AND JSONExtractString(metadata['attributes'], 'modeltrace.async.status') = 'completed'), countIf(name = 'model.async.execution' AND JSONExtractString(metadata['attributes'], 'modeltrace.async.status') = 'failed'), countIf(end_time IS NULL), countIf(name = 'model.async.execution' AND metadata['item_id'] = ''), uniqExactIf(metadata['item_id'], name = 'model.async.execution'), arraySort(groupUniqArrayIf(metadata['item_id'], name = 'model.async.execution')) = arraySort(arrayMap(i -> concat('item-', toString(i)), range($BATCH_ITEM_COUNT))) FROM observations WHERE trace_id = '$BATCH_TRACE_ID' FORMAT TabSeparated")
BATCH_OBS_COUNT=$(echo "$BATCH_OBS_ROW" | cut -f1)
BATCH_ROOT_COUNT=$(echo "$BATCH_OBS_ROW" | cut -f2)
BATCH_GEN_COUNT=$(echo "$BATCH_OBS_ROW" | cut -f3)
BATCH_DIRECT_CHILDREN=$(echo "$BATCH_OBS_ROW" | cut -f4)
BATCH_CONTINUED_COUNT=$(echo "$BATCH_OBS_ROW" | cut -f5)
BATCH_COMPLETED_COUNT=$(echo "$BATCH_OBS_ROW" | cut -f6)
BATCH_FAILED_COUNT=$(echo "$BATCH_OBS_ROW" | cut -f7)
BATCH_UNFINISHED_COUNT=$(echo "$BATCH_OBS_ROW" | cut -f8)
BATCH_EMPTY_ITEM_IDS=$(echo "$BATCH_OBS_ROW" | cut -f9)
BATCH_UNIQUE_ITEM_IDS=$(echo "$BATCH_OBS_ROW" | cut -f10)
BATCH_ITEM_ID_SET_MATCH=$(echo "$BATCH_OBS_ROW" | cut -f11)
EXPECTED_BATCH_OBSERVATIONS=$((BATCH_ITEM_COUNT + 1))
[[ "$BATCH_TRACE_COUNT" == "1" && "$BATCH_OBS_COUNT" == "$EXPECTED_BATCH_OBSERVATIONS" && "$BATCH_ROOT_COUNT" == "1" && "$BATCH_GEN_COUNT" == "$BATCH_ITEM_COUNT" && "$BATCH_DIRECT_CHILDREN" == "$BATCH_ITEM_COUNT" && "$BATCH_CONTINUED_COUNT" == "$BATCH_ITEM_COUNT" && "$BATCH_COMPLETED_COUNT" == "1" && "$BATCH_FAILED_COUNT" == "$EXPECTED_BATCH_FAILED_ITEMS" && "$BATCH_UNFINISHED_COUNT" == "0" && "$BATCH_EMPTY_ITEM_IDS" == "0" && "$BATCH_UNIQUE_ITEM_IDS" == "$BATCH_ITEM_COUNT" && "$BATCH_ITEM_ID_SET_MATCH" == "1" ]] \
  || fail "batch trace mismatch: traces=$BATCH_TRACE_COUNT observations=$BATCH_OBS_COUNT root=$BATCH_ROOT_COUNT generations=$BATCH_GEN_COUNT direct=$BATCH_DIRECT_CHILDREN continued=$BATCH_CONTINUED_COUNT completed=$BATCH_COMPLETED_COUNT failed=$BATCH_FAILED_COUNT unfinished=$BATCH_UNFINISHED_COUNT empty_item_ids=$BATCH_EMPTY_ITEM_IDS unique_item_ids=$BATCH_UNIQUE_ITEM_IDS item_id_set_match=$BATCH_ITEM_ID_SET_MATCH"

# 8.7 全局凭据、上游 secret 与媒体 canary 泄露门禁。
SENSITIVE_HITS=$(clickhouse_query "SELECT count() FROM traces t INNER JOIN observations o ON o.trace_id = t.id WHERE position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$LANGFUSE_SK') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$APIKEY') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$DISABLED_APIKEY') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$FAILOVER_APIKEY') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$BATCH_APIKEY') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$GEMINI_BATCH_API_KEY') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$BATCH_MEDIA_CANARY') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$BATCH_MEDIA_CANARY_B64') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$SECURE_BODY_SECRET') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$SECURE_MEDIA_RAW') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$SECURE_MEDIA_B64') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), 'e2e-upstream-failure-secret') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), 'e2e-upstream-success-secret') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), 'e2e-upstream-fail-key') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), 'e2e-upstream-success-key') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), 'e2e-upstream-stream-key') > 0 FORMAT TabSeparated")
EXTRA_SENSITIVE_HITS=$(clickhouse_query "SELECT count() FROM traces t INNER JOIN observations o ON o.trace_id = t.id WHERE position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$ALL_FAIL_APIKEY') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), '$HOLD_APIKEY') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), 'e2e-all-fail-upstream-1') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), 'e2e-all-fail-upstream-2') > 0 OR position(concat(ifNull(t.input, ''), ifNull(t.output, ''), toString(t.metadata), ifNull(o.input, ''), ifNull(o.output, ''), toString(o.metadata)), 'e2e-hold-upstream') > 0 FORMAT TabSeparated")
TOTAL_SENSITIVE_HITS=$((SENSITIVE_HITS + EXTRA_SENSITIVE_HITS))
[[ "$TOTAL_SENSITIVE_HITS" == "0" ]] || fail "Langfuse stored $TOTAL_SENSITIVE_HITS observations containing a credential or media canary"

if [[ "${RUN_PROTOCOL_MATRIX:-false}" == "true" ]]; then
  log "running real protocol matrix as a required full-scale acceptance gate"
  TOKEN="$TOKEN" REPO_ROOT="$REPO_ROOT" RUN_ID="$RUN_ID-matrix" bash "$SKILL_DIR/scripts/run_protocol_matrix.sh"
  log "protocol matrix passed as a required full-scale acceptance gate"
fi


# 9. 输出
echo "$TRACE_ID"
echo "$OBS_COUNT"
echo "VERIFY_OK"
log "http statuses: langfuse=200 sub2api=200 anonymous=$ANON_CODE unknown=$UNKNOWN_CODE control=$CONTROL_CODE recognized=$HTTP_CODE disabled=$DISABLED_CODE secure=$SECURE_CODE near_limit=$LARGE_CODE failover=$FAILOVER_CODE all_fail=$ALL_FAIL_CODE stream=$STREAM_CODE in_flight=$HOLD_CODE post_disable=$POST_DISABLE_CODE export_500=$EXPORT_FAIL_CODE export_slow=$SLOW_EXPORT_CODE batch=$BATCH_CODE"
log "session grouping passed: session=$SHARED_SESSION_ID traces=$SHARED_SESSION_TRACES cache_only_empty_session=$CACHE_EMPTY_SESSION_COUNT"
log "failover trace passed: trace=$FAILOVER_TRACE_ID observations=$FAILOVER_OBS_COUNT generations=$FAILOVER_GEN_COUNT children=$FAILOVER_CHILDREN"
log "all-attempts-fail trace passed: trace=$ALL_FAIL_TRACE_ID observations=$ALL_FAIL_OBS_COUNT generations=$ALL_FAIL_GEN_COUNT children=$ALL_FAIL_CHILDREN"
log "near-limit trace passed: trace=$LARGE_TRACE_ID observations=$LARGE_OBS_COUNT generations=$LARGE_GEN_COUNT request_bytes=$LARGE_REQUEST_BYTES stored_input_bytes=$LARGE_INPUT_BYTES limit=$DEFAULT_PROMPT_MAX_BYTES truncated=0"
log "stream trace passed: trace=$STREAM_TRACE_ID observations=$STREAM_OBS_COUNT generations=$STREAM_GEN_COUNT direct_attempt_children=$STREAM_CHILD_MATCH status=completed"
log "config snapshot and export fail-open passed: in_flight_trace=$HOLD_TRACE_ID slow_business_ms=$SLOW_ELAPSED_MS"
log "batch trace passed at configured scale: trace=$BATCH_TRACE_ID observations=$BATCH_OBS_COUNT generations=$BATCH_GEN_COUNT direct_async_children=$BATCH_DIRECT_CHILDREN unique_item_ids=$BATCH_UNIQUE_ITEM_IDS completed=1 failed=$EXPECTED_BATCH_FAILED_ITEMS status=$BATCH_STATUS"
log "sensitive-content gate passed: credential_or_media_hits=$TOTAL_SENSITIVE_HITS"
log "full-scale e2e passed: trace=$TRACE_ID observations=$OBS_COUNT; anonymous/unknown/control/post_disable=0 disabled=1 batch_items=$BATCH_ITEM_COUNT"