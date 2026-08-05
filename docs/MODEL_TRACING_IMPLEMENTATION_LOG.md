# Model Tracing Implementation Log

## 2026-07-23 — Langfuse long-horizon conversation (P0/P1)

### Durable references

- Runtime behavior: `backend/internal/modeltrace/`
- E2E behavior mapping: `.agent/skills/sub2api-model-trace-e2e/references/otel-spec-mapping.md`
- The original local Superpowers plan/spec were working artifacts and are intentionally not tracked.

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

### Verification

- Unit tests live under `backend/internal/modeltrace/tests/` (`package modeltrace_test`); production package has no `*_test.go`.
- Internal test hooks: `backend/internal/modeltrace/testing_export.go` (`Testing*` only).
- Run: `cd backend && go test ./internal/modeltrace/... -count=1`
- Local stack image: `sub2api:model-trace-b4316e25-forkfix` via `/opt/sub2api-stack/compose.yml`.

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

### Credentials

- No real or local test credentials recorded here. Use deployment/runtime config keys `public_key` / `secret_key` only.

## 2026-07-24 - Runtime Langfuse OTLP network repair

- Symptom: model requests completed successfully, but the configured Langfuse project contained no traces or observations.
- Root cause: sub2api resolved OTLP through the external Docker network `sub2api-langfuse`, while Langfuse Web was attached only to its Compose default network. The configured `langfuse-otel` hostname therefore did not resolve from the sub2api container.
- Deployment fix: attach `langfuse-web` to `sub2api-langfuse` with the network alias `langfuse-otel`; keep port 3000 bound to loopback only.
- Runtime endpoint remains sourced from `MODEL_TRACING_ENDPOINT`; credentials remain sourced from the model-tracing public/secret key configuration and are not recorded here.
- The initially deployed upstream image was revision `cd8bb98` (`0.1.164`) and did not contain the workspace model-tracing implementation. The runtime was replaced with a local release image built from revision `61d6389`, tagged `sub2api:modeltrace-61d6389`, with the production frontend embedded.
- Because deployment endpoint validation permits plaintext HTTP only for loopback hosts, cross-container OTLP uses `https://sub2test-hendo.scitrace.cc/api/public/otel`. Caddy routes only `/api/public/otel*` to the `langfuse-otel` network alias; Langfuse port 3000 remains loopback-bound on the host.
- Startup evidence after replacement: `model trace configuration applied` reported `enabled=true`, source `deployment`, config version `0`.
- Production black-box verification: a non-streaming Responses request for `gpt-5.6-sol` returned HTTP 200 with session `langfuse-verified-1784909184`; Langfuse returned trace `05cdf6a7860f257204c82ae2f697cd17` named `model.request` and four observations: root `model.request`, `upstream.attempt.1` GENERATION, `chat.user`, and `chat.assistant`.
- The repository skill's isolated Colima smoke was not run on this Debian host because the required `colima` executable/profile is unavailable. The deployed production stack was verified directly instead.
- Build note: the 4 GiB host could not run the frontend build while Langfuse was active. Stopping Langfuse temporarily and running the cached frontend builder with a 2560 MiB Node heap completed typecheck and Vite build; Langfuse was then restored before deployment verification.

## 2026-07-26 — Task 7.3 移除请求期 Langfuse Public API 读取

### 行为变更

- 删除 `backend/internal/modeltrace/langfuse_reader.go` 及仅覆盖该 reader 的测试和测试导出钩子。
- HTTP 模型请求与 Responses WebSocket 回合只解析当前捕获的 input/output，并把所有可解析 `chat.*` 事件追加到 OTLP；重复 `message_id` 不在运行时过滤。
- 模型请求路径不再从 OTLP endpoint 派生 Langfuse Public API 地址，也不请求 `/api/public/traces` 或 `/api/public/observations`。运行时不再依赖 Langfuse 历史、Public API 可用性或读取凭据。
- 仅客户端明确声明 `request_kind=compaction` 时追加 `chat.compact`；显式 fork 继续追加 `chat.fork`。远端历史不再用于推断压缩或抑制重复 fork marker。
- `scripts/langfuse_session_graph.py` 是唯一会话图与离线导出标准；其 `graph.json`、`assignments.jsonl` 和 `message-objects.jsonl` 共同构成会话导出产物。
- Responses WebSocket `response.completed`/`response.done` JSON frame 现在归一化其 `response.output`，确保 WS 回合也能生成当前 assistant/tool 会话事件。

