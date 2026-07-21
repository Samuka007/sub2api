# 环境与踩坑速查

## 端口

| 端口 | 服务 | 来源 compose |
|------|------|-------------|
| 3000 | Langfuse web UI + OTLP endpoint | langfuse-compose.yml |
| 3030 | Langfuse worker | langfuse-compose.yml |
| 8123 | ClickHouse HTTP | langfuse-compose.yml |
| 9000 | ClickHouse native | langfuse-compose.yml |
| 9090 | MinIO API | langfuse-compose.yml |
| 9091 | MinIO console | langfuse-compose.yml |
| 6379 | Langfuse Redis（内部） | langfuse-compose.yml |
| 5432 | Langfuse Postgres（内部） | langfuse-compose.yml |
| 15432 | sub2api 专用 Postgres | sub2api-deps-compose.yml |
| 16379 | sub2api 专用 Redis | sub2api-deps-compose.yml |
| 8080 | sub2api server（容器内） | run_e2e.sh `--network host` |
| 18080 | 预留 sub2api 宿主映射（未使用） | — |

实际从宿主访问 sub2api 用 `http://localhost:8080`，因为 `--network host` + Colima 端口转发。

## 凭据

| 用途 | 值 | 来源 |
|------|-----|------|
| Langfuse 登录 | `admin@local.dev` / `admin123456` | `LANGFUSE_INIT_USER_*` |
| Langfuse 项目 public key | `pk-lf-local` | `LANGFUSE_INIT_PROJECT_PUBLIC_KEY` |
| Langfuse 项目 secret key | `sk-lf-local` | `LANGFUSE_INIT_PROJECT_SECRET_KEY` |
| sub2api admin | `admin@e2e.local` / `admin123456` | `ADMIN_EMAIL` / `ADMIN_PASSWORD` |
| sub2api API Key | `sk-e2e-<16字节hex>` | `run_e2e.sh` `openssl rand -hex 16` |
| sub2api Postgres | `sub2api` / `sub2api` | `POSTGRES_USER` / `POSTGRES_PASSWORD` |

**所有凭据仅本地，不得写入提交、文档（除本 reference）、PR、log。日志输出时遮蔽为 `sk-e2e-***`。**

## 镜像版本

| 镜像 | 版本 | 备注 |
|------|------|------|
| `golang` | `1.26.5`（非 alpine） | 编译 sub2api，必须带 git 因为 `go mod` 需要 |
| `langfuse/langfuse` | `3` | `>= v3.22.0` 才有 OTLP endpoint |
| `langfuse/langfuse-worker` | `3` | 同上 |
| `clickhouse/clickhouse-server` | `latest` | Langfuse compose 默认 |
| `postgres` | `17` | Langfuse 与 sub2api 都用 |
| `redis` | `7` | 同上 |
| `minio/minio` | `latest` | Langfuse 对象存储 |

docker hub 偶发拉取超时（`EOF` / `failed to fetch anonymous token`），重试即可；不要改用第三方镜像源以免版本漂移。

## Endpoint 校验规则

sub2api 的 `config.validModelTracingEndpoint`（`backend/internal/config/config.go`）强制：
- `https://` 永远允许
- `http://` 仅允许 host 为 `localhost` 或字面量回环 IP（`127.0.0.1`/`::1`）
- 其他 scheme 拒绝
- **不**提供 `InsecureSkipVerify` 或跳过证书校验开关

因此 e2e 必须用 `MODEL_TRACING_ENDPOINT=http://127.0.0.1:3000`，**禁止** `http://host.docker.internal:3000`（非字面回环会被拒）或 `http://sub2api-langfuse-langfuse-web-1:3000`（非 loopback）。

这就是 `run_e2e.sh` 用 `--network host` + `127.0.0.1` 的原因：让 sub2api 容器内 127.0.0.1 等于 VM 的 127.0.0.1，从而既符合 endpoint 校验又能访问 Langfuse 暴露的 3000 端口。

## ClickHouse 表结构（Langfuse v3）

### `traces` 表关键列

