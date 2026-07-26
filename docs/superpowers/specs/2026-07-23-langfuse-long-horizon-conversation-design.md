# Langfuse Long-Horizon Conversation Design

Date: 2026-07-23  
Status: ready for review  
Scope: sub2api model-trace → Langfuse (no separate conversation DB)

## Goals

Capture **long-horizon conversation content** for Codex (and compatible Responses clients) such that:

1. A session is browsable in the Langfuse Session UI.
2. The same session is fully exportable via API/scripts.
3. Context compression/truncation does **not** erase archived originals.
4. Client fork/branching (resume-from / fork) is represented without overwriting the parent line.
5. Parallel tool calls within a turn are linear events, not branches.

Non-goals:

- Using the latest request `input` snapshot as the source of truth.
- Treating billing/SSE telemetry as the conversation export source.
- Inferring session grouping from `prompt_cache_key`, sticky routing hashes, or content similarity.

## Decisions (product + design alignment)

| Topic | Choice |
|---|---|
| Storage | Langfuse only |
| Consumers | Session UI + API export |
| Branching | Codex/client fork from a historical message |
| Parallel tools | No special branch model |
| Compression | Permanently keep original messages |
| Dedupe source of truth | Langfuse Public API read-back of existing `chat.*` `message_id`s |
| Read-back failure | Still write all parsed items for the turn (may duplicate); do not block the model request |
| Export duplicates | Collapse by `message_id`, keep the earliest observation |
| Delivery | Two phases: P0 session grouping, then P1 conversation track |

## Problem statement (current behavior)

Observed with local Codex → sub2api `:8080` → Langfuse `:3000`:

- Multi-turn Codex requests share client-owned session/thread identifiers and distinct turn identifiers.
- Langfuse `trace.sessionId` is unset, so Sessions UI/API do not group them.
- Root cause: `ExtractLangfuseSessionID` (today in `backend/internal/modeltrace/session.go`) does not read Codex `client_metadata.session_id` / `client_metadata.thread_id`, nor common Codex headers `session_id` / `thread_id`.
- Later-turn request `input` often contains prior user/assistant text, but not prior SSE streams or per-turn generation usage.
- After compact/truncate, later `input` snapshots can omit earlier messages; snapshot-based reconstruction is unsafe.

## Recommended approach

**Dual-track writes under one Langfuse session:**

1. **Request observation track** (existing, may be slimmed): `model.request` / `upstream.attempt` for latency, usage, cost; SSE optional/truncated.
2. **Conversation event track** (P1, append-only): `chat.*` events for user / assistant / system (includes Codex `developer`) / tool_call / tool_result (/ fork / compact markers).

Long-horizon truth = union of all `chat.*` events for a `session_id`, never the newest request body alone.

## Identity model

| Field | Meaning | Source |
|---|---|---|
| `session_id` | One conversation line. Written to Langfuse `trace.sessionId` / `langfuse.session.id`. | Explicit client identifiers only (see extraction order below). Not inventable from cache/scheduling keys. |
| `turn_id` | One user-initiated turn. | Prefer `client_metadata.turn_id`, or `turn_id` inside `X-Codex-Turn-Metadata` / `client_metadata.x-codex-turn-metadata` JSON; otherwise a per-request generated ID. |
| `message_id` | Stable ID for a single user/assistant/tool item. | Prefer protocol IDs (Responses item `id`, tool `call_id`); otherwise `sha256` hex of the canonical JSON item payload (type + role/name + normalized content/`call_id`) computed by sub2api. **Not** assumed to be a Codex-required field. |
| `parent_message_id` | Linear reply/parent linkage within a line. | Derived when building the conversation track. |
| Fork metadata | New `session_id` linked to a parent point. | Prefer explicit `fork_from_session_id` + `fork_from_message_id` (optional `fork_from_turn_id`). Codex also sends `forked_from_thread_id` inside `X-Codex-Turn-Metadata` / `client_metadata.x-codex-turn-metadata`; when only a thread-level fork is present, `fork_from_message_id` is stored as `thread`. |

### Session extraction rules

Extend session extraction to accept, in order:

