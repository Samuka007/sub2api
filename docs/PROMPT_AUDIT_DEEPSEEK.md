# DeepSeek Prompt Audit Adapter

本文面向部署和维护 Prompt Audit 的管理员，定义如何使用官方 DeepSeek `deepseek-v4-flash` 作为安全分类器。行为真源位于 `backend/internal/securityaudit/deepseekadapter/`，部署入口为 `deploy/docker-compose.prompt-audit-deepseek.yml`。

## 架构与契约

Sub2API 的 Prompt Audit Scanner 使用 OpenAI-compatible `POST /v1/chat/completions`，并要求响应正文严格包含：

```text
Safety: Safe|Controversial|Unsafe
Categories: None|<Qwen3Guard category list>
```

普通通用模型不会自然遵守该格式。Adapter 将待审文本放入显式的 `BEGIN_UNTRUSTED_TEXT` 边界，注入不可执行用户指令的分类 System Prompt，要求 DeepSeek 返回严格 JSON，再校验 `safety` 和已知类别，最后转换成 Scanner 现有格式。上游 429 保持 429，瞬时网络/超时/5xx 返回 502；永久上游 4xx、非法或矛盾 JSON、未知 safety/category、空响应和超大响应返回 422，使现有 Scanner 区分可重试与终态失败。所有错误都使用脱敏正文，上游响应正文和待审提示词不会写入 Adapter 日志。

支持模型：

- `deepseek-v4-flash`：默认，适合低延迟影子审计。
- `deepseek-v4-pro`：可选，成本与延迟更高。

上游地址固定限制为官方 `https://api.deepseek.com`，避免把审计凭据发送到任意管理员输入的主机。

## 准备 Secret

API Key 和 Adapter 共享 Token 必须是不同的值，并以文件提供：

```bash
install -d -m 0700 ./secrets
umask 077
printf '%s' "$DEEPSEEK_API_KEY" > ./secrets/deepseek_api_key
openssl rand -hex 32 > ./secrets/prompt_audit_adapter_token
chmod 0400 ./secrets/deepseek_api_key ./secrets/prompt_audit_adapter_token
```

不得把 Secret 内容写入 `.env`、Compose、Issue、PR、普通日志或镜像层。

## 启动 Adapter

先拉取通过发布门禁的固定镜像 digest，再组合部署文件：

```bash
export SUB2API_IMAGE='ghcr.io/alle-group/sub2api@sha256:<release-index-digest>'
export PROMPT_AUDIT_DEEPSEEK_API_KEY_FILE="$PWD/secrets/deepseek_api_key"
export PROMPT_AUDIT_DEEPSEEK_ADAPTER_TOKEN_FILE="$PWD/secrets/prompt_audit_adapter_token"

/bin/sh deploy/prompt-audit-deepseek-up.sh
```

启动脚本会拒绝 tag、`latest`、短 digest 和非预期镜像仓库，只接受 `ghcr.io/alle-group/sub2api@sha256:<64 hex>`。该服务不发布宿主机端口，只加入 `app-local` 私网；文件系统只读、删除全部 Linux capability，并启用 `no-new-privileges`，同时限制 PID、内存和 CPU。默认使用 Go HTTP/1.1。只有在同一目标上稳定复现 HTTP/2 `PROTOCOL_ERROR` 或帧解析异常，且系统 curl HTTP/1.1 差分成功时，才设置：

```bash
export PROMPT_AUDIT_DEEPSEEK_TRANSPORT=curl
```

curl 模式会在容器私有 `/tmp` 中创建 `0600` 临时认证配置；Token 不进入进程参数，容器停止后 tmpfs 自动销毁。两种传输都不继承代理配置；curl 以 `-q` 首参数禁用用户级配置，避免认证头被隐式代理或 `.curlrc` 转发。

## 配置 Sub2API

在「安全审计 → 提示词审计」新增节点：

| 字段 | 值 |
| --- | --- |
| Protocol | `openai_compatible` |
| Base URL | `http://prompt-audit-deepseek-adapter:8787` |
| Model | `deepseek-v4-flash` |
| Token | `prompt_audit_adapter_token` 文件内容 |
| Timeout | 建议先使用 `15000`–`30000` ms |
| Input limit | `4000` |

首次只选择测试分组，开启异步审计并关闭同步阻断。连接测试必须返回 2xx，随后至少验证一条普通输入得到 `pass/Allow`，一条越狱输入得到 `critical/Block/Jailbreak`，并确认没有持续 `unavailable`、重试或队列堆积。

## 配置内容审计

同一 Adapter 也提供 OpenAI-compatible `POST /v1/moderations`。在「安全审计 → 内容审计」中配置：

| 字段 | 值 |
| --- | --- |
| 审核上游协议 | `OpenAI Moderations` |
| 上游 Base URL | `http://prompt-audit-deepseek-adapter:8787` |
| 模型名 | `deepseek-v4-flash` |
| 上游 API Key | `prompt_audit_adapter_token` 文件内容 |
| 全局模式 | 首次使用 `仅观察` |
| 采样率 | 验收期间 `100%` |

Adapter 接受单个文本、文本数组及 OpenAI 文本 part，并返回完整的 `flagged`、`categories` 与 13 项 `category_scores`。每批最多 32 项、每项最多 16384 Unicode 字符，整批共享配置的上游超时。

`deepseek-v4-flash` 是文本模型，不能检查图片。包含 `image_url` 的请求会明确返回 `422`，而不是忽略图片后生成“安全”结果；因此在图片审计覆盖完成前，不得把该 Adapter 作为图片输入的唯一同步阻断器。先保持「仅观察」，用人工标注样本验证误报率、召回率及各类别阈值，再单独审批是否切换「前置拦截」。
## 上线门禁

两条 smoke 只证明协议闭环，不代表可以生产阻断。扩大范围前至少需要人工标注的正常与风险样本集，计算误报率、阻断精确率、召回率、可用率和 P95/P99。未达到团队阈值前保持异步；未经故障演练不得启用 fail-closed 同步阻断。

发送到 DeepSeek 的内容是完整或缩窄后的 Prompt Audit 扫描文本。部署前必须确认数据地域、隐私和保留要求允许把所选测试分组的文本发送给该外部服务。

## 回滚

1. 在 Sub2API 中关闭同步阻断，再关闭 Prompt Audit 或移除测试分组。
2. 停止 `prompt-audit-deepseek-adapter` 服务。
3. 恢复变更前的 Prompt Audit 配置版本。
4. 不回滚 PostgreSQL 或 Redis 数据卷；按既定保留规则处理测试审计事件。
5. 撤销不再使用的 DeepSeek API Key 和 Adapter Token。

配置、镜像或 Secret 任一项无法验证时，保持审计关闭，不以普通聊天模型响应冒充安全分类结果。
