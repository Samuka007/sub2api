# Tasks

每项 checkbox 都是可独立验收的垂直切片。每个切片从可观察行为测试 RED 开始，完成最小实现后得到 GREEN；若实现暴露行为歧义，停止该切片并先修订 specs/design。

## 1. 建立首个端到端 OTLP Trace

- [x] 1.1 [Requirement: 所有模型推理与生成请求必须具有唯一 Trace；Scenario: 成功的同步模型请求；Requirement: Trace 必须发送到唯一的自部署 Langfuse 项目；Scenario: 有效的自部署目标可用；Scenario: 未配置任何目标；Requirement: Langfuse 中的 Trace 字段必须可筛选并保持标准映射；Scenario: 管理员查看模型观察] 以 Chat Completions 为 tracer bullet：先用本地 OTLP/HTTP fake server 写出“启用部署配置后一个请求产生一个可解码 Trace、禁用/缺失目标时不产生 Trace”的 RED 合约测试，再建立 `internal/modeltrace`、显式 OTel SDK/OTLP 依赖、启动/关闭生命周期和最小网关接线，使 fake server 能验证 Trace ID、根/Generation 层级、客户端与上游 input/output、模型和 request ID；验证：`cd backend && go test ./internal/modeltrace/... ./internal/handler/... -run 'TestModelTraceOTLPChatCompletions|TestModelTraceDisabledWithoutTarget' -count=1`，预期目标测试全部通过且 fake server 只收到目标项目的一个 Trace。

## 2. 交付部署与运行时配置闭环

- [ ] 2.1 [Requirement: 部署与运行时配置必须遵守确定优先级；Scenario: 只有部署配置；Scenario: 运行时配置覆盖部署配置；Scenario: 管理员运行时关闭；Scenario: 保存的运行时配置无法解析或解密；Requirement: 管理员必须能够无重启读取和更新运行时配置；Scenario: 管理员读取配置；Scenario: 管理员保存有效配置；Scenario: 非管理员尝试读取或更新；Scenario: 管理员请求 MVP 不支持的运行状态；Requirement: 配置更新必须保护秘密并保持原子语义；Scenario: 更新非秘密字段且保留秘密；Scenario: 替换项目秘密；Scenario: 无效更新] 先为部署默认、运行时整体覆盖、显式关闭、损坏回退、管理员权限、secret 保留/替换/清除、CAS 冲突和无 probe/runtime surface 写 RED 测试，再贯通后端配置管理、加密 settings、管理操作审计、`GET/PUT /api/v1/admin/model-tracing/config` 与管理员前端配置区；验证：`cd backend && go test ./internal/modeltrace/... ./internal/handler/admin/... ./internal/server/... -run 'TestModelTraceConfig|TestModelTraceAdminConfig' -count=1 && pnpm --dir frontend exec vitest run src/features/model-tracing/__tests__`，预期后端配置合约和前端保存流程通过，响应与 DOM 均不含 secret，且不存在连接测试/运行状态控件。

- [ ] 2.2 [Requirement: OTLP 传输必须保护内容与项目凭据；Scenario: 管理员配置非回环 HTTP 目标；Scenario: 管理员配置 HTTPS 或回环 HTTP 目标；Scenario: 部署配置使用不安全的远端 HTTP 目标] 先为部署配置与运行时 API 写传输矩阵 RED 测试，覆盖远端 HTTPS、`localhost`/IPv4/IPv6 回环 HTTP、远端域名/IP HTTP、正常证书失败和无跳过证书校验入口；再让两类配置复用同一 endpoint validator，并确保无效运行时更新保留旧 generation、无效部署配置退化为 disabled；验证：`cd backend && go test ./internal/modeltrace/... ./internal/handler/admin/... -run 'TestModelTraceEndpointTransport|TestModelTraceRejectsRemoteHTTP|TestModelTraceTLSVerification' -count=1`，预期仅 HTTPS 与回环 HTTP 组合通过，远端明文目标不产生任何 OTLP 请求。

## 3. 固化模型路由边界和身份门禁

