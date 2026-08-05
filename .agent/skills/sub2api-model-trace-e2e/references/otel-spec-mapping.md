# OTEL Spec 映射与 503 设计依据

## 本 e2e 验证的行为契约映射

| Spec Requirement | Scenario | e2e 验证点 |
|---|---|---|
| 所有模型推理与生成请求必须具有唯一 Trace | 已识别请求在真实发送前失败 | HTTP 503；按唯一 session/request ID 只有 1 条 Trace、1 个根 SPAN、0 GENERATION |
| 身份建立前不得创建 Trace | 匿名请求、未知 API Key | HTTP 401，按唯一 request ID 查询均为 0 Trace |
| 已识别身份的鉴权失败必须追踪 | disabled API Key | HTTP 401；1 个 ERROR 根 SPAN、0 GENERATION，且 user/API Key/group 数值身份正确 |
| 控制面不得创建模型 Trace | 已鉴权 `GET /v1/models` | HTTP 200，按唯一 request ID 查询为 0 Trace |
| 一个 Trace 必须保留完整尝试层级 | 本地 fixture 固定 429 后切换账号 200 | 一个根 Trace；`upstream.attempt.1/2` 两个真实 GENERATION 均为根的直接子项，账号和 ERROR/成功状态可区分 |
| Trace 必须区分客户端与上游内容视角 | OpenAI Chat Completions → Anthropic Messages | 根保存客户端 input/output，attempt 保存转换后 input 与 Anthropic SSE output；上游响应 credential 字段只出现 `[REDACTED]` |
| 有界内容、多模态和秘密隔离 | 2 KiB Prompt 上限 + Base64 图片 + 分离 canary | 有确定性截断标记和媒体 fingerprint/approx_bytes；请求字段 secret、媒体正文、用户/上游 API Key、上游响应 secret 在真实 Langfuse 中命中数为 0 |
| 部署与运行时内容上限可观察并真实生效 | 启动不覆盖 16/8/16 MiB 部署默认；运行时切到 1 MiB 后发送 1,040,000 字节正文 | admin GET 的 prompt/response/media 分别为 16777216/8388608/16777216；Langfuse root input 保留 head/tail canary、长度接近 1 MiB 且无截断标记 |
| 流式请求正常完成 | 单成功账号返回完整 Anthropic SSE | 客户端 HTTP 200、`text/event-stream`、content/finish/[DONE] 帧；Langfuse 只有一个根 Trace，真实 attempt GENERATION 是根的直接子项，根状态为 `completed` |
| 异步/批量模型执行必须续接提交 Trace | Gemini Batch API 200 item（一个成功、199 个 provider 失败） | API 200、worker `completed`；同一逻辑 Trace 下 1 个提交根 SPAN、200 个直系 `model.async.execution` GENERATION，API 与 Langfuse 的 `item_id` 均为精确且唯一的 `item-0..199` 集合，fingerprint 均匹配，终态 1 个 `completed` / 199 个 `failed`；媒体 canary 零泄露 |
| 认证凭据永远不得进入 Trace | 配置 API、客户端 Header、上游请求/响应 | 配置响应不回显 secret；ClickHouse 对本次所有 observation/trace 执行完整 canary 零命中查询 |
| Trace 必须发送到唯一的自部署 Langfuse 项目 | 本地目标可用 | 只配置 `http://127.0.0.1:3000`；公开 health 版本必须 `>=3.22.0`，并记录 OCI version/revision/digest |
| 部署与运行时配置必须遵守确定优先级 | 部署默认、运行时小上限、运行时 1 MiB 上限、secret 保留与 CAS | 先验证 deployment version 0，再运行时 version 1/2/3；远端明文 HTTP 被拒且不改旧配置，空 secret 保留，过期 version 返回 409 |
| 观测故障必须 fail-open 且 MVP 不保证补送 | OTLP 端点固定 500 与 1.5 秒延迟 | 两次业务请求均在约 50ms 内返回 200；fixture 分别观测到 `otlp_500>=1`、`slow_exports>=1`，证明 exporter 错误与阻塞不拖住业务路径 |

