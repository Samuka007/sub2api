#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MATRIX_SCRIPT="$SCRIPT_DIR/run_protocol_matrix.sh"
PROTOCOL_MATRIX_CONTRACTS_ONLY=1 source "$MATRIX_SCRIPT"

fail() {
  printf '[protocol-response-contract-test][ERROR] %s\n' "$*" >&2
  exit 1
}

TEST_TMP="$(mktemp -d "${TMPDIR:-/tmp}/protocol-response-contracts.XXXXXX")"
trap 'rm -rf "$TEST_TMP"' EXIT

expect_pass() {
  local label="$1" contract="$2" canary_json="$3" payload="$4"
  local response_file="$TEST_TMP/$label.json" output
  printf '%s' "$payload" >"$response_file"
  if ! output=$(assert_protocol_response "$label" "$contract" "$canary_json" "$response_file" 2>&1); then
    fail "$label unexpectedly failed: $output"
  fi
}

expect_file_fail() {
  local label="$1" contract="$2" canary_json="$3" response_file="$4" output
  if output=$(assert_protocol_response "$label" "$contract" "$canary_json" "$response_file" 2>&1); then
    fail "$label unexpectedly passed"
  fi
  [[ "$output" == *"case $label"* ]] || fail "$label diagnostic did not identify the case: $output"
}

expect_payload_fail() {
  local label="$1" contract="$2" canary_json="$3" payload="$4"
  local response_file="$TEST_TMP/$label.json"
  printf '%s' "$payload" >"$response_file"
  expect_file_fail "$label" "$contract" "$canary_json" "$response_file"
}

expect_renamed_field_fail() {
  local label="$1" contract="$2" canary_json="$3" source_label="$4" mutation="$5"
  local response_file="$TEST_TMP/$label.json"
  jq "$mutation" "$TEST_TMP/$source_label.json" >"$response_file"
  expect_file_fail "$label" "$contract" "$canary_json" "$response_file"
}

expect_pass anthropic anthropic.message '"matrix anthropic"' \
  '{"id":"msg_e2e_matrix","type":"message","role":"assistant","model":"claude-e2e-upstream","content":[{"type":"text","text":"matrix anthropic"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":3}}'
expect_pass chat openai.chat_completion '"matrix chat"' \
  '{"id":"chatcmpl_e2e_matrix","object":"chat.completion","model":"gpt-e2e-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"matrix chat"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}}'
expect_pass responses openai.response '"matrix response"' \
  '{"id":"resp_e2e_matrix","object":"response","status":"completed","model":"gpt-e2e-upstream","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"matrix response"}]}],"usage":{"input_tokens":13,"output_tokens":5,"total_tokens":18}}'
expect_pass embeddings openai.embeddings '[0.125,-0.25,0.5]' \
  '{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.125,-0.25,0.5]}],"model":"embed-e2e-upstream","usage":{"prompt_tokens":3,"total_tokens":3}}'
expect_pass search openai.search '"https://example.test/e2e"' \
  '{"results":[{"title":"matrix result","url":"https://example.test/e2e","snippet":"deterministic search"}]}'
expect_pass anthropic-count anthropic.count_tokens '11' \
  '{"input_tokens":11}'
expect_pass openai-backed-count anthropic.count_tokens '11' \
  '{"input_tokens":11}'
expect_pass openai-image-generation openai.image '"https://example.test/matrix-generation.png"' \
  '{"created":1784690000,"data":[{"url":"https://example.test/matrix-generation.png","revised_prompt":"matrix image generation"}]}'
expect_pass openai-image-edit openai.image '"https://example.test/matrix-edit.png"' \
  '{"created":1784690001,"data":[{"url":"https://example.test/matrix-edit.png","revised_prompt":"matrix image edit"}]}'
expect_pass grok-image-generation openai.image '"https://example.test/grok-generation.png"' \
  '{"created":1784690002,"data":[{"url":"https://example.test/grok-generation.png"}]}'
expect_pass grok-image-edit openai.image '"https://example.test/grok-edit.png"' \
  '{"created":1784690003,"data":[{"url":"https://example.test/grok-edit.png"}]}'
expect_pass grok-video-generation grok.video_operation '"video_e2e_generation"' \
  '{"request_id":"video_e2e_generation","status":"pending"}'
expect_pass grok-video-edit grok.video_operation '"video_e2e_edit"' \
  '{"request_id":"video_e2e_edit","status":"pending"}'
expect_pass grok-video-extension grok.video_operation '"video_e2e_extension"' \
  '{"request_id":"video_e2e_extension","status":"pending"}'
expect_pass gemini gemini.generate_content '"matrix gemini"' \
  '{"candidates":[{"content":{"role":"model","parts":[{"text":"matrix gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":17,"candidatesTokenCount":4,"totalTokenCount":21},"modelVersion":"gemini-e2e-upstream"}'