### RED / GREEN 证据

- RED：`cd backend && go test ./internal/modeltrace/... -run 'TestExtractConversationDelta_(appendsRepeatedHistory|doesNotInferCompactionFromRemoteHistory)|TestModelTraceConversationHTTPAppendsWithoutLangfusePublicAPIRead|TestModelTraceResponsesWSTurnAppendsWithoutLangfusePublicAPIRead' -count=1`；旧实现观察到 HTTP/WS 各 2 次 Public API GET，重复历史被过滤，并从远端历史推断 compaction。
- GREEN：`cd backend && go test ./internal/modeltrace/... -run 'TestModelTraceConversation|TestModelTraceResponsesWSTurn' -count=1`，退出 0；fake OTLP server 对任何 Public API GET 返回 405，并断言计数为 0、HTTP/WS 的重复 `u1`/`a1` message ID 均各追加两次、显式 compact/fork span 和 fork metadata 保留。
- 受影响模块：`cd backend && go test ./internal/modeltrace/... -count=1`，退出 0。
- 会话图离线验证：`python3 scripts/test_langfuse_session_graph.py`。

### 剩余验证边界

- 真实本地 Langfuse `3.222.0` e2e smoke 在最终工作区执行两次；两次均完成配置闭环、身份边界、503/401 Trace、内容截断、Session、failover、all-attempts-fail、SSE、fail-open、Responses WebSocket 请求和 200-item batch 验证，但最终 ClickHouse 断言稳定失败：`Responses WebSocket disconnect terminal mismatch: status=0 errors=0 partial_output=1`。失败 Trace 的 `model.request` 实际终态为 `completed`、level 为 `DEFAULT`，未得到既有 e2e 期望的 `client_disconnected`；该失败位于 Task 7.3 会话事件断言之外，因此本次只能声明移除实时读取的目标回归已通过，不能宣称真实 Langfuse 全链路门禁通过。
- 本记录不包含任何 Langfuse、模型供应商或本地测试凭据；凭据仍只从部署/运行时配置的安全取值位置加载。

## 2026-07-26 — 统一部署与运行时 endpoint validator

- 新增唯一公开校验入口 `config.ValidateModelTracingEndpoint`；部署配置规范化、运行时配置读取/更新和 exporter generation 构建均复用该函数。
- 统一规则允许 HTTPS 与 `localhost`/字面量回环 IP 的 HTTP，拒绝远端明文 HTTP、userinfo、query、fragment、空 host 和非 HTTP(S) scheme；部署配置无效时退化为 disabled，运行时无效更新保持旧 generation。
- RED：`cd backend && go test ./internal/config -run TestLoadDisablesModelTracingForUnsafeEndpointComponents -count=1`，三个 userinfo/query/fragment case 均因 tracing 未关闭而失败。
- GREEN：同一命令退出 0；`cd backend && go test ./internal/modeltrace/... -run TestModelTraceEndpointTransport -count=1` 退出 0。
- 本次不增加网络 probe，不读取 Langfuse Public API，不记录任何真实或本地测试凭据。

## 2026-07-26 — 发布质量门禁格式修复

- `backend/internal/modeltrace/tests/config_manager_test.go` 仅执行 `gofmt`，修复 Code Quality 的格式门禁；模型追踪生产行为、测试断言和配置契约均未改变。
- 验证：`gofmt -l backend/internal/modeltrace/tests/config_manager_test.go` 无输出；完整 `make test-unit` 与 golangci-lint `v2.9.0` 退出 0。
- 本次不新增或记录任何模型追踪凭据、endpoint 或运行时配置。

## 2026-07-28 — Issue #38 Session / Thread 关联修复

### 问题与边界

- 当前 `ExtractLangfuseSessionID` 会把 `client_metadata.thread_id`、`Thread-Id` / `thread_id` 回退为 Langfuse session，错误合并本应独立的 session 与 thread。
- Anthropic Messages 的 `metadata.user_id` 字符串尚未按严格 JSON 与已确认 legacy 格式提取 session，`X-Claude-Code-Session-Id` 也未纳入 modeltrace fallback。
- 提取规则必须由入口协议和显式客户端信号决定，不得依赖模型名称；`prompt_cache_key`、`previous_response_id`、路由 hash、身份 ID、内容 hash 和上游生成 ID 均继续排除。

