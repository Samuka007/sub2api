# OTEL Spec 映射与 503 设计依据

## OpenSpec 来源

完整规格在仓库 `openspec/changes/add-model-request-otel-tracing/`：
- `proposal.md` — 变更动机与范围
- `design.md` — 14 个设计决策，含 `internal/modeltrace` 模块结构、根 Trace + Generation 子 span、fail-open、endpoint 校验、Langfuse 属性映射
- `specs/model-request-tracing/spec.md` — 模型请求追踪能力需求
- `specs/langfuse-otel-export/spec.md` — Langfuse OTLP 导出能力需求
- `tasks.md` — 11 个垂直切片任务，当前实现覆盖 1.1 / 2.1 / 2.2 / 7.2

## 本 e2e 验证的 spec 映射

| Spec Requirement | Scenario | e2e 验证点 |
|---|---|---|
| 所有模型推理与生成请求必须具有唯一 Trace | 成功的同步模型请求 | ClickHouse `traces` 表对 1 个 session_id 返回 1 条 |
| 一个 Trace 必须保留完整尝试层级 | 请求未到达上游 | `observations` 表 2 行：根 SPAN + 子 GENERATION，parent_observation_id 链接正确 |
| Trace 必须区分客户端与上游内容视角 | 请求和响应发生协议转换 | 根 span `input`/`output` = 客户端请求/响应 JSON |
| 认证凭据永远不得进入 Trace | 请求和上游调用都携带秘密凭据 | 确认 trace 的 input/output 不含 `sk-lf-local`/`admin123456`（只含客户端 body 的 `sk-e2e-...` 用户 key 是用户主动写入） |
| 流式与取消请求必须保留已产生的现场 | 流式请求正常完成 | 本 e2e **未覆盖**（非流式请求），后续 task 6.1 |
| Trace 必须支持身份、会话和请求关联 | 两个请求携带相同显式会话标识 | `traces.session_id` = 请求 body `session_id`；`metadata.api_key_id`/`metadata.group_id` 非空 |
| Trace 必须记录可得的用量、成本和阶段耗时 | 请求失败且没有 Usage | 503 时不写伪造 0 值 usage（当前 modeltrace 中间件未写 usage 属性，符合"未知不写"） |
| Trace 必须发送到唯一的自部署 Langfuse 项目 | 有效的自部署目标可用 | 只发到 `http://127.0.0.1:3000/api/public/otel/v1/traces` 一个目标 |
| 部署与运行时配置必须遵守确定优先级 | 只有部署配置 | 本 e2e 用部署配置（环境变量），运行时配置（admin API）未接 wire，属于已知缺口 |
| OTLP 传输必须保护内容与项目凭据 | 管理员配置非回环 HTTP 目标 | endpoint 强制 loopback，e2e 用 `127.0.0.1` 通过 |
| 观测故障必须 fail-open 且 MVP 不保证补送 | Langfuse 在非流式请求期间不可用 | 503 业务响应正常返回，trace 仍尝试发送；停 Langfuse 后业务不受影响（本 e2e 未跑此场景，属于 task 10.1） |

## 503 产生原因

sub2api 是 LLM 网关，收到 `/v1/chat/completions` 后要找可用的上游 LLM 账号（OpenAI/Anthropic/Grok）来转发。e2e 环境**故意不配上游账号**，因此：
1. 请求通过 API Key 鉴权 → 建立 user/api_key/group 身份
2. `modeltrace.Middleware` 在 `apiKeyAuth` 之后挂载，已创建根 Trace + Generation span
3. Handler 进入账号调度，无可用账号 → 返回 503 `Service temporarily unavailable`
4. Middleware 的 `defer span.End()` 捕获 503 状态码，写入 `http.response.status_code=503`
5. Generation span 的 `output` 是 `{"error":{"message":"Service temporarily unavailable","type":"api_error"}}`
6. BatchSpanProcessor 5 秒后 flush 到 Langfuse

**这正是 spec 的 fail-open 设计**：业务失败（503）也被完整追踪，而追踪链路本身不影响业务响应。

## 为什么不配真实上游账号

1. **成本**：真实 OpenAI/Anthropic 调用产生费用，e2e 每次跑都花钱不合理。
2. **确定性**：真实上游响应不稳定（限流、网络、模型变更），e2e 结果不可复现；503 是确定性响应。
3. **覆盖足够**：503 场景已覆盖 Trace 生成、span 层级、身份映射、fail-open、内容捕获；真实成功响应只是 `http.response.status_code=200` + 非错误 output，逻辑路径相同。
4. **隔离**：不配上游 = 不依赖外部 LLM 凭据，e2e 完全本地。

## 已实现的 OpenSpec tasks

| Task | 描述 | 实现文件 | 状态 |
|---|---|---|---|
| 1.1 | 首个端到端 OTLP Trace | `backend/internal/modeltrace/{exporter,middleware,manager_test}.go` | ✅ |
| 2.1 | 部署与运行时配置闭环 | `backend/internal/modeltrace/{config_manager,admin_handler,config_manager_test}.go` | ✅（wire 未接 ConfigManager，admin API 返回 503） |
| 2.2 | 传输安全校验 | `backend/internal/modeltrace/exporter_test.go` | ✅ |
| 7.2 | 独立 Session extractor | `backend/internal/modeltrace/{session,session_test}.go` | ✅ |

## 未实现的 OpenSpec tasks（建议列 GitHub Issue）

| Task | 描述 | 备注 |
|---|---|---|
| 2.1 前端 | 管理员前端配置区 | 需改 `frontend/src/views/admin/` |
| 3.1 | 路由矩阵 + 身份门禁 | 当前只接 `/v1/chat/completions`，其他协议入口未接 |
| 4.1 | 协议转换 + 重试 + 故障转移 attempt 层级 | 当前一个 Generation span，不区分多次上游尝试 |
| 5.1 | 有界内容 + 多模态 + 秘密隔离 | 当前有界捕获已实现，多模态默认元数据未实现 |
| 6.1 | SSE/流式/取消部分 response | 未实现 |
| 6.2 | WebSocket 多回合 | 未实现 |
| 7.1 | Usage/cost 精确映射 | 当前未写 usage 属性 |
| 8.1 | 全协议入口矩阵 | 未实现 |
| 9.1 | 异步/批量任务 SpanContext 续接 | 未实现 |
| 10.1 | 热切换不可变 generation + 引用计数 | 未实现，配置重启生效 |
| 11.1 | 全量门禁验收 | 未实现 |

## 与 sub2api 仓库规约的关系

项目 `AGENTS.md` 规定：
- 远端用 GitHub（`origin = Vitus213/sub2api`），跨 fork PR 用 `gh pr create --repo Wei-Shaw/sub2api`
- **禁止**触发 `antcode-skill`
- OTEL/e2e 工作分支用 `otel/` 前缀
- 本地测试凭据不进提交

本 skill 遵守该规约：脚本不 `git commit`/`git push`/`gh pr create`，只跑容器和 HTTP。