1. Explicit body `session_id`
2. Explicit body `conversation_id`
3. `metadata.session_id` / `metadata.user_id.session_id` (existing)
4. `client_metadata.session_id` (Codex body)
5. `client_metadata.thread_id` (fallback if session_id absent)
6. Header `session_id` / `session-id` (Codex common wire form)
7. Header `thread_id` / `thread-id` (fallback if session header absent)
8. Grok header `x-grok-conv-id` on grok routes (existing)

Still excluded: `prompt_cache_key`, sticky hashes, generated scheduling session values, content-similarity inference.

Notes:

- Codex is an important producer of these identifiers, but the allowlist is client-agnostic.
- Sticky-routing session isolation for upstream OAuth accounts remains a separate concern from Langfuse session grouping; Langfuse receives the **client-owned** explicit identifier, not a cache/routing surrogate.

## Phased delivery

### P0 — Session grouping

Ship only the extended `SessionExtractor` so multi-turn Codex (and compatible clients) appear as one Langfuse Session in UI and Sessions API.

### P1 — Conversation track + export

Ship append-only `chat.*` events, Langfuse Public API read-back for dedupe, fork markers, and the export helper.

## Per-turn write model

### Observation track (existing; both phases)

Keep current request/generation spans for ops and billing. SSE full streams are not required for conversation export and may be truncated or disabled by config.

### Conversation track (P1)

After a turn completes, append conversation items according to the anti-compression algorithm:

| Event name | Payload (minimum) |
|---|---|
| `chat.user` | text/content, `message_id`, `turn_id`, `session_id`, `seq`, timestamp |
| `chat.system` | system / Codex `developer` instructions, same identity fields |
| `chat.assistant` | final visible text (not token deltas), same identity fields |
| `chat.tool_call` | `call_id`, name, arguments |
| `chat.tool_result` | `call_id`, result body |
| `chat.compact` | marker that upstream context was compacted/truncated this turn; **does not delete** prior events |
| `chat.fork` | on first turn of a forked session: `fork_from_session_id`, `fork_from_message_id` |

Implementation shape in Langfuse:

- Same `sessionId` on the turn trace.
- `chat.*` as child observations (or equivalent structured events) on that trace.
- Metadata must include `session_id`, `turn_id`, `message_id`, `parent_message_id`, `seq`.

## Anti-compression algorithm (P1)

1. Load already-persisted `message_id`s for this `session_id` from Langfuse via the **Langfuse Public API** (`chat.*` observations), authenticated with the same deployment credentials already used for OTLP export. An in-process cache is optional; **Langfuse is the source of truth** across restarts and instances.
2. Parse this turn’s request `input` items + final assistant/tool outputs into canonical conversation items.
3. Resolve `turn_id` per the identity model above.
4. When read-back succeeds: append events only for unseen `message_id`s.
5. When read-back **fails**: do **not** block the model request; still write **all** parsed conversation items for this turn (duplicates possible). Emit a warn-level log without credentials or secrets.
6. If read-back succeeded and this turn’s input is missing previously persisted **user/assistant/tool** messages → emit `chat.compact`. If read-back failed, skip missing-content compact detection for that turn. Additionally, when Codex turn metadata has `request_kind=compaction`, emit `chat.compact` even if content-missing detection does not fire. **Never** delete or rewrite older `chat.*` events from the new snapshot.
7. Codex `developer` / OpenAI `system` messages are stored as `chat.system` (empty-content items skipped). Deduped by `message_id` like other content events.
8. Export reconstructs history by reading all historical `chat.*` for the session and **deduplicating by `message_id`, keeping the earliest observation**. Native Langfuse Session UI may still list duplicate observations after a read-back-failure write; the export helper is the guaranteed deduped view.

## Fork / branching rules (P1)

1. Parent line `S_old` continues unchanged.
2. Fork creates `S_new` with metadata `fork_from_session_id=S_old` and `fork_from_message_id=M`.
3. Write `chat.fork` once on the new line, then append only new-line deltas.
4. Do **not** copy the full parent transcript into every child trace (avoid bloat).
5. If explicit fork fields are absent, optional heuristic: new session’s first-turn prefix matches an existing session event sequence then diverges → annotate fork; if uncertain, keep independent sessions (no forced merge).
6. Parallel tool calls remain multiple `chat.tool_*` events on the same turn/session, not new sessions.

