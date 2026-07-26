# Langfuse Long-Horizon Conversation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Group Codex/compatible multi-turn requests into Langfuse Sessions (P0), then append-only `chat.*` conversation events with Public API dedupe and export (P1).

**Architecture:** Extend `ExtractLangfuseSessionID` for Codex body/header identifiers. Add a conversation track that, after each turn, reads existing `message_id`s from Langfuse Public API, computes deltas from request input/output, and emits `chat.*` child spans on the same session-scoped trace. Export script reconstructs a linear transcript with `message_id` dedupe.

**Tech Stack:** Go, `gjson`, OpenTelemetry OTLP/HTTP → Langfuse, Langfuse Public API (Basic auth with existing pk/sk), `testing` + testify.

## Global Constraints

- Storage: Langfuse only (no separate conversation DB).
- Session IDs: explicit client-owned identifiers only; never `prompt_cache_key`, sticky hashes, or content inference.
- Read-back failure: do not block model requests; still write all parsed chat items for the turn.
- Export: dedupe by `message_id`, keep earliest observation.
- Delivery: P0 session grouping first; P1 conversation track second.
- No production hardcoding of local Langfuse credentials.
- Skip git commits unless the user explicitly asks.

## File structure

| File | Responsibility |
|---|---|
| `backend/internal/modeltrace/session.go` | Session ID extraction allowlist |
| `backend/internal/modeltrace/session_test.go` | Session extractor unit tests |
| `backend/internal/modeltrace/conversation_id.go` | `message_id` / `turn_id` helpers |
| `backend/internal/modeltrace/conversation_delta.go` | Parse turn → canonical chat items + compact detect |
| `backend/internal/modeltrace/conversation_writer.go` | Emit `chat.*` child spans |
| `backend/internal/modeltrace/langfuse_reader.go` | Public API read-back of `message_id`s |
| `backend/internal/modeltrace/fork.go` | Fork metadata / `chat.fork` |
| `backend/internal/modeltrace/middleware.go` / `ws_turn.go` / `recorder.go` | Hook conversation track after turn complete |
| `scripts/langfuse_session_export.py` (or `.go` under `backend/cmd/`) | Export helper |
| `docs/MODEL_TRACING_IMPLEMENTATION_LOG.md` | Persist runtime facts / task notes |

---

### Task 1: P0 — Extend SessionExtractor

**Files:**
- Modify: `backend/internal/modeltrace/session.go`
- Modify: `backend/internal/modeltrace/session_test.go`

**Interfaces:**
- Consumes: existing `ExtractLangfuseSessionID(body []byte, headers http.Header, grokRoute bool) string`
- Produces: same signature; expanded allowlist order per spec

- [ ] **Step 1: Write the failing tests**

Add cases to `TestLangfuseSessionExtractor`:

```go
{name: "client_metadata session", body: `{"client_metadata":{"session_id":"cm-session-1"}}`, want: "cm-session-1"},
{name: "client_metadata thread fallback", body: `{"client_metadata":{"thread_id":"cm-thread-1"}}`, want: "cm-thread-1"},
{name: "client_metadata session wins over thread", body: `{"client_metadata":{"session_id":"cm-session-2","thread_id":"cm-thread-2"}}`, want: "cm-session-2"},
{name: "header session_id", header: http.Header{"Session_id": []string{"hdr-session-1"}}, want: "hdr-session-1"},
{name: "header session-id", header: http.Header{"Session-Id": []string{"hdr-session-dash"}}, want: "hdr-session-dash"},
{name: "header thread_id fallback", header: http.Header{"Thread_id": []string{"hdr-thread-1"}}, want: "hdr-thread-1"},
{name: "body session wins over header", body: `{"session_id":"body-1"}`, header: http.Header{"Session_id": []string{"hdr-1"}}, want: "body-1"},
{name: "client_metadata wins over header", body: `{"client_metadata":{"session_id":"cm-1"}}`, header: http.Header{"Session_id": []string{"hdr-1"}}, want: "cm-1"},
```