### 实施计划

- 引入带 `SessionID` / `SessionSource` / `ThreadID` / `ThreadSource` 与 session conflict 标记的结构化关联结果。
- HTTP middleware 首次记录 header 关联，读取请求体后按协议优先级合并；Responses WebSocket 回合复用相同提取器。
- 根 span 记录低基数 session/thread source、独立 thread ID 与 conflict 布尔属性；原始冲突 ID 不进入属性。
- 增加协议、优先级、JSON/legacy、thread 独立性、模型无关性以及 HTTP/WS 接线测试，并运行 modeltrace 单测与 `.agent/skills/sub2api-model-trace-e2e/` smoke。

### 安全取值位置

- 入口协议来自请求 URL path；客户端关联信号只来自请求 body 与允许的 header。
- 本记录不包含任何 Langfuse、模型供应商或本地测试凭据；运行时配置继续从既有 model-tracing 配置安全加载。

### 实现与验证结果

- `backend/internal/modeltrace/session.go` 新增结构化 `Correlation`：Anthropic Messages 按 body session/conversation、metadata session、`metadata.user_id` JSON、已确认 legacy、`X-Claude-Code-Session-Id`、标准 session header 优先级解析；Responses 按 body、`client_metadata.session_id`、标准 session header、Claude header、对应 Grok header 解析；其他入口只接受显式 body/header 信号。
- `client_metadata.thread_id`、`Thread-Id` / `thread_id`、`X-Codex-Turn-Metadata.thread_id` 独立保存，不再回退为 Langfuse session。根 span 写入低基数 source 与 conflict 属性，冲突时 body session 胜出且不导出落选 ID。
- HTTP middleware、Responses WebSocket handler/turn、upstream attempt 均接入结构化结果；协议判断来自 URL path，与模型名称无关。
- 定向单测：`go test ./internal/modeltrace/... -run 'TestCorrelationExtractor|TestModelTraceHTTPCorrelation|TestModelTraceResponsesWebSocketCorrelation' -count=1` 通过。
- 完整单测：`go test ./internal/modeltrace/... -count=1` 通过；race：`go test -race ./internal/modeltrace/... -count=1` 通过。
- handler 接线测试：`go test ./internal/handler -run 'TestOpenAIWSTraceTurnsUsesPayloadCorrelationBeforeHeaders|TestOpenAIResponsesWebSocketTrace|TestOpenAIResponsesWebSocketUsageUsesTurnTraceContext' -count=1` 通过。

### Smoke 边界

- `.agent/skills/sub2api-model-trace-e2e/SKILL.md` 要求的 `run_e2e.sh` 依赖 Colima profile `swebench`。本机已安装 Colima，但 guest Docker provisioning 因宿主环境缺少可用的 containerd 服务未完成；因此没有把 Colima VM 结果当作通过依据。
- 为完成行为验证，使用同一脚本通过宿主 rootful Docker socket `/var/run/docker.sock` 运行临时 fallback：镜像来自国内镜像，Go 客户端使用 host network，编译目标按宿主 `linux/amd64` 适配；Langfuse MinIO 数据绑定到 `/data`，绕过根分区空间保留阈值。该路径仅为本机验证适配，不改变生产代码或脚本。
- `run_e2e.sh` 完整 smoke 通过：Langfuse `3.224.2`，stdout 为 `trace_id=174c92bd5cb108117971adb3a9bead20`、观测数 `1`、`VERIFY_OK`；日志含 `full-scale e2e passed`，HTTP/WS、failover、断连、配置快照、OTLP fail-open、200 项 batch 续接及敏感内容门禁均通过。
- 单测与 race 使用 `golang:1.26.5` 容器完成；本记录不包含任何 Langfuse、模型供应商或本地测试凭据。

## 2026-08-03 — 提升模型追踪捕获上限（prompt 16M / response 8M / media 16M）

### 背景与根因

