#!/usr/bin/env bash
# 真实 HTTP 协议矩阵：复用 run_e2e.sh 已启动的 sub2api、fixture 和 Langfuse。
set -euo pipefail

# 非流式响应必须先通过客户端 schema 与上游 fixture canary 门禁，才能计入矩阵。
# canary_json 是 JSON 字面量；递归计数确保 fixture canary 在响应中精确出现一次。
assert_protocol_response() {
  local label="$1" contract="$2" canary_json="$3" response_file="$4" diagnostic
  if [[ ! -f "$response_file" ]]; then
    printf '[e2e-matrix][ERROR] case %s response file is missing\n' "$label" >&2
    return 1
  fi
  if ! diagnostic=$(jq -er --arg contract "$contract" --arg canary_json "$canary_json" '
    def nonempty_string:
      if type == "string" then length > 0 else false end;
    def nonnegative_integer:
      if type == "number" then . >= 0 and floor == . else false end;
    def nonempty_array:
      if type == "array" then length > 0 else false end;
    def non_error_object:
      if type == "object" then
        (has("error") | not) and ((.type? // "") != "error")
      else
        false
      end;
    def anthropic_message:
      (.id | nonempty_string) and .type == "message" and .role == "assistant" and
      (.model | nonempty_string) and (.content | nonempty_array) and
      .content[0].type == "text" and (.content[0].text | nonempty_string) and
      (.stop_reason | nonempty_string) and
      (.usage.input_tokens | nonnegative_integer) and
      (.usage.output_tokens | nonnegative_integer);
    def openai_chat_completion:
      (.id | nonempty_string) and .object == "chat.completion" and
      (.model | nonempty_string) and (.choices | nonempty_array) and
      (.choices[0].index | nonnegative_integer) and
      .choices[0].message.role == "assistant" and
      (.choices[0].message.content | nonempty_string) and
      (.choices[0].finish_reason | nonempty_string) and
      (.usage.prompt_tokens | nonnegative_integer) and
      (.usage.completion_tokens | nonnegative_integer) and
      (.usage.total_tokens | nonnegative_integer);
    def openai_response:
      (.id | nonempty_string) and .object == "response" and .status == "completed" and
      (.model | nonempty_string) and (.output | nonempty_array) and
      .output[0].type == "message" and .output[0].role == "assistant" and
      (.output[0].content | nonempty_array) and
      .output[0].content[0].type == "output_text" and
      (.output[0].content[0].text | nonempty_string) and
      (.usage.input_tokens | nonnegative_integer) and
      (.usage.output_tokens | nonnegative_integer) and
      (.usage.total_tokens | nonnegative_integer);
    def openai_embeddings:
      .object == "list" and (.data | nonempty_array) and
      .data[0].object == "embedding" and
      (.data[0].index | nonnegative_integer) and
      (.data[0].embedding | nonempty_array) and
      all((.data[0].embedding // [])[]; type == "number") and
      (.model | nonempty_string) and
      (.usage.prompt_tokens | nonnegative_integer) and
      (.usage.total_tokens | nonnegative_integer);
    def openai_search:
      (.results | nonempty_array) and
      (.results[0].title | nonempty_string) and
      (.results[0].url | nonempty_string) and
      (.results[0].snippet | nonempty_string);
    def anthropic_count_tokens:
      (.input_tokens | nonnegative_integer);
    def openai_image:
      (.created | nonnegative_integer) and (.data | nonempty_array) and
      (.data[0].url | nonempty_string);
    def grok_video_operation:
      (.request_id | nonempty_string) and .status == "pending";
    def gemini_generate_content:
      (.candidates | nonempty_array) and
      .candidates[0].content.role == "model" and
      (.candidates[0].content.parts | nonempty_array) and
      (.candidates[0].content.parts[0].text | nonempty_string) and
      .candidates[0].finishReason == "STOP" and
      (.usageMetadata.promptTokenCount | nonnegative_integer) and
      (.usageMetadata.candidatesTokenCount | nonnegative_integer) and
      (.usageMetadata.totalTokenCount | nonnegative_integer) and
      (.modelVersion | nonempty_string);
    def matches_contract($name):
      if $name == "anthropic.message" then anthropic_message
      elif $name == "openai.chat_completion" then openai_chat_completion
      elif $name == "openai.response" then openai_response
      elif $name == "openai.embeddings" then openai_embeddings
      elif $name == "openai.search" then openai_search
      elif $name == "anthropic.count_tokens" then anthropic_count_tokens
      elif $name == "openai.image" then openai_image
      elif $name == "grok.video_operation" then grok_video_operation
      elif $name == "gemini.generate_content" then gemini_generate_content
      else error("unsupported response contract: \($name)")
      end;
    ($canary_json | fromjson) as $canary
    | if (non_error_object | not) then
        error("expected a non-error JSON object")
      elif (matches_contract($contract) | not) then
        error("required schema mismatch for \($contract)")
      elif ([.. | select(. == $canary)] | length) != 1 then
        error("fixture canary must occur exactly once for \($contract)")
      else
        true
      end
  ' "$response_file" 2>&1); then
    printf '[e2e-matrix][ERROR] case %s response contract %s failed: %s\n' \
      "$label" "$contract" "$diagnostic" >&2
    return 1
  fi
}

record_validated_case() {
  local label="$1" contract="$2" canary_json="$3" response_file="$4"
  local request_id="$5" protocol="$6" account_id="$7"
  assert_protocol_response "$label" "$contract" "$canary_json" "$response_file" || return 1
  record_case "$request_id" "$protocol" "$account_id"
}

# 轻量回归测试只加载上述确定性 helper，不触发账号、网络或容器操作。
if [[ "${PROTOCOL_MATRIX_CONTRACTS_ONLY:-0}" == "1" ]]; then
  return 0 2>/dev/null || exit 0
fi

REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)}"
BASE_URL="http://127.0.0.1:8080"
TOKEN="${TOKEN:?TOKEN is required}"
RUN_ID="${RUN_ID:-$(date +%s)-$$}"
PREFIX="e2e-matrix-${RUN_ID}"
TMP_DIR="$REPO_ROOT/.e2e-tmp/protocol-matrix-$RUN_ID"
mkdir -p "$TMP_DIR"

log() { printf '[e2e-matrix] %s\n' "$*" >&2; }
fail() { printf '[e2e-matrix][ERROR] %s\n' "$*" >&2; exit 1; }
[[ "$RUN_ID" =~ ^[A-Za-z0-9._-]+$ ]] || fail "RUN_ID contains unsupported characters"
curl_secret_header() {
  local header="$1"
  shift
  curl -H @<(printf '%s\n' "$header") "$@"
}
clickhouse_query() {
  local query="$1" basic_auth
  basic_auth=$(printf '%s' "${CLICKHOUSE_USER:-clickhouse}:${CLICKHOUSE_PASSWORD:-clickhouse}" | openssl base64 -A)
  printf '%s' "$query" | curl_secret_header "Authorization: Basic $basic_auth" -fsS \
    --data-binary @- "${CLICKHOUSE_READ_PROXY_URL:-http://127.0.0.1:18123/}"
}
admin_post() {
  local path="$1" payload="$2"
  printf '%s' "$payload" | curl_secret_header "Authorization: Bearer $TOKEN" -fsS -X POST \
    -H "Content-Type: application/json" --data-binary @- "$BASE_URL$path"
}

for cmd in curl jq openssl docker; do
  command -v "$cmd" >/dev/null 2>&1 || fail "missing command: $cmd"
done

# run_e2e.sh 使用 simple mode；该模式会把所有 group 的调度快照合并到 group 0。
# 协议矩阵是完整 E2E 的最后阶段，因此先停用前序场景账号，避免其抢占矩阵请求。
PREEXISTING_SCHEDULABLE_COUNT=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc \
  "SELECT count(*) FROM accounts WHERE deleted_at IS NULL AND schedulable = TRUE")
ISOLATION_RESULT=$(admin_post /api/v1/admin/accounts/bulk-update '{"filters":{},"schedulable":false}')
ISOLATED_ACCOUNT_COUNT=$(echo "$ISOLATION_RESULT" | jq -er '.data.success')
ISOLATION_FAILED_COUNT=$(echo "$ISOLATION_RESULT" | jq -er '.data.failed')
[[ "$ISOLATION_FAILED_COUNT" == "0" ]] || fail "failed to isolate pre-existing accounts: $ISOLATION_FAILED_COUNT"
REMAINING_SCHEDULABLE_COUNT=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc \
  "SELECT count(*) FROM accounts WHERE deleted_at IS NULL AND schedulable = TRUE")
[[ "$REMAINING_SCHEDULABLE_COUNT" == "0" ]] \
  || fail "pre-existing schedulable accounts remain after isolation: $REMAINING_SCHEDULABLE_COUNT"
[[ "$PREEXISTING_SCHEDULABLE_COUNT" == "0" || "$ISOLATED_ACCOUNT_COUNT" -gt 0 ]] \
  || fail "bulk isolation matched no accounts although $PREEXISTING_SCHEDULABLE_COUNT were schedulable"
log "pre-existing accounts isolated: schedulable_before=$PREEXISTING_SCHEDULABLE_COUNT updated=$ISOLATED_ACCOUNT_COUNT"

# simple mode 不按 group 隔离账号；前序账号已停用，矩阵内同平台账号使用不同客户端模型。
setup_platform() {
  local platform="$1" suffix="$2" credentials="$3" extra="$4" allow_image="$5"
  local group_payload group_id key_name api_key account_payload account_id
	group_payload=$(jq -nc --arg name "$PREFIX-$suffix" --arg platform "$platform" --argjson allow_image "$allow_image" '{
		name:$name,description:"model trace protocol matrix",platform:$platform,rate_multiplier:1,
		is_exclusive:false,status:"active",allow_image_generation:$allow_image,
		allow_messages_dispatch:($platform == "openai"),image_price_1k:0.01,
		video_rate_independent:true,video_price_480p:0.02,web_search_price_per_call:0.03
	}')
  group_id=$(admin_post /api/v1/admin/groups "$group_payload" | jq -er '.data.id')
  key_name="$PREFIX-$suffix-key"
  admin_post /api/v1/keys "$(jq -nc --arg name "$key_name" --argjson group_id "$group_id" '{name:$name,group_id:$group_id}')" >/dev/null
  api_key="sk-${PREFIX}-${suffix}-$(openssl rand -hex 8)"
  printf "UPDATE api_keys SET key='%s' WHERE name='%s';\n" "$api_key" "$key_name" | \
    docker exec -i sub2api-deps-postgres-1 psql -U sub2api -d sub2api -v ON_ERROR_STOP=1 >/dev/null
  account_payload=$(printf '%s\n%s\n' "$credentials" "$extra" | jq -cs \
    --arg name "$PREFIX-$suffix-account" --arg platform "$platform" --argjson group_id "$group_id" '{
      name:$name,platform:$platform,type:"apikey",concurrency:4,priority:1,status:"active",schedulable:true,
      group_ids:[$group_id],credentials:.[0],extra:.[1]
    }')
  account_id=$(admin_post /api/v1/admin/accounts "$account_payload" | jq -er '.data.id')
  printf '%s|%s|%s\n' "$group_id" "$api_key" "$account_id"
}

ANTHROPIC_SECRET="matrix-anthropic-secret-$RUN_ID"
OPENAI_SECRET="matrix-openai-secret-$RUN_ID"
OPENAI_CHAT_SECRET="matrix-openai-chat-secret-$RUN_ID"
GROK_SECRET="matrix-grok-secret-$RUN_ID"
GEMINI_SECRET="matrix-gemini-secret-$RUN_ID"
ANTIGRAVITY_SECRET="matrix-antigravity-secret-$RUN_ID"
ANTHROPIC_MODEL="$PREFIX-anthropic-model"
OPENAI_MODEL="$PREFIX-openai-model"
OPENAI_CHAT_MODEL="$PREFIX-openai-chat-model"
GEMINI_MODEL="$PREFIX-gemini-model"
ANTIGRAVITY_GEMINI_MODEL="$PREFIX-antigravity-gemini-model"
ANTIGRAVITY_ANTHROPIC_MODEL="$PREFIX-antigravity-anthropic-model"

ANTHROPIC_CREDENTIALS=$(printf '%s' "$ANTHROPIC_SECRET" | jq -Rsc --arg model "$ANTHROPIC_MODEL" \
  '{api_key:.,base_url:"http://127.0.0.1:18081/matrix/anthropic",model_mapping:{($model):"claude-e2e-upstream"}}')
OPENAI_CREDENTIALS=$(printf '%s' "$OPENAI_SECRET" | jq -Rsc --arg model "$OPENAI_MODEL" \
  '{api_key:.,base_url:"http://127.0.0.1:18081/matrix/openai",openai_capabilities:["chat_completions","embeddings","alpha_search"],model_mapping:{($model):"gpt-e2e-upstream","embed-e2e-matrix":"embed-e2e-upstream","gpt-image-2":"gpt-image-e2e-upstream"}}')
OPENAI_CHAT_CREDENTIALS=$(printf '%s' "$OPENAI_CHAT_SECRET" | jq -Rsc --arg model "$OPENAI_CHAT_MODEL" \
  '{api_key:.,base_url:"http://127.0.0.1:18081/matrix/openai",openai_capabilities:["chat_completions"],model_mapping:{($model):"gpt-e2e-upstream"}}')
GROK_CREDENTIALS=$(printf '%s' "$GROK_SECRET" | jq -Rsc \
  '{api_key:.,base_url:"http://127.0.0.1:18081/matrix/grok/v1",model_mapping:{"grok-imagine":"grok-imagine","grok-imagine-image-quality":"grok-imagine-image-quality","grok-imagine-edit":"grok-imagine-edit","grok-imagine-video":"grok-imagine-video"}}')
GEMINI_CREDENTIALS=$(printf '%s' "$GEMINI_SECRET" | jq -Rsc --arg model "$GEMINI_MODEL" \
  '{api_key:.,base_url:"http://127.0.0.1:18081/matrix/gemini",model_mapping:{($model):"gemini-e2e-upstream"}}')
ANTIGRAVITY_CREDENTIALS=$(printf '%s' "$ANTIGRAVITY_SECRET" | jq -Rsc \
  --arg gemini_model "$ANTIGRAVITY_GEMINI_MODEL" --arg anthropic_model "$ANTIGRAVITY_ANTHROPIC_MODEL" \
  '{api_key:.,base_url:"http://127.0.0.1:18081/matrix/gemini",model_mapping:{($gemini_model):"gemini-e2e-upstream",($anthropic_model):"gemini-e2e-upstream"}}')

IFS='|' read -r ANTHROPIC_GROUP ANTHROPIC_KEY ANTHROPIC_ACCOUNT < <(setup_platform anthropic anthropic "$ANTHROPIC_CREDENTIALS" '{}' false)
IFS='|' read -r OPENAI_GROUP OPENAI_KEY OPENAI_ACCOUNT < <(setup_platform openai openai "$OPENAI_CREDENTIALS" \
  '{"openai_responses_supported":true}' true)
IFS='|' read -r OPENAI_CHAT_GROUP OPENAI_CHAT_KEY OPENAI_CHAT_ACCOUNT < <(setup_platform openai openai-chat "$OPENAI_CHAT_CREDENTIALS" \
  '{"openai_responses_mode":"force_chat_completions","openai_responses_supported":false}' false)
IFS='|' read -r GROK_GROUP GROK_KEY GROK_ACCOUNT < <(setup_platform grok grok "$GROK_CREDENTIALS" \
  '{"grok_media_eligible":true}' true)
IFS='|' read -r GEMINI_GROUP GEMINI_KEY GEMINI_ACCOUNT < <(setup_platform gemini gemini "$GEMINI_CREDENTIALS" '{}' false)
IFS='|' read -r ANTIGRAVITY_GROUP ANTIGRAVITY_KEY ANTIGRAVITY_ACCOUNT < <(setup_platform antigravity antigravity "$ANTIGRAVITY_CREDENTIALS" '{}' false)
unset ANTHROPIC_CREDENTIALS OPENAI_CREDENTIALS OPENAI_CHAT_CREDENTIALS GROK_CREDENTIALS GEMINI_CREDENTIALS ANTIGRAVITY_CREDENTIALS

OPENAI_PROBES_READY=0
for _ in {1..200}; do
  OPENAI_PROBES_READY=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc \
    "SELECT count(*) FROM accounts WHERE (id=$OPENAI_ACCOUNT AND extra->>'openai_responses_supported'='true') OR (id=$OPENAI_CHAT_ACCOUNT AND extra->>'openai_responses_supported'='true' AND extra->>'openai_responses_mode'='force_chat_completions')")
  [[ "$OPENAI_PROBES_READY" == "2" ]] && break
  sleep 0.1