Keep existing negative cases (`prompt_cache_key`, sticky hash, etc.).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd backend && go test ./internal/modeltrace/ -run TestLangfuseSessionExtractor -count=1`

Expected: FAIL on new cases (empty got).

- [ ] **Step 3: Write minimal implementation**

Update `session.go` paths to:

```go
"session_id",
"conversation_id",
"metadata.session_id",
"metadata.user_id.session_id",
"client_metadata.session_id",
"client_metadata.thread_id",
```

After body loop, before grok:

```go
if headers != nil {
  for _, key := range []string{"session_id", "session-id", "thread_id", "thread-id"} {
    if sessionID := strings.TrimSpace(headers.Get(key)); sessionID != "" {
      return sessionID
    }
  }
}
```

Preserve grok header last.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd backend && go test ./internal/modeltrace/ -run TestLangfuseSessionExtractor -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

Skip unless user asks.

---

### Task 2: P1 — turn_id + message_id helpers

**Files:**
- Create: `backend/internal/modeltrace/conversation_id.go`
- Create: `backend/internal/modeltrace/conversation_id_test.go`

**Interfaces:**
- Produces:
  - `func ExtractLangfuseTurnID(body []byte, headers http.Header) string`
  - `func MessageIDFromProtocolOrHash(protocolID string, canonicalJSON []byte) string`

- [ ] **Step 1: Failing tests** for turn_id from `client_metadata.turn_id`, from `X-Codex-Turn-Metadata` JSON, from `client_metadata.x-codex-turn-metadata` JSON; empty → empty (caller generates). message_id prefers non-empty protocol ID; else stable sha256 hex of canonical JSON.

- [ ] **Step 2: Run fail** → **Step 3: Implement** → **Step 4: Pass**

---

### Task 3: P1 — ConversationDeltaExtractor

**Files:**
- Create: `backend/internal/modeltrace/conversation_delta.go`
- Create: `backend/internal/modeltrace/conversation_delta_test.go`

**Interfaces:**
- Produces:
  - `type ChatEvent struct { Name, MessageID, TurnID, SessionID, ParentMessageID, CallID, NameTool, Content string; Seq int }`
  - `func ExtractConversationDelta(sessionID, turnID string, input, output []byte, existing map[string]struct{}) (events []ChatEvent, compact bool)`

Parse Responses-style `input` arrays (message / function_call / function_call_output) and final assistant/tool outputs. Skip bootstrap-only developer instructions when clearly static. Set `compact=true` only when `existing` non-nil and a previously seen user/assistant/tool id is absent from this turn’s input set.

- [ ] TDD cycle as above with fixtures for two-turn delta, tool pair, compact detection.

---

### Task 4: P1 — LangfuseObservationReader

**Files:**
- Create: `backend/internal/modeltrace/langfuse_reader.go`
- Create: `backend/internal/modeltrace/langfuse_reader_test.go`

**Interfaces:**
- Produces:
  - `type LangfuseReader struct { BaseURL string; PublicKey string; SecretKey string; HTTPClient *http.Client }`
  - `func (r *LangfuseReader) ListChatMessageIDs(ctx context.Context, sessionID string) (map[string]struct{}, error)`

Use Langfuse Public API: list traces by `sessionId`, then observations (or traces with observations) and collect metadata `message_id` where observation name has prefix `chat.`. Basic auth = `publicKey:secretKey`. Derive API base by stripping `/api/public/otel` suffix from model-tracing endpoint when present.

- [ ] TDD with `httptest.Server` fixtures.

---

### Task 5: P1 — ConversationEventWriter + wire into turn completion

**Files:**
- Create: `backend/internal/modeltrace/conversation_writer.go`
- Create: `backend/internal/modeltrace/conversation_writer_test.go`
- Modify: `backend/internal/modeltrace/middleware.go` (HTTP finish path)
- Modify: `backend/internal/modeltrace/ws_turn.go` (WS turn end path)
- Modify: `backend/internal/modeltrace/recorder.go` if shared finish helper fits better

**Interfaces:**
- Produces: `func WriteConversationEvents(ctx context.Context, tracer trace.Tracer, parent trace.Span, events []ChatEvent)`
- Each event → child span named `chat.user` etc. with `langfuse.observation.type=span` (or event), metadata JSON fields, `langfuse.observation.input`/`output` for content.

On turn complete (after root attrs set, before span end):
1. Resolve session/turn IDs
2. `ListChatMessageIDs` (on error → empty existing + warn log; write all parsed items)
3. `ExtractConversationDelta`
4. `WriteConversationEvents`
5. Optional fork annotate when fork metadata present in body

Must never return error to the client path; all fail-open except model request itself.

- [ ] TDD with fake OTLP collector asserting child span names + session id.

---

### Task 6: P1 — ForkAnnotator

**Files:**
- Create: `backend/internal/modeltrace/fork.go`
- Create: `backend/internal/modeltrace/fork_test.go`

**Interfaces:**
- Produces: `func ExtractForkAnnotation(body []byte) (forkFromSessionID, forkFromMessageID, forkFromTurnID string)`
- Writer emits `chat.fork` once when annotation present on first turn of `S_new`.

Explicit body/metadata fields only for v1; heuristic optional and off by default.

---

### Task 7: P1 — Export helper

**Files:**
- Create: `scripts/langfuse_session_export.py`

**Behavior:** CLI `--session-id` required, `--include-ancestors` flag, env `LANGFUSE_HOST` / `LANGFUSE_PUBLIC_KEY` / `LANGFUSE_SECRET_KEY` (never commit values). Fetch `chat.*`, sort, dedupe by `message_id` earliest, print JSON transcript.

- [ ] Manual smoke against local Langfuse after P0/P1 code is up; document command in script docstring only (no credentials).

---

### Task 8: Implementation log

**Files:**
- Create/Update: `docs/MODEL_TRACING_IMPLEMENTATION_LOG.md`

Append P0/P1 notes: session allowlist order, Public API base derivation, fail-open write-all on read failure, export dedupe rule. No credentials.

---

## Spec coverage check

| Spec item | Task |
|---|---|
| Session allowlist + headers | 1 |
| turn_id / message_id | 2 |
| chat.* delta + compact | 3 |
| Langfuse read-back | 4 |
| Writer + wire | 5 |
| Fork | 6 |
| Export + dedupe | 7 |
| Persistence log | 8 |
| Read-back failure write-all | 5 (+ 4 error path) |
| No cache-key session inference | 1 negatives |