- [ ] 3.1 [Requirement: 所有模型推理与生成请求必须具有唯一 Trace；Scenario: 已识别身份的请求在鉴权或参数校验阶段失败；Scenario: 匿名请求在身份建立前失败；Scenario: 非模型控制面请求；Requirement: 一个 Trace 必须保留完整尝试层级；Scenario: 请求未到达上游] 先建立真实路由与身份门禁矩阵 RED 测试，覆盖缺失/未知 API Key、无效鉴权限流、已知但禁用或受 IP/用户/分组限制的 API Key、请求体超限、协议校验失败、无可用账号，以及 `/models`、`/usage`、任务轮询、下载、取消等控制面；随后接入不分配 Span 的候选路由 middleware，并在 API Key 查询建立真实身份后通过通用 context hook 惰性启动 Trace，确保匿名失败为零 Trace、已识别身份后的失败有且仅有一个 Trace且没有虚构上游 Span；验证：`cd backend && go test ./internal/server/routes/... ./internal/server/middleware/... ./internal/handler/... -run 'TestModelTraceRouteMatrix|TestModelTraceAnonymousFailureSuppressed|TestModelTraceRecognizedAuthFailure|TestModelTraceNoUpstreamAttempt' -count=1`，预期匿名/控制面入口不产生 OTLP 工作，所有已识别用户/API Key 的模型执行入口有且仅有一个 Trace。

## 4. 保留协议转换、重试与故障转移现场

- [ ] 4.1 [Requirement: 一个 Trace 必须保留完整尝试层级；Scenario: 首次上游失败后故障转移成功；Scenario: 所有上游尝试均失败；Requirement: Trace 必须区分客户端与上游内容视角；Scenario: 请求和响应发生协议转换；Requirement: Trace 必须记录可得的用量、成本和阶段耗时；Scenario: 请求失败且没有 Usage] 先以 OpenAI Chat Completions→Anthropic 转换和账号故障转移写 RED 合约测试，断言同一根 Trace 下失败/成功 attempt 的顺序、账号、模型、endpoint、错误、耗时及四类 input/output 可区分；再在现有转换与 dispatch 边界接入 Attempt Recorder，覆盖最终成功与全部失败；验证：`cd backend && go test ./internal/handler/... ./internal/service/... -run 'TestModelTraceProtocolConversion|TestModelTraceFailoverAttempts|TestModelTraceAllAttemptsFailed' -count=1`，预期一个客户端请求始终只有一个根 Trace，且 Usage 未知时不写伪造零值。

## 5. 交付有界内容、多模态和秘密隔离

- [ ] 5.1 [Requirement: Trace 必须区分客户端与上游内容视角；Scenario: 内容未超过配置上限；Scenario: 内容超过配置上限；Requirement: 多模态内容必须遵守可配置的记录边界；Scenario: 默认处理包含 Base64 图片的请求；Scenario: 管理员开启原始媒体记录；Requirement: 认证凭据永远不得进入 Trace；Scenario: 请求和上游调用都携带秘密凭据；Scenario: Prompt 本身包含类似密钥的业务文本] 先用长文本、JSON、Base64 图片、媒体 opt-in 和分离 canary 凭据写 RED 测试，再实现有界增量 capture、原始/保存大小和 truncated 属性、默认媒体元数据与显式原文模式、字段 allowlist；在本机隔离自部署的 Langfuse `>= v3.22.0` 执行真实 OTLP smoke test 后固定 design 中的三个默认上限；验证：`cd backend && go test ./internal/modeltrace/... ./internal/handler/... -run 'TestBoundedTraceCapture|TestTraceMediaCapture|TestTraceCredentialCanaries' -count=1`，预期正文按配置保留/截断，所有系统凭据 canary 在解码后的 OTLP payload 中出现次数为 0，且本地 Langfuse 能展示截断元数据。

## 6. 交付流式、取消和部分 Response Trace

- [ ] 6.1 [Requirement: 流式与取消请求必须保留已产生的现场；Scenario: 流式请求正常完成；Scenario: 客户端收到部分内容后断开；Scenario: 上游流中途报错；Requirement: 观测故障必须 fail-open 且 MVP 不保证补送；Scenario: Langfuse 在流式请求期间变慢] 先为 SSE 正常结束、客户端断连、上游部分输出后错误以及阻塞 exporter 写逐帧 RED 测试，再在不改变真实 writer/read closer 语义的前提下增量捕获客户端与上游流、首 Token 时间、部分 Response 和终态；验证：`cd backend && go test ./internal/modeltrace/... ./internal/handler/... ./internal/service/... -run 'TestModelTraceSSEComplete|TestModelTraceClientDisconnect|TestModelTraceUpstreamStreamError|TestModelTraceSlowExporterDoesNotDelayStream' -count=1`，预期客户端帧序列与关闭追踪基线逐字节一致，Trace 保留正确部分内容和状态。