done
[[ "$OPENAI_PROBES_READY" == "2" ]] \
  || fail "OpenAI capability probes did not stabilize native Responses and forced Chat accounts"

log "accounts ready: anthropic=$ANTHROPIC_ACCOUNT openai=$OPENAI_ACCOUNT openai_chat=$OPENAI_CHAT_ACCOUNT grok=$GROK_ACCOUNT gemini=$GEMINI_ACCOUNT antigravity=$ANTIGRAVITY_ACCOUNT"

SCHEDULER_OUTBOX_TARGET=$(docker exec sub2api-deps-postgres-1 psql -U sub2api -d sub2api -tAc \
  "SELECT COALESCE(MAX(id), 0) FROM scheduler_outbox WHERE account_id IN ($ANTHROPIC_ACCOUNT, $OPENAI_ACCOUNT, $OPENAI_CHAT_ACCOUNT, $GROK_ACCOUNT, $GEMINI_ACCOUNT, $ANTIGRAVITY_ACCOUNT)")
SCHEDULER_OUTBOX_WATERMARK=""
for _ in {1..100}; do
  SCHEDULER_OUTBOX_WATERMARK=$(docker exec sub2api-deps-redis-1 redis-cli GET sched:outbox:watermark)
  [[ "$SCHEDULER_OUTBOX_WATERMARK" =~ ^[0-9]+$ ]] && (( SCHEDULER_OUTBOX_WATERMARK >= SCHEDULER_OUTBOX_TARGET )) && break
  sleep 0.1
