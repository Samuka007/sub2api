# release-1.0.3 生产部署记录

## 发布标识

| 项目 | 值 |
| --- | --- |
| GitHub Release 时间 | `2026-07-28 03:59:18`（Asia/Shanghai） |
| 生产部署时间 | `2026-07-28 04:06:04`（Asia/Shanghai） |
| 发布标签 | `release-1.0.3` |
| Tag object | `2b0217c7c229499e3187a7585d2b6edddd46a1df` |
| Git 提交 | `13248c9931255c5ca8bdc4ed41c2536445659ea9` |
| 紧急修复 PR | [`Alle-Group/sub2api#27`](https://github.com/Alle-Group/sub2api/pull/27)（Responses 消费账单幂等 ID） |
| 官方基线 | `v0.1.164` / `cd8bb98c44303b2c8f04c0da340447c992f0cb7d` |
| 程序版本 | `1.0.3` |
| 发布镜像 | `ghcr.io/alle-group/sub2api:1.0.3` |
| 生产固定镜像 | `ghcr.io/alle-group/sub2api@sha256:028dbb92507fdb4903b13b68a4a71e84f6a200920b75c7a89040fa037f7595c9` |
| Registry index digest | `sha256:028dbb92507fdb4903b13b68a4a71e84f6a200920b75c7a89040fa037f7595c9` |
| `linux/amd64` manifest digest | `sha256:7dfa80cea5e347de01d1abcbc4d6139cfd9003570fd48b4216bb5d4391b829c8` |
| 生产镜像 ID | `sha256:09a0e0e77f1533dda8234222e6012fed5952d0f6c84d8c0905e8c2ca1e43166b` |
| 镜像构建时间 | `2026-07-27T19:58:28.688346473Z` |
| 平台 | `linux/amd64` |
| Main Code Quality | [`30296420151`](https://github.com/Alle-Group/sub2api/actions/runs/30296420151) |
| Release workflow | [`30297949654`](https://github.com/Alle-Group/sub2api/actions/runs/30297949654) |
| GitHub Release | [`release-1.0.3`](https://github.com/Alle-Group/sub2api/releases/tag/release-1.0.3) |

## 发布内容

- Responses/WS 计费幂等 ID 改为优先使用上游响应 ID，其次使用上游请求 ID，最后才生成内部 ID。
- Generic Gateway 的 Anthropic 到 Responses 流式与缓冲转换、Chat Completions fallback 均保留上游 `ResponseID`。
- `/v1/responses` 和 `/v1/responses/compact` 不再使用调用方可控的请求头作为计费幂等键；非 Responses 路径保持原行为。
- `google.golang.org/grpc` 升级到 `v1.82.1`，并同步必要的 `genproto` 版本，修复 `GO-2026-6061`。
- 本次没有 Ent Schema、SQL migration、新生产配置项或 Compose 兼容性变化。
- 本次只阻止继续漏计，没有调查、计算或补偿历史漏计金额。

## 构建与发布验证

- `release-1.0.3` 是正文非空的 annotated tag，固定指向已合入 `main` 的提交 `13248c9931255c5ca8bdc4ed41c2536445659ea9`。
- 该提交的独立 Main Code Quality push run 全部适用任务通过，包括前后端质量与安全、后端 unit/integration、lint、构建和部署契约。
- Release workflow 对 tag 固定提交重新执行同一质量门禁，并成功完成发布解析、前端制品、GoReleaser、多平台归档、GHCR 镜像、GitHub Release 和 VERSION 同步。
- Responses 专项测试覆盖响应 ID 优先级、compact、重复调用方请求头、同线程不同 turn、fallback 和 Generic 转换路径。
- GitHub Packages API、OCI manifest 和生产拉取结果对 registry index digest 的读回一致；生产平台 manifest 为 `linux/amd64`。
- Release workflow 已将 `main` 上的 `backend/cmd/server/VERSION` 同步为 `1.0.3`，同步提交为 `fd2376cb09d1891532aa6230457d388967986b11`。
- 没有使用或覆盖 `latest`；生产服务器没有拉取源码、安装构建依赖或执行构建。

## 部署前保护

部署前从实际生产容器重新读取回滚点，没有沿用过期的 `release-1.0.1` 信息：

```text
版本：1.0.2
OCI revision：bd2ebd1c874e9048470bbb699496da34e32aed57
固定镜像：ghcr.io/alle-group/sub2api@sha256:22863680d90e9ed95348fc31e4dcddb433f33e09e804f9f755094fcdcf0d981f
生产镜像 ID：sha256:82d76c02b5e3396092176c50350acae51bf0c815f2d222b43ab920247e1db1b1
```

部署前应用为 `running`、`healthy`、`RestartCount=1`、`OOMKilled=false`。该重启计数来自 `release-1.0.2` 部署后已记录的生产主机异常重启，不是本次部署事件。

本次没有数据库 schema 变化、迁移或破坏性管理功能，因此没有为该镜像切换新增数据库备份。数据恢复边界保持不变：镜像回滚不回滚 PostgreSQL 或 Redis 数据卷。

## 部署范围

生产使用：

```text
/srv/sub2api-prod/compose.apps.yml
/srv/sub2api-prod/compose.release-1.0.3.yml
```

新覆盖文件从现场 `compose.release-1.0.2.yml` 复制，只把 `services.sub2api.image` 替换为新的固定 registry index digest。文件权限为 `0644 root:root`，SHA-256 为 `0616422bd3c7713bc1c1aa229ca2d41553a2944c66d53c132e371a515ebc51ce`；`docker compose config --quiet` 通过，解析后的应用镜像与新 digest 精确一致。

部署命令只重新创建应用容器：

```bash
docker compose \
  -f compose.apps.yml \
  -f compose.release-1.0.3.yml \
  up -d --no-build --no-deps sub2api
```

PostgreSQL、Redis、Caddy、OTel Collector、Langfuse X-Ray Portal、moderation adapter 和 prompt audit adapter 的容器 ID、镜像 ID 及重启次数在部署前后均未变化。

## 验收结果

- `sub2api` 实际运行新的固定 digest，生产镜像 ID、`linux/amd64` 平台、OCI version `1.0.3` 和 OCI revision `13248c9931255c5ca8bdc4ed41c2536445659ea9` 均匹配。
- 应用在约 9 分钟的两轮检查中均为 `running`、`healthy`、`RestartCount=0`、`OOMKilled=false`、`ExitCode=0`。
- PostgreSQL、Redis、Caddy、OTel Collector、Langfuse X-Ray Portal 和两个审核适配器保持原容器运行，未被 Compose 重建。
- `https://sub.scitrace.cc/health` 返回 HTTP `200` 和 `{"status":"ok"}`；`/setup/status` 返回 `code=0`、`needs_setup=false`。
- `/api/v1/settings/public` 返回公开版本 `1.0.3`；前端首页返回 HTTP `200`。
- 未携带 API Key 请求 `/v1/models` 返回 HTTP `401`；携带调用方自定义 `Idempotency-Key` 但未认证请求 `/v1/responses` 同样返回 HTTP `401`。
- 新容器启动后的真实日志窗口内，`DPANIC`、`PANIC`、`FATAL`、migration 错误、`usage billing request fingerprint conflict` 和 `record_usage_failed` 计数均为 `0`。
- 宽泛 `panic` 文本扫描曾命中 `INFO model trace export health` 的属性，按真实日志级别复核后确认不是 panic。窗口内仍有既有 Prompt Audit 异步失败，并观察到一次正常 failover 的上游 HTTP `502`；两者与本次计费补丁无关。

## 验收边界和后续项

- 生产没有提供无人并发使用的专用 API Key。为避免产生真实上游消费，本次没有执行成功 Responses 请求，也没有读取用户余额或账单金额；核心计费 ID 行为由 tag 制品一致性、专项 unit/integration 测试和部署后的被动冲突日志窗口共同验证。
- 历史漏计范围、金额和补偿不在本次紧急补丁范围内，后续必须作为独立审计任务处理。
- Generic 超长上游 ID 归一化、生成 fallback ID 的重试级缓存属于后续完整修复项，本次没有扩大补丁范围。
- `release-1.0.2` 已记录的 Prompt Audit 端点/协议问题仍然存在，需独立修复；它不阻断当前 API 转发和本次计费补丁验收。

## 回滚

如发现本次发布回归，只将 `sub2api` 应用切回部署前已验证的固定镜像：

```text
ghcr.io/alle-group/sub2api@sha256:22863680d90e9ed95348fc31e4dcddb433f33e09e804f9f755094fcdcf0d981f
```

在 `/srv/sub2api-prod` 执行：

```bash
docker compose \
  -f compose.apps.yml \
  -f compose.release-1.0.2.yml \
  up -d --no-build --no-deps sub2api
```

不要回滚或重建 PostgreSQL、Redis 数据卷及其他服务。回滚镜像不会撤销已经成功写入的 usage 或账单记录，也不会恢复历史漏计数据。