- [ ] 6.2 [Requirement: 所有模型推理与生成请求必须具有唯一 Trace；Scenario: 一个 WebSocket 连接包含多个模型回合；Requirement: 流式与取消请求必须保留已产生的现场；Scenario: 流式请求正常完成；Scenario: 客户端收到部分内容后断开；Requirement: 配置切换必须为请求提供一致快照；Scenario: WebSocket 连接的两个回合之间切换配置] 为 Responses WebSocket 握手、无效帧、首轮、后续轮次、轮次间配置切换和连接中断写 RED 测试；再在 Handler 回合边界使用 turn recorder factory，使每个语法有效 `response.create` 独立获取 generation、生成唯一 turn request ID 和 Trace，并写共同 connection request ID/turn index，确保握手/无效帧为零 Trace、连接 ID 不被推断为 Session；验证：`cd backend && go test ./internal/handler/... ./internal/service/... -run 'TestModelTraceResponsesWebSocketTurns|TestModelTraceResponsesWebSocketConfigSwitch|TestModelTraceResponsesWebSocketDisconnect' -count=1`，预期两轮模型执行产生两个可关联但独立结束的 Trace，配置切换只影响后续轮次，既有 WS frame/close code 逐字节保持不变。

## 7. 交付身份、Session、Usage 和成本映射

- [ ] 7.1 [Requirement: Trace 必须支持身份、会话和请求关联；Scenario: 两个请求携带相同显式会话标识；Scenario: 请求没有显式会话标识；Requirement: Trace 必须记录可得的用量、成本和阶段耗时；Scenario: 成功请求返回完整 Usage；Scenario: 请求失败且没有 Usage；Requirement: Langfuse 中的 Trace 字段必须可筛选并保持标准映射；Scenario: 管理员按身份筛选；Scenario: 管理员查看模型观察] 先写 RED 合约测试验证 user/API Key/group/account/request/model 字段、显式 session 归组、无显式 session 不推断、Usage Log 与 Langfuse token/cost 事实一致及未知值缺失，再在身份建立、Session 提取和最终 Usage 记录边界补全属性映射并复制必要 Trace 属性到相关 Span；验证：`cd backend && go test ./internal/modeltrace/... ./internal/handler/... ./internal/service/... -run 'TestModelTraceIdentityAndSession|TestModelTraceUsageAndCost|TestModelTraceUnknownUsage' -count=1`，预期相同显式 Session 的两个 Trace 可归组，内容 fallback 不产生 Session，OTLP Usage 与 Usage Log 逐字段一致。

- [ ] 7.2 [Requirement: Trace 必须支持身份、会话和请求关联；Scenario: 两个请求携带相同显式会话标识；Scenario: 请求没有显式会话标识；Scenario: 请求只携带缓存或调度键] 为 `session_id`、`conversation_id`、Grok `x-grok-conv-id`、结构化 `metadata.user_id.session_id`、`prompt_cache_key`、粘性 hash 和内容 fallback 写表驱动 RED 测试，再实现独立 Langfuse Session allowlist extractor，禁止复用包含缓存语义的 `ExtractSessionID`；验证：`cd backend && go test ./internal/modeltrace/... ./internal/service/... -run 'TestLangfuseSessionExtractor' -count=1`，预期只有明确会话字段生成 `langfuse.session.id`，缓存/调度/内容派生键全部保持缺失。

## 8. 扩展全部同步协议与入口

- [ ] 8.1 [Requirement: 所有模型推理与生成请求必须具有唯一 Trace；Scenario: 成功的同步模型请求；Requirement: Trace 必须区分客户端与上游内容视角；Scenario: 请求和响应发生协议转换] 在 tracer bullet 稳定后，用表驱动 RED 集成测试扩展到 Responses、Anthropic Messages、Gemini generateContent/streamGenerateContent、Embeddings、实际调用上游的 Search/Count Tokens、同步图片与视频生成/编辑；每个 case 验证一个根 Trace、协议/模型、四类内容和最终结果，再逐入口接入 Recorder；验证：`cd backend && go test ./internal/handler/... ./internal/service/... ./internal/server/routes/... -run 'TestModelTraceProtocolMatrix' -count=1`，预期矩阵中每个实际模型执行入口通过，纯本地或控制面 case 保持零 Trace。