done
[[ "$SCHEDULER_OUTBOX_WATERMARK" =~ ^[0-9]+$ ]] && (( SCHEDULER_OUTBOX_WATERMARK >= SCHEDULER_OUTBOX_TARGET )) \
  || fail "protocol matrix account scheduler snapshots were not published"
log "account scheduler snapshots published: target=$SCHEDULER_OUTBOX_TARGET watermark=$SCHEDULER_OUTBOX_WATERMARK"
FIXTURE_PROTOCOL_BASELINE=$(curl -fsS http://127.0.0.1:18081/stats | jq -c '.protocol // {}')

CASE_COUNT=0
EXPECTED_MAP="$TMP_DIR/expected.tsv"
: >"$EXPECTED_MAP"
record_case() {
  local request_id="$1" protocol="$2" account_id="$3"
  printf '%s\t%s\t%s\n' "$request_id" "$protocol" "$account_id" >>"$EXPECTED_MAP"
  CASE_COUNT=$((CASE_COUNT + 1))
}
account_for_key() {
  case "$1" in
    "$ANTHROPIC_KEY") printf '%s' "$ANTHROPIC_ACCOUNT" ;;
    "$OPENAI_KEY") printf '%s' "$OPENAI_ACCOUNT" ;;
    "$OPENAI_CHAT_KEY") printf '%s' "$OPENAI_CHAT_ACCOUNT" ;;
    "$GROK_KEY") printf '%s' "$GROK_ACCOUNT" ;;
    "$GEMINI_KEY") printf '%s' "$GEMINI_ACCOUNT" ;;
    "$ANTIGRAVITY_KEY") printf '%s' "$ANTIGRAVITY_ACCOUNT" ;;
    *) fail "unknown matrix API key" ;;
  esac
}
post_json() {
  local label="$1" key="$2" path="$3" protocol="$4" contract="$5" canary_json="$6" payload="$7" auth_header="${8:-Authorization}"
  local request_id="$PREFIX-$label" response="$TMP_DIR/$label.json" code account_id auth_value="$key"
  if [[ "$auth_header" == "Authorization" ]]; then
    auth_value="Bearer $key"
  fi
  code=$(printf '%s' "$payload" | curl_secret_header "$auth_header: $auth_value" -sS -X POST "$BASE_URL$path" \
    -H "Content-Type: application/json" -H "X-Client-Request-ID: $request_id" --data-binary @- \
    -o "$response" -w '%{http_code}')
  [[ "$code" == "200" ]] || { cat "$response" >&2; fail "$label returned HTTP $code"; }
  account_id=$(account_for_key "$key")
  record_validated_case "$label" "$contract" "$canary_json" "$response" \
    "$request_id" "$protocol" "$account_id" || return 1
}