| 列名 | 类型 | 说明 |
|------|------|------|
| `id` | String | trace id |
| `name` | String | 根 span name（应为 `model.request`） |
| `user_id` | String | Langfuse user id（应为 `1`） |
| `session_id` | String | Langfuse session id（应等于请求 body `session_id`） |
| `metadata` | Map(LowCardinality(String), String) | **Map 类型**，不是 JSON 字符串，访问用 `metadata['key']` |
| `input` | Nullable(String) | 客户端请求 body JSON |
| `output` | Nullable(String) | 客户端响应 body JSON |
| `tags` | Array(String) | trace 标签 |

常见错误：`SELECT JSONExtractString(metadata, 'key')` 报 `illegal type: Map`，因为 metadata 是 Map 不是 JSON。正确写法：`metadata['key']`。

### `observations` 表关键列

| 列名 | 类型 | 说明 |
|------|------|------|
| `id` | String | observation id |
| `trace_id` | String | 关联 trace |
| `name` | String | span name（`model.request` 或 `generation`） |
| `type` | LowCardinality(String) | `SPAN` / `GENERATION` |
| `parent_observation_id` | Nullable(String) | 父 observation；根 span 为 NULL，Generation 子 span 指向根 |
| `provided_model_name` | Nullable(String) | Generation 应为 `gpt-4` |
| `input` | Nullable(String) | 该 observation 的输入 |
| `output` | Nullable(String) | 该 observation 的输出 |
| `start_time` / `end_time` | DateTime64(3) | 起止时间 |

## 踩坑速查

| 现象 | 原因 | 解法 |
|------|------|------|
| `config file creation failed: open /data/config.yaml: no such file or directory` | `DATA_DIR` 没挂卷 | `docker volume create sub2api-e2e-data` + `-v sub2api-e2e-data:/data` |
| `invalid model_tracing deployment config; tracing disabled` | endpoint 非 loopback | 改用 `http://127.0.0.1:3000` + `--network host` |
| `database connection failed: pq: password authentication failed for user "postgres"` | AUTO_SETUP 用 `DATABASE_*` 环境变量，不是 `DB_*` | 全部用 `DATABASE_HOST`/`DATABASE_PORT`/`DATABASE_USER`/... |
| `NeedsSetup=false` 跳过 AUTO_SETUP | 上次写的 config.yaml 还在 /data 卷里 | `docker volume rm sub2api-e2e-data` 或脚本里 `DROP SCHEMA public CASCADE` 重置 |
| API Key 响应里 `key: "[openai_token_redacted]"` | sub2api 对 OpenAI 平台 group 的 key 做了 redact 展示 | 直接 `docker exec` 进 PG 用 `UPDATE api_keys SET key='sk-...'` 改成可鉴权值 |
| `/v1/chat/completions` 返回 503 | 没配上游 LLM 账号 | **预期行为**，trace 仍落 Langfuse，不要尝试"修复" |
| Langfuse Postgres `traces` 表 0 条 | Langfuse v3 用 ClickHouse 存 traces，PG 只是 metadata | 查 `docker exec sub2api-langfuse-clickhouse-1 clickhouse-client ...` |
| ClickHouse `traces` 表 0 条 | BatchSpanProcessor 默认 5 秒批次 + 网络 | `sleep 6` 后再查 |
| `docker compose` 报 `unknown command` | Colima 内 docker 是老版本 | 用 `docker-compose`（带横线） |
| `docker-compose` 报 `pull access denied for registry.cn-hangzhou.aliyuncs.com/...` | 中间尝试过第三方镜像但没权限 | 回到 `docker.io/library/...` 标准镜像，重试拉取 |
| Colima 端口从宿主访问不通 | 某些端口只绑 `127.0.0.1`（VM 内） | sub2api 用 `--network host` 绕过；Langfuse web 暴露 `0.0.0.0:3000` |
| `pricing_service` 报 TLS handshake timeout | GitHub raw 偶发不通 | 非致命，pricing 服务降级；等 retry 或离线跑 |

## 与 sub2api-admin skill 的关系

- `sub2api-admin`：用 CLI 管理 sub2api admin API（账号、分组、兑换码等），面向**生产运维**。
- `sub2api-model-trace-e2e`（本 skill）：本地**测试**模型追踪链路，部署 Langfuse + sub2api 临时环境，面向**开发验证**。

两者不重叠：本 skill 不调用 sub2api-admin CLI，直接用 curl + docker exec 完成所有操作。