expect_pass antigravity-native anthropic.message '"matrix antigravity"' \
  '{"id":"msg_e2e_antigravity_matrix","type":"message","role":"assistant","model":"gemini-e2e-upstream","content":[{"type":"text","text":"matrix antigravity"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":3}}'
expect_pass antigravity-gemini gemini.generate_content '"matrix gemini"' \
  '{"candidates":[{"content":{"role":"model","parts":[{"text":"matrix gemini"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":17,"candidatesTokenCount":4,"totalTokenCount":21},"modelVersion":"gemini-e2e-upstream"}'

expect_payload_fail empty-object openai.response '"matrix response"' '{}'

ERROR_BODY_CANARY='response-body-secret-canary'
ERROR_RESPONSE_FILE="$TEST_TMP/http-200-error-object.json"
printf '%s' '{"type":"error","error":{"type":"upstream_error","message":"response-body-secret-canary"}}' >"$ERROR_RESPONSE_FILE"
ERROR_OUTPUT=""
if ERROR_OUTPUT=$(assert_protocol_response http-200-error-object openai.response '"matrix response"' "$ERROR_RESPONSE_FILE" 2>&1); then
  fail 'HTTP 200 error object unexpectedly passed'
fi
[[ "$ERROR_OUTPUT" == *'case http-200-error-object'* ]] \
  || fail "HTTP 200 error diagnostic did not identify the case: $ERROR_OUTPUT"
[[ "$ERROR_OUTPUT" != *"$ERROR_BODY_CANARY"* ]] \
  || fail 'HTTP 200 error diagnostic leaked the response body'

expect_renamed_field_fail renamed-anthropic anthropic.message '"matrix anthropic"' anthropic \
  '.contents = .content | del(.content)'
expect_renamed_field_fail renamed-chat openai.chat_completion '"matrix chat"' chat \
  '.options = .choices | del(.choices)'
expect_renamed_field_fail renamed-responses openai.response '"matrix response"' responses \
  '.outputs = .output | del(.output)'
expect_renamed_field_fail renamed-embeddings openai.embeddings '[0.125,-0.25,0.5]' embeddings \
  '.vectors = .data | del(.data)'
expect_renamed_field_fail renamed-search openai.search '"https://example.test/e2e"' search \
  '.items = .results | del(.results)'
expect_renamed_field_fail renamed-count anthropic.count_tokens '11' anthropic-count \
  '.tokens = .input_tokens | del(.input_tokens)'
expect_renamed_field_fail renamed-image openai.image '"https://example.test/matrix-generation.png"' openai-image-generation \
  '.items = .data | del(.data)'
expect_renamed_field_fail renamed-video grok.video_operation '"video_e2e_generation"' grok-video-generation \
  '.id = .request_id | del(.request_id)'
expect_renamed_field_fail renamed-gemini gemini.generate_content '"matrix gemini"' gemini \
  '.choices = .candidates | del(.candidates)'

DUPLICATE_CANARY_FILE="$TEST_TMP/duplicate-canary.json"
jq '.duplicate = .content[0].text' "$TEST_TMP/anthropic.json" >"$DUPLICATE_CANARY_FILE"
expect_file_fail duplicate-canary anthropic.message '"matrix anthropic"' "$DUPLICATE_CANARY_FILE"
expect_file_fail missing-canary anthropic.message '"different fixture output"' "$TEST_TMP/anthropic.json"
expect_file_fail unknown-contract unknown.contract '"matrix anthropic"' "$TEST_TMP/anthropic.json"

RECORDED_CASES=0
RECORDED_ROW=""
record_case() {
  RECORDED_CASES=$((RECORDED_CASES + 1))
  RECORDED_ROW="$1|$2|$3"
}
record_validated_case record-gate-valid anthropic.message '"matrix anthropic"' \
  "$TEST_TMP/anthropic.json" request-valid anthropic.messages 101
[[ "$RECORDED_CASES" == "1" && "$RECORDED_ROW" == 'request-valid|anthropic.messages|101' ]] \
  || fail 'validated response was not recorded exactly once'
RECORD_GATE_OUTPUT=""
if RECORD_GATE_OUTPUT=$(record_validated_case record-gate-invalid openai.response '"matrix response"' \
  "$TEST_TMP/empty-object.json" request-invalid openai.responses 102 2>&1); then
  fail 'invalid response reached record_case'
fi
[[ "$RECORD_GATE_OUTPUT" == *'case record-gate-invalid'* ]] \
  || fail "record gate diagnostic did not identify the case: $RECORD_GATE_OUTPUT"
[[ "$RECORDED_CASES" == "1" && "$RECORDED_ROW" == 'request-valid|anthropic.messages|101' ]] \
  || fail 'invalid response changed the recorded case count'

printf 'protocol response contracts passed\n'