CANARY="matrix-canary-$RUN_ID"
post_json anthropic "$ANTHROPIC_KEY" /v1/messages anthropic.messages \
  anthropic.message '"matrix anthropic"' \
  "$(jq -nc --arg model "$ANTHROPIC_MODEL" --arg prompt "$CANARY-anthropic" '{model:$model,max_tokens:32,messages:[{role:"user",content:$prompt}]}')"
post_json chat "$OPENAI_CHAT_KEY" /v1/chat/completions openai.chat_completions \
  openai.chat_completion '"matrix chat"' \
  "$(jq -nc --arg model "$OPENAI_CHAT_MODEL" --arg prompt "$CANARY-chat" '{model:$model,messages:[{role:"user",content:$prompt}]}')"
post_json responses "$OPENAI_KEY" /v1/responses openai.responses \
  openai.response '"matrix response"' \
  "$(jq -nc --arg model "$OPENAI_MODEL" --arg prompt "$CANARY-responses" '{model:$model,input:$prompt,stream:false}')"
post_json embeddings "$OPENAI_KEY" /v1/embeddings openai.embeddings \
  openai.embeddings '[0.125,-0.25,0.5]' \
  "$(jq -nc --arg prompt "$CANARY-embeddings" '{model:"embed-e2e-matrix",input:$prompt}')"