- Langfuse 观测：最近 24h input 高度集中于 8,388,665/8,388,666 bytes（合计 8,429 条）、output 集中于 1,048,629/1,048,633 bytes（合计 1,762 条），且全部为失效 JSON。截断值 = 捕获 limit + `[truncated:original_bytes=%d,captured_bytes=%d]` 标记（标记追加在 limit 之外，长度随 original_bytes 位数变化），确认是 Sub2API 捕获层硬截断，非 Langfuse/Collector 侧。
- input 8 MiB = 面板运行时配置 `prompt_max_bytes=8MiB` 命中 `maxCaptureBytes = 8<<20` 硬顶；output 1 MiB = 部署 env `response_max_bytes=1MiB`。
- 捕获顺序是先按 limit 截原始请求字节、再 sanitize：base64 媒体字节计入捕获预算，超限请求被截断后 JSON 解析失败，媒体摘要（media descriptor）机制无法生效，残留 input 是含 base64 的半截 JSON。请求数据一旦截断不可重来，上限过低导致不可恢复的信息丢失。

### 行为变更

- `backend/internal/modeltrace/exporter.go`：默认捕获上限调整为 prompt 16 MiB、response 8 MiB、media 16 MiB；`maxCaptureBytes` 硬顶 8 MiB → 32 MiB，解除 8 MiB 钳制。
- `backend/internal/config/config.go`：`setModelTracingDefaults` 与 `normalizeModelTracingConfig` 缺省值对齐 16M/8M/16M。
- `backend/internal/modeltrace/recording/recording.go`：`InputLimit` 非 recorder fallback 1 MiB → 16 MiB 对齐。
- 运行时注意：面板运行时配置若仍保留 `prompt_max_bytes=8MiB`，会继续覆盖部署配置，升级后需把面板与 `model-tracing.env` 同步为 16M/8M/16M。

### 验证

- `gofmt -l` 无输出；`go test ./internal/modeltrace/... ./internal/config -count=1` 退出 0。
- `tests/security_test.go` 通过 `TestingMaxCaptureBytes` 常量自适应新上限，断言无需改动。

## 2026-08-03 — 移除 maxCaptureBytes 硬顶，捕获上限改为纯可选配置

### 背景

- #89 把硬顶从 8 MiB 提升到 32 MiB，但硬顶本身是多余约束：`prompt/response/media_max_bytes` 本来就是可选配置（≤0 时回落默认值），代码再设一个不可配置的硬顶会静默钳制用户明确配置的值；32 MiB 这个数字没有理由替用户决定边界。

### 行为变更

- `backend/internal/modeltrace/exporter.go`：删除 `maxCaptureBytes` 常量与 `boundedSizes` 中的三处钳制；显式配置值原样生效、无上限；≤0 仍回落默认（16M/8M/16M）。
- `backend/internal/modeltrace/testing_export.go`：移除 `TestingMaxCaptureBytes` 导出。
- `backend/internal/modeltrace/tests/security_test.go`：`TestModelTraceLargePayloadMemoryBound` 重写为 `TestModelTraceCaptureLimitsHonorConfigWithoutHardCap`——显式值不被钳制（含 MaxInt）、缺省回落默认、截断标记行为不变。

### 验证

- `gofmt -l` 无输出；`go test ./internal/modeltrace/... ./internal/config -count=1` 退出 0。

### 运行时注意

- 上限不再有代码硬顶：面板/部署配置设多大就生效多大。内存与单批 OTLP 导出载荷随配置线性放大，超大请求的导出失败仍为 fail-open、不影响业务。

## 2026-08-03 — 移除前端 model-tracing 面板的 8M 捕获上限钳制

### 背景

- 后端 #89/#90 已移除 `maxCaptureBytes` 硬顶并提升默认值（prompt 16M / response 8M / media 16M），但前端面板 `frontend/src/features/model-tracing/ModelTracingSettings.vue` 仍硬编码 `MAX_CAPTURE_BYTES = 8 * 1024 * 1024`：输入框 `:max` 限制 + 提交时 `positiveInteger` 用 `Math.min(MAX_CAPTURE_BYTES, …)` 钳制。
- 现象：面板把 prompt 改为 16M 保存后回弹为 8M——前端提交前把值钳到 8M，后端如实存储并回显，非后端/存储问题。

### 行为变更

- 删除 `MAX_CAPTURE_BYTES` 常量与输入框 `:max` 属性；
- `positiveInteger` 不再做上限钳制，正值原样提交（`Math.floor`），与后端"显式配置无硬顶"契约一致。

### 验证

- `pnpm test:run src/features/model-tracing` 通过（vitest）。

### 部署注意

- 面板 UI 随前端构建发布（新 release）；生效后管理员可在面板保存 >8M 的捕获上限。