## Export model (UI + API) (P1)

### UI

Langfuse Session page lists all traces for `session_id`. Operators can browse `chat.*` observations there; for a guaranteed linear transcript with `message_id` dedupe, use the export helper below.

### API / script

Inputs:

- `session_id` (required)
- `include_ancestors` (default `false`)

Behavior:

1. List traces for session.
2. Collect `chat.*` observations.
3. Sort into a linear transcript.
4. Dedupe by `message_id`, keeping the earliest observation.
5. If `include_ancestors=true`, walk `fork_from_*` and prepend ancestor transcripts, splitting at the fork message.

Default export is the current session’s own events; ancestor inclusion is opt-in.

## Components and change boundary

All changes stay inside sub2api model-trace → Langfuse export path.

| Component | Phase | Responsibility |
|---|---|---|
| `SessionExtractor` (extend) | P0 | Map body/`client_metadata`/Codex headers/`x-grok-conv-id` to Langfuse `sessionId` |
| `ConversationDeltaExtractor` (new) | P1 | Parse turn input/output; compute `message_id`; detect compact/truncate |
| `LangfuseObservationReader` (new) | P1 | Public API read-back of persisted `chat.*` `message_id`s |
| `ConversationEventWriter` (new) | P1 | Emit `chat.*` observations with identity metadata |
| `ForkAnnotator` (new) | P1 | Persist fork metadata + `chat.fork` |
| Export helper (docs/script) | P1 | Session transcript with `message_id` dedupe (+ optional ancestor tree) |
| Existing request traces | keep | Remain for usage/cost; SSE optional |

Data flow per turn (P1):

```text
Client request
  → SessionExtractor sets Langfuse sessionId
  → existing model.request / generation (observation track)
  → LangfuseObservationReader loads existing message_ids
  → ConversationDeltaExtractor computes chat items
       (on read failure: treat all parsed items as writable)
  → ConversationEventWriter appends chat.*
  → ForkAnnotator writes chat.fork + fork_from_* when applicable
```

## Acceptance criteria

### P0

1. Multi-turn Codex chat with a shared client-owned session identifier (`client_metadata.session_id` / `thread_id` and/or header `session_id` / `thread_id`) appears as one Langfuse Session in UI and Sessions API.
2. Requests lacking explicit supported session identifiers remain ungrouped (no inference from cache/scheduling keys).

### P1

3. After a turn that truncates/compacts upstream context, earlier `chat.*` events remain and full-session export still contains original user/assistant/tool content.
4. Fork creates a new session linked by `fork_from_*`; parent session remains intact; ancestor-aware export can rebuild the joined view.
5. Export API/script returns a linear transcript of user/agent/tool_call/tool_result for a session without requiring SSE payloads or generation cost fields, collapsing duplicate `message_id`s to the earliest observation.
6. When Langfuse read-back fails for a turn, the model request still succeeds; that turn may write duplicate `chat.*` items, and export still produces a correct deduped transcript.

## Test plan (design-level)

### P0

1. **Session grouping (body)**: two-turn Codex with shared `client_metadata.session_id` → both traces share Langfuse `sessionId`.
2. **Session grouping (header)**: two-turn request with shared header `session_id` / `thread_id` → same Langfuse Session.
3. **Negative session inference**: only `prompt_cache_key` present → `sessionId` stays empty.

### P1

4. **Delta append**: turn 2 adds only new messages when read-back succeeds; turn 1 `message_id`s are not duplicated.
5. **Read-back failure**: simulate Public API failure → turn still writes parsed items; export dedupes by `message_id`.
6. **Compact survival**: simulate turn N input missing early messages → `chat.compact` emitted; export still includes early `chat.user`/`chat.assistant`.
7. **Tool linearity**: one turn with tool_call + tool_result → ordered `chat.tool_*` under same session/turn.
8. **Fork**: new session with `fork_from_*` → parent unchanged; `include_ancestors=true` export joins at fork point.

## Out of scope for first implementation

- Separate conversation database outside Langfuse.
- Guaranteed retention policies / legal hold UI.
- Perfect fork detection when clients omit fork metadata and heuristics are ambiguous.
- Replacing existing billing/trace pipelines.
- Blocking model requests on conversation-track or Langfuse read-back failures.