post_json search "$OPENAI_KEY" /v1/alpha/search openai.search \
  openai.search '"https://example.test/e2e"' \
  "$(jq -nc --arg model "$OPENAI_MODEL" --arg prompt "$CANARY-search" '{model:$model,query:$prompt}')"
post_json anthropic-count "$ANTHROPIC_KEY" /v1/messages/count_tokens anthropic.count_tokens \
  anthropic.count_tokens '11' \
  "$(jq -nc --arg model "$ANTHROPIC_MODEL" --arg prompt "$CANARY-anthropic-count" '{model:$model,messages:[{role:"user",content:$prompt}]}')"
post_json openai-backed-count "$OPENAI_KEY" /v1/messages/count_tokens anthropic.count_tokens \
  anthropic.count_tokens '11' \
  "$(jq -nc --arg model "$OPENAI_MODEL" --arg prompt "$CANARY-openai-backed-count" '{model:$model,messages:[{role:"user",content:$prompt}]}')"
post_json openai-image-generation "$OPENAI_KEY" /v1/images/generations openai.images.generations \
  openai.image '"https://example.test/matrix-generation.png"' \
  "$(jq -nc --arg prompt "$CANARY-openai-image-generation" '{model:"gpt-image-2",prompt:$prompt,size:"1024x1024"}')"

printf '\x89PNG\r\n\x1a\n' >"$TMP_DIR/input.png"
OPENAI_EDIT_ID="$PREFIX-openai-image-edit"
OPENAI_EDIT_CODE=$(curl_secret_header "Authorization: Bearer $OPENAI_KEY" -sS -X POST "$BASE_URL/v1/images/edits" \
  -H "X-Client-Request-ID: $OPENAI_EDIT_ID" -F 'model=gpt-image-2' -F 'size=1024x1024' \
  -F "prompt=$CANARY-openai-image-edit" -F "image=@$TMP_DIR/input.png;type=image/png" \
  -o "$TMP_DIR/openai-image-edit.json" -w '%{http_code}')
[[ "$OPENAI_EDIT_CODE" == "200" ]] || { cat "$TMP_DIR/openai-image-edit.json" >&2; fail "openai image edit returned HTTP $OPENAI_EDIT_CODE"; }
record_validated_case openai-image-edit openai.image '"https://example.test/matrix-edit.png"' \
  "$TMP_DIR/openai-image-edit.json" "$OPENAI_EDIT_ID" openai.images.edits "$OPENAI_ACCOUNT" || exit 1

post_json grok-image-generation "$GROK_KEY" /v1/images/generations openai.images.generations \
  openai.image '"https://example.test/grok-generation.png"' \
  "$(jq -nc --arg prompt "$CANARY-grok-image-generation" '{model:"grok-imagine",prompt:$prompt,size:"1024x1024"}')"
post_json grok-image-edit "$GROK_KEY" /v1/images/edits openai.images.edits \
	openai.image '"https://example.test/grok-edit.png"' \
	"$(jq -nc --arg prompt "$CANARY-grok-image-edit" '{model:"grok-imagine-edit",prompt:$prompt,image:{url:"https://example.test/source.png"},size:"1024x1024"}')"
