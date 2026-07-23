# Model Tracing Implementation Log

## 2026-07-23 — Langfuse long-horizon conversation (P0/P1)

### Spec / plan

- Spec: `docs/superpowers/specs/2026-07-23-langfuse-long-horizon-conversation-design.md`
- Plan: `docs/superpowers/plans/2026-07-23-langfuse-long-horizon-conversation.md`

### P0 — Session extraction

- Extended `ExtractLangfuseSessionID` allowlist order:
  1. body `session_id`
  2. body `conversation_id`
  3. `metadata.session_id` / `metadata.user_id.session_id`
  4. `client_metadata.session_id`
  5. `client_metadata.thread_id`
  6. header `session_id` / `session-id`
  7. header `thread_id` / `thread-id`
  8. grok route header `x-grok-conv-id`
- Still excludes `prompt_cache_key`, sticky hashes, content inference.
- WS turn start now passes connection headers into the same extractor.

### P1 — Conversation track

- New packages under `backend/internal/modeltrace/`:
  - `conversation_id.go` — turn_id + message_id helpers
  - `conversation_delta.go` — Responses input/output → `chat.*` deltas + compact detect
  - `langfuse_reader.go` — Public API read-back (`/api/public/traces?sessionId=` then `/api/public/observations?traceId=`)
  - `conversation_writer.go` — emit child spans; wire from HTTP `finish` and WS `End`
  - `fork.go` — explicit `fork_from_*` only (no heuristic in v1)
- Public API base is derived by stripping path from model-tracing OTLP endpoint (e.g. `.../api/public/otel` → origin).
- Read-back failure: warn log, do not block request, write all parsed items for the turn.
- Compact detection only when read-back succeeds.
- Export helper: `scripts/langfuse_session_export.py` (env `LANGFUSE_HOST` / `LANGFUSE_PUBLIC_KEY` / `LANGFUSE_SECRET_KEY`; dedupe by `message_id` earliest; optional `--include-ancestors`).
- `--include-ancestors`: `fork_from_message_id=thread` takes the whole parent line; join drops `chat.fork`; final pass dedupes by `message_id` (keep earliest). Unit tests: `scripts/test_langfuse_session_export.py`.

### Verification

- Unit tests live under `backend/internal/modeltrace/tests/` (`package modeltrace_test`); production package has no `*_test.go`.
- Internal test hooks: `backend/internal/modeltrace/testing_export.go` (`Testing*` only).
- Run: `cd backend && go test ./internal/modeltrace/... -count=1`
- Local stack image: `sub2api:model-trace-b4316e25-forkfix` via `/opt/sub2api-stack/compose.yml`.
- Branching live check: parent unchanged; child emits one `chat.fork` with `fork_from_*`; second child turn does not duplicate fork; `--include-ancestors` export joins parent+child and drops `chat.fork`.

### Bug found in branching verify

- Symptom: child session turn-2 emitted false `chat.compact` because persisted `chat.fork` message_id was treated as a missing content message.
- Fix: compact detection only considers `chat.user` / `chat.assistant` / `chat.tool_call` / `chat.tool_result`; reader now returns `message_id -> observation name`.

### Include `developer` / `system` as `chat.system`

- `ExtractConversationDelta` maps Codex `developer` and OpenAI `system` → `chat.system` (empty content skipped).
- Compact detection treats `chat.system` as content history.
- Image `sub2api:model-trace-b4316e25-chatsys`; real Codex parent wrote `chat.system=2` (main prompt + permissions).

### Bug: parent `chat.assistant` missing on streaming Responses

- Root cause: Codex `/responses` returns SSE; `recordConversationTrack` passed raw SSE to `ExtractConversationDelta`, which only parses JSON `output` arrays → only `chat.user` from request input.
- Fix: `NormalizeConversationOutput` (`conversation_output.go`) extracts `response.completed` / `response.done` → `{"output":[...]}`; fallback to `response.output_item.done` when completed event truncated.
- Wired in `recordConversationTrack` before delta extract.
- Unit tests: `tests/conversation_output_test.go`.
- Real Codex verify: image `sub2api:model-trace-b4316e25-sseout`; parent session wrote `chat.assistant` with marker.

### Real Codex wire mapping (2026-07-23 evening)

- Capture via local header proxy `127.0.0.1:18080` → sub2api `:8080`.
- Codex HTTP headers use dash form: `session-id`, `thread-id`, plus `X-Codex-Turn-Metadata` JSON.
- Body `client_metadata` carries `session_id` / `thread_id` / `turn_id` and the same turn-metadata JSON string under `x-codex-turn-metadata`.
- Fork on the wire is **not** `fork_from_session_id`; it is `forked_from_thread_id` inside turn metadata (app-server local field `forkedFromId` is not sent on `/responses`).
- Compaction is an explicit turn with `request_kind=compaction` (beta `remote_compaction_v2`); missing-content compact alone does not always fire for that request.
- Code updates:
  - `ExtractForkAnnotation(body, headers)` accepts Codex `forked_from_thread_id` / `forkedFromId`; when message id absent, stores `fork_from_message_id=thread`.
  - `IsCodexCompactionRequest` emits `chat.compact` when `request_kind=compaction`.
- Local image: `sub2api:model-trace-b4316e25-codexwire` via `/opt/sub2api-stack/compose.yml`.
- Real Codex verify (app-server `thread/fork` + `turn/start` + `thread/compact/start`):
  - parent session grouped; child session grouped
  - child wrote one `chat.fork` with `fork_from_session_id=<parent>` and `fork_from_message_id=thread`
  - child wrote one `chat.compact` on the compaction turn

### Complex scenario harness (nested fork + multi-compact)

- Synthetic: `scripts/complex_conversation_scenarios.py` (`/v1/responses` against local stack).
- **Real Codex**: `scripts/codex_complex_real_scenarios.py` (app-server fork/turn/compact via proxy `:18080`).
- **Real Codex 20-turn cross-branch**: `scripts/codex_complex_20turn_scenarios.py`
  - Topology: A (turns+compact) → fork B → fork C (nested); sibling fork D←A; then cross-back turns on A/B/C. Entirely via `thread/start` (exec sessions are not visible to app-server).
  - Asserts: fork counts, multi-compact, C genealogy excludes D, D genealogy excludes B/C, ≥20 turns, no export `message_id` dupes.
  - Artifacts: `tmp/codex_20turn_report.json`, `tmp/codex_20turn_C_ancestors_transcript.md`, `tmp/codex_20turn_D_ancestors_transcript.md`.
  - Latest: VERDICT PASS (`T20-1784821818-c8ac`). Soft gap: assistant counts lag users on compacted lines; occasional `<turn_aborted>` noise.

### Credentials

- No real or local test credentials recorded here. Use deployment/runtime config keys `public_key` / `secret_key` only.