## 9. 关联异步图片、批量图片和长任务

- [ ] 9.1 [Requirement: 所有模型推理与生成请求必须具有唯一 Trace；Scenario: 异步或批量模型执行；Scenario: 异步执行时原配置 generation 已不可用；Requirement: 配置切换必须为请求提供一致快照；Scenario: 异步任务跨越目标配置切换；Requirement: 一个 Trace 必须保留完整尝试层级；Scenario: 首次上游失败后故障转移成功；Requirement: 多模态内容必须遵守可配置的记录边界；Scenario: 默认处理包含 Base64 图片的请求] 先写异步图片接受后执行、同/异 fingerprint、目标切换、追踪关闭、进程内 detached context、批量多 item、item 重试和任务取消 RED 测试；再持久化最小 SpanContext、task/item ID 和单向 generation fingerprint，后台仅在 fingerprint 匹配时续接原 Trace，错配且当前追踪启用时创建带 OTel Link/`submission_trace_id` 的独立 Trace，关闭时 no-op，禁止持久化 exporter 快照或秘密；验证：`cd backend && go test ./internal/handler/... ./internal/service/... ./internal/modeltrace/... -run 'TestAsyncImageTracePropagation|TestAsyncTraceGenerationMismatch|TestBatchImageTraceItems|TestAsyncModelTraceCancellation' -count=1`，预期同 fingerprint 续接原 Trace，错配不复用原 Trace ID且可由 task/submission ID 关联，业务结果始终不受追踪配置影响。

## 10. 保证热切换和导出故障不影响业务

- [ ] 10.1 [Requirement: 配置切换必须为请求提供一致快照；Scenario: 请求进行中切换目标；Scenario: 请求进行中关闭追踪；Requirement: 观测故障必须 fail-open 且 MVP 不保证补送；Scenario: Langfuse 在非流式请求期间不可用；Scenario: 导出内部发生异常] 先用两个 fake OTLP 目标、进行中请求、并发更新、队列饱和、网络拒绝、序列化错误和 panic exporter 写 RED 测试，再实现不可变 generation、请求引用、原子 swap、有界异步 flush 和 no-op/panic 隔离；验证：`cd backend && go test -race ./internal/modeltrace/... ./internal/handler/... -run 'TestModelTraceGenerationSwap|TestModelTraceDisableInFlight|TestModelTraceExporterFailOpen' -count=1`，预期进行中 Trace 不跨目标，新请求使用新配置，所有故障场景的客户端响应、上游次数和 Usage Log 与追踪关闭基线一致。

## 11. 完成全量门禁与 MVP 验收

- [ ] 11.1 [Requirement: 所有模型推理与生成请求必须具有唯一 Trace；Scenario: 成功的同步模型请求；Scenario: 已识别身份的请求在鉴权或参数校验阶段失败；Scenario: 匿名请求在身份建立前失败；Scenario: 非模型控制面请求；Requirement: 观测故障必须 fail-open 且 MVP 不保证补送；Scenario: Langfuse 在非流式请求期间不可用；Scenario: Langfuse 在流式请求期间变慢；Scenario: 导出内部发生异常] 运行完整后端、前端、构建、race、OTLP 合约、协议矩阵和 canary 泄露门禁；记录追踪关闭/开启的非流式、SSE、长上下文 CPU/内存/延迟对比，并在仅绑定回环地址的本地自部署 Langfuse `>= v3.22.0` 执行成功、失败、重试、流式和截断 smoke，确认没有同步等待、响应差异或无界增长；验证：`make test-backend && pnpm --dir frontend run lint:check && pnpm --dir frontend run typecheck && pnpm --dir frontend run test:run && make build`，并额外运行 `cd backend && go test -race ./internal/modeltrace/... ./internal/handler/... ./internal/service/... -count=1`；预期所有命令退出 0且本地 Langfuse 展示对应 Trace。若无用户批准的性能数值阈值，只能报告实测差异，不能宣称达到未定义 SLO。