post_json grok-video-generation "$GROK_KEY" /v1/videos/generations openai.videos.generations \
	grok.video_operation '"video_e2e_generation"' \
	"$(jq -nc --arg prompt "$CANARY-grok-video-generation" '{model:"grok-imagine-video",prompt:$prompt,resolution:"480p",duration:1}')"
post_json grok-video-edit "$GROK_KEY" /v1/videos/edits openai.videos.edits \
	grok.video_operation '"video_e2e_edit"' \
	"$(jq -nc --arg prompt "$CANARY-grok-video-edit" '{model:"grok-imagine-video",prompt:$prompt,video:{url:"https://example.test/source.mp4"},resolution:"480p",duration:1}')"
post_json grok-video-extension "$GROK_KEY" /v1/videos/extensions openai.videos.extensions \
	grok.video_operation '"video_e2e_extension"' \
	"$(jq -nc --arg prompt "$CANARY-grok-video-extension" '{model:"grok-imagine-video",prompt:$prompt,video:{url:"https://example.test/source.mp4"},resolution:"480p",duration:1}')"
post_json gemini "$GEMINI_KEY" "/v1beta/models/$GEMINI_MODEL:generateContent" gemini.generateContent \
  gemini.generate_content '"matrix gemini"' \
  "$(jq -nc --arg prompt "$CANARY-gemini" '{contents:[{role:"user",parts:[{text:$prompt}]}]}')" x-goog-api-key

GEMINI_STREAM_ID="$PREFIX-gemini-stream"
GEMINI_STREAM_PAYLOAD=$(jq -nc --arg prompt "$CANARY-gemini-stream" '{contents:[{role:"user",parts:[{text:$prompt}]}]}')
GEMINI_STREAM_CODE=$(printf '%s' "$GEMINI_STREAM_PAYLOAD" | curl_secret_header "x-goog-api-key: $GEMINI_KEY" -sS -X POST \
  "$BASE_URL/v1beta/models/$GEMINI_MODEL:streamGenerateContent?alt=sse" -H "Content-Type: application/json" \
  -H "X-Client-Request-ID: $GEMINI_STREAM_ID" --data-binary @- \
  -o "$TMP_DIR/gemini-stream.sse" -w '%{http_code}')
unset GEMINI_STREAM_PAYLOAD
[[ "$GEMINI_STREAM_CODE" == "200" ]] || { cat "$TMP_DIR/gemini-stream.sse" >&2; fail "Gemini stream returned HTTP $GEMINI_STREAM_CODE"; }
grep -q 'matrix gemini stream' "$TMP_DIR/gemini-stream.sse" || fail "Gemini stream missing upstream content"
record_case "$GEMINI_STREAM_ID" gemini.streamGenerateContent "$GEMINI_ACCOUNT"

post_json antigravity-native "$ANTIGRAVITY_KEY" /antigravity/v1/messages anthropic.messages \
  anthropic.message '"matrix antigravity"' \
  "$(jq -nc --arg model "$ANTIGRAVITY_ANTHROPIC_MODEL" --arg prompt "$CANARY-antigravity-native" '{model:$model,max_tokens:32,messages:[{role:"user",content:$prompt}]}')"
post_json antigravity-gemini "$ANTIGRAVITY_KEY" "/antigravity/v1beta/models/$ANTIGRAVITY_GEMINI_MODEL:generateContent" gemini.generateContent \
  gemini.generate_content '"matrix gemini"' \
  "$(jq -nc --arg prompt "$CANARY-antigravity-gemini" '{contents:[{role:"user",parts:[{text:$prompt}]}]}')" x-goog-api-key

sort -o "$EXPECTED_MAP" "$EXPECTED_MAP"
log "business responses verified: cases=$CASE_COUNT"

# 上游夹具必须看到每种实际协议；这同时证明账号 base_url、认证与协议转换走到了真实 HTTP 边界。
FIXTURE_STATS=$(curl -fsS http://127.0.0.1:18081/stats)
echo "$FIXTURE_STATS" | jq -e --argjson before "$FIXTURE_PROTOCOL_BASELINE" '
  def delta($name): ((.protocol[$name] // 0) - ($before[$name] // 0));
  delta("anthropic.messages") == 1 and
  delta("openai.chat_completions") == 1 and
  delta("openai.responses") == 1 and
  delta("openai.embeddings") == 1 and
  delta("openai.search") == 1 and
  delta("anthropic.count_tokens") == 1 and
  delta("openai.count_tokens") == 1 and
  delta("openai.images.generations") == 2 and
  delta("openai.images.edits") == 2 and
  delta("openai.videos.generations") == 1 and
  delta("openai.videos.edits") == 1 and
  delta("openai.videos.extensions") == 1 and
  delta("gemini.generateContent") == 1 and
  delta("gemini.streamGenerateContent") == 1 and
  delta("antigravity.messages") == 1 and
  delta("antigravity.generateContent") == 1
' >/dev/null || { echo "$FIXTURE_STATS" | jq . >&2; fail "upstream protocol coverage delta mismatch"; }

# 等待 BatchSpanProcessor + Langfuse ingestion。
TRACE_COUNT=0
for _ in {1..30}; do
  TRACE_COUNT=$(clickhouse_query "SELECT count() FROM traces WHERE startsWith(metadata['request_id'], '$PREFIX-') FORMAT TabSeparated" 2>/dev/null || true)
  [[ "$TRACE_COUNT" == "$CASE_COUNT" ]] && break
  sleep 1
done
[[ "$TRACE_COUNT" == "$CASE_COUNT" ]] || fail "Langfuse trace count=$TRACE_COUNT, want $CASE_COUNT"

ACTUAL_MAP="$TMP_DIR/actual.tsv"
clickhouse_query "SELECT t.metadata['request_id'], t.metadata['entry_protocol'], anyIf(o.metadata['account_id'], o.type = 'GENERATION') FROM traces AS t INNER JOIN observations AS o ON o.trace_id = t.id WHERE startsWith(t.metadata['request_id'], '$PREFIX-') GROUP BY t.id, t.metadata['request_id'], t.metadata['entry_protocol'] ORDER BY t.metadata['request_id'] FORMAT TabSeparated" >"$ACTUAL_MAP"
cmp -s "$EXPECTED_MAP" "$ACTUAL_MAP" || { diff -u "$EXPECTED_MAP" "$ACTUAL_MAP" >&2 || true; fail "request-to-protocol-account mapping mismatch"; }

# 根与 Generation 层级必须逐请求精确验证；跨协议总量只作诊断，绝不能替代父子关系。
HIERARCHY_MAP="$TMP_DIR/hierarchy.tsv"
clickhouse_query "SELECT t.metadata['request_id'], countIf(o.name = 'model.request' AND o.type = 'SPAN' AND isNull(o.parent_observation_id)), countIf(o.type = 'GENERATION'), countIf(o.type = 'GENERATION' AND toString(o.parent_observation_id) = roots.root_id), countIf(o.end_time IS NULL) FROM traces AS t INNER JOIN observations AS o ON o.trace_id = t.id INNER JOIN (SELECT trace_id, anyIf(id, name = 'model.request' AND type = 'SPAN' AND isNull(parent_observation_id)) AS root_id FROM observations WHERE trace_id IN (SELECT id FROM traces WHERE startsWith(metadata['request_id'], '$PREFIX-')) GROUP BY trace_id) AS roots ON roots.trace_id = t.id WHERE startsWith(t.metadata['request_id'], '$PREFIX-') GROUP BY t.id, t.metadata['request_id'], roots.root_id ORDER BY t.metadata['request_id'] FORMAT TabSeparated" >"$HIERARCHY_MAP"
HIERARCHY_COUNT=0
while IFS=$'\t' read -r REQUEST_ID ROOTS GENERATIONS DIRECT_GENERATIONS UNFINISHED; do
  [[ -n "$REQUEST_ID" ]] || fail "empty request id in per-trace hierarchy result"
  [[ "$ROOTS" == "1" && "$GENERATIONS" == "1" && "$DIRECT_GENERATIONS" == "1" && "$UNFINISHED" == "0" ]] \
    || fail "per-request hierarchy mismatch: request=$REQUEST_ID roots=$ROOTS generations=$GENERATIONS direct_generations=$DIRECT_GENERATIONS unfinished=$UNFINISHED"
  HIERARCHY_COUNT=$((HIERARCHY_COUNT + 1))
done <"$HIERARCHY_MAP"
[[ "$HIERARCHY_COUNT" == "$CASE_COUNT" ]] \
  || fail "per-request hierarchy row count=$HIERARCHY_COUNT, want $CASE_COUNT"
CANARY_MAP="$TMP_DIR/canary.tsv"
clickhouse_query "SELECT t.metadata['request_id'], countIf(position(toString(o.input), replaceOne(t.metadata['request_id'], '$PREFIX-', '$CANARY-')) > 0) FROM traces AS t INNER JOIN observations AS o ON o.trace_id = t.id WHERE startsWith(t.metadata['request_id'], '$PREFIX-') GROUP BY t.metadata['request_id'] ORDER BY t.metadata['request_id'] FORMAT TabSeparated" >"$CANARY_MAP"
CANARY_REQUEST_COUNT=0
while IFS=$'\t' read -r REQUEST_ID INPUT_MATCHES; do
  [[ -n "$REQUEST_ID" && "$INPUT_MATCHES" -ge 1 ]] \
    || fail "trace input lost unique prompt: request=$REQUEST_ID matches=$INPUT_MATCHES"
  CANARY_REQUEST_COUNT=$((CANARY_REQUEST_COUNT + 1))
done <"$CANARY_MAP"
[[ "$CANARY_REQUEST_COUNT" == "$CASE_COUNT" ]] \
  || fail "per-request canary row count=$CANARY_REQUEST_COUNT, want $CASE_COUNT"

OBS_ROW=$(clickhouse_query "SELECT countIf(name = 'model.request' AND type = 'SPAN'), countIf(type = 'GENERATION'), countIf(end_time IS NULL), countIf(level = 'ERROR') FROM observations WHERE trace_id IN (SELECT id FROM traces WHERE startsWith(metadata['request_id'], '$PREFIX-')) FORMAT TabSeparated")
IFS=$'\t' read -r ROOT_COUNT GENERATION_COUNT UNFINISHED_COUNT ERROR_COUNT <<<"$OBS_ROW"
[[ "$ROOT_COUNT" == "$CASE_COUNT" && "$GENERATION_COUNT" == "$CASE_COUNT" && "$UNFINISHED_COUNT" == "0" && "$ERROR_COUNT" == "0" ]] \
  || fail "protocol matrix summary mismatch after per-request hierarchy validation: roots=$ROOT_COUNT generations=$GENERATION_COUNT unfinished=$UNFINISHED_COUNT errors=$ERROR_COUNT"

# 已知 usage 的协议逐项对齐上游事实；total cost 字段必须存在（允许免费测试组为 0）。
assert_usage() {
  local label="$1" input="$2" output="$3" total="$4" row
  row=$(clickhouse_query "SELECT countIf(usage_details['input'] = $input AND usage_details['output'] = $output AND usage_details['total'] = $total AND mapContains(cost_details, 'total')) FROM observations WHERE trace_id IN (SELECT id FROM traces WHERE metadata['request_id'] = '$PREFIX-$label') AND type = 'GENERATION' FORMAT TabSeparated")
  [[ "$row" == "1" ]] || fail "$label usage/cost mismatch: got=$row want=1 ($input/$output/$total)"
}
assert_usage anthropic 11 3 14
assert_usage chat 12 4 16
assert_usage responses 13 5 18
assert_usage embeddings 3 0 3
assert_usage gemini 17 4 21
assert_usage gemini-stream 17 4 21
assert_usage antigravity-native 11 3 14
assert_usage antigravity-gemini 17 4 21
assert_usage_and_cost() {
	local label="$1" input="$2" output="$3" total="$4" cost="$5" row
	row=$(clickhouse_query "SELECT countIf(usage_details['input'] = $input AND usage_details['output'] = $output AND usage_details['total'] = $total AND abs(cost_details['total'] - $cost) < 0.000000001) FROM observations WHERE trace_id IN (SELECT id FROM traces WHERE metadata['request_id'] = '$PREFIX-$label') AND type = 'GENERATION' FORMAT TabSeparated")
	[[ "$row" == "1" ]] || fail "$label usage/cost mismatch: got=$row want=1 ($input/$output/$total cost=$cost)"
}
assert_no_usage() {
	local label="$1" row
	row=$(clickhouse_query "SELECT countIf(NOT mapContains(usage_details, 'total') AND NOT mapContains(cost_details, 'total')) FROM observations WHERE trace_id IN (SELECT id FROM traces WHERE metadata['request_id'] = '$PREFIX-$label') AND type = 'GENERATION' FORMAT TabSeparated")
	[[ "$row" == "1" ]] || fail "$label unexpectedly recorded usage or cost: got=$row want=1"
}
assert_usage_and_cost search 0 0 0 0.03
assert_no_usage anthropic-count
assert_no_usage openai-backed-count
assert_usage_and_cost openai-image-generation 0 0 0 0.01
assert_usage_and_cost openai-image-edit 0 0 0 0.01
assert_usage_and_cost grok-image-generation 0 0 0 0.01
assert_usage_and_cost grok-image-edit 0 0 0 0.01
assert_usage_and_cost grok-video-generation 0 0 0 0.02
assert_usage_and_cost grok-video-edit 0 0 0 0.02
assert_usage_and_cost grok-video-extension 0 0 0 0.02

for secret in "$ANTHROPIC_SECRET" "$OPENAI_SECRET" "$OPENAI_CHAT_SECRET" "$GROK_SECRET" "$GEMINI_SECRET" "$ANTIGRAVITY_SECRET" "$ANTHROPIC_KEY" "$OPENAI_KEY" "$OPENAI_CHAT_KEY" "$GROK_KEY" "$GEMINI_KEY" "$ANTIGRAVITY_KEY"; do
  LEAKS=$(clickhouse_query "SELECT (SELECT count() FROM traces WHERE startsWith(metadata['request_id'], '$PREFIX-') AND position(toString(metadata), '$secret') > 0) + (SELECT count() FROM observations WHERE trace_id IN (SELECT id FROM traces WHERE startsWith(metadata['request_id'], '$PREFIX-')) AND (position(input, '$secret') > 0 OR position(output, '$secret') > 0 OR position(toString(metadata), '$secret') > 0)) FORMAT TabSeparated")
  [[ "$LEAKS" == "0" ]] || fail "credential canary leaked into Langfuse: matches=$LEAKS"
done

log "Langfuse protocol matrix verified: traces=$TRACE_COUNT roots=$ROOT_COUNT generations=$GENERATION_COUNT"
printf 'MATRIX_TRACE_COUNT=%s\nMATRIX_GENERATION_COUNT=%s\nPROTOCOL_MATRIX_VERIFY_OK\n' "$TRACE_COUNT" "$GENERATION_COUNT"