## 503、failover、SSE 与异步 batch 的本地执行链

1. Candidate middleware 在模型执行路由安装延迟身份 Hook；匿名/未知 Key 阶段不创建 Span。
2. API Key 仓储解析出真实身份后创建唯一根 Trace。
3. 原始 `e2e-key` group 在身份、截断和 1 MiB 场景尚无账号，因此 Handler 确定性返回 503；这些 Trace 只有根 SPAN，不得虚构 attempt。
4. 独立 failover group 挂两个本地 Anthropic 账号：priority 1 指向 `/fail` 固定 429，priority 2 指向 `/ok` 固定 200 完整 SSE；一次非流式客户端请求产生两个真实 attempt GENERATION。
5. 完成 503 场景后，原始 group 只挂一个 `/ok` 账号；`stream=true` 请求经协议转换把本地 Anthropic SSE 作为 OpenAI SSE 返回，客户端必须收到内容帧、成功终态和 `[DONE]`。
6. 独立 Gemini batch group 通过本地一次性 CA 把 `generativelanguage.googleapis.com:443` 定向到 TLS fixture；提交 API 保存最小 trace continuation，真实 queue worker 完成 upload/create/poll/download 后，为配置上限 200 个 item 在原提交根下分别结束 1 个成功与 199 个失败 Generation。
7. 根 finalizer 捕获最终客户端状态与安全 input/output；BatchSpanProcessor flush 后脚本在 Langfuse ClickHouse 断言 cardinality、父子关系、续接 fingerprint、状态和 canary。

这四类路径分别证明“未发送不造 attempt”“真实 429→200 保留全部 attempt”“正常 SSE 保留帧与 completed 终态”“真实 queue/worker 续接同一 Trace 且不泄露媒体”，不能互相替代。

## 为什么不调用真实外部 LLM

1. **成本与凭据**：不使用生产/个人 LLM Key，不产生外部费用。
2. **确定性**：本地 Go fixture 固定提供 Anthropic 健康 200、失败 429、完整 SSE，以及 Gemini File/Batch API 的 upload/create/poll/download；不受限流、网络和模型版本漂移影响。
3. **覆盖边界**：无账号 503、故障转移 429→200、非流式协议转换、真实 SSE 正常完成和真实 queue/worker 异步续接都在同一隔离环境可复现。
4. **不等价声明**：本地 fixture 证明网关、异步 worker 与 Langfuse 链路，不证明任一外部厂商服务当前可用，也不证明生产网络、证书或配额。

## 当前 e2e harness 覆盖边界

完整 smoke 已覆盖匿名/未知/控制面零 Trace、已识别 503/401 单根 Trace、Chat Completions→Anthropic 故障转移、内容截断与 secret canary、正常 SSE、运行中配置快照、500/慢 exporter fail-open，以及真实 Gemini Batch queue/worker。

以下场景仍需由聚焦 Go 测试或全规模验收补充，不能由单次真实 Langfuse smoke 替代：

- 客户端断连、上游部分失败和 Responses WebSocket 多回合/配置切换；
- Usage Log 与 token/cost 逐字段比对，以及完整 Session allowlist；
- 全同步协议/入口矩阵；
- 异步 fingerprint 错配、目标切换、关闭、重试与取消；
- 目标切换、序列化失败和进程崩溃的完整 fail-open 对照；
- `run_full_e2e.sh` 的全后端/前端测试、race、production build、200-item 黑盒和本机 benchmark。

## 与 sub2api 仓库规约的关系

项目 `AGENTS.md` 规定：
- 远端用 GitHub（`origin = Alle-Group/sub2api`，`upstream = Wei-Shaw/sub2api` 且只读），Pull Request 只提交到 `origin`
- **禁止**触发 `antcode-skill`
- OTEL/e2e 工作分支用 `otel/` 前缀
- 本地测试凭据不进提交

本 skill 遵守该规约：脚本不 `git commit`/`git push`/`gh pr create`，只跑容器和 HTTP。