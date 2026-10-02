# release-1.0.4 生产部署记录

## 发布标识

| 项目 | 值 |
| --- | --- |
| GitHub Release 时间 | `2026-07-28 06:47:17`（Asia/Shanghai） |
| 生产部署时间 | `2026-07-28 06:52:26`（Asia/Shanghai） |
| 发布标签 | `release-1.0.4` |
| Tag object | `bf0872f3086bf07a65a46bb63b871a6a237dabd1` |
| Git 提交 | `d4edac4efe6c30c3e8304b53ec74c8c1bb836a07` |
| 完整修复 PR | [`Alle-Group/sub2api#29`](https://github.com/Alle-Group/sub2api/pull/29)（Responses 消费计费 ID 稳定性） |
| 官方基线 | `v0.1.164` / `cd8bb98c44303b2c8f04c0da340447c992f0cb7d` |
| 程序版本 | `1.0.4` |
| 发布镜像 | `ghcr.io/alle-group/sub2api:1.0.4` |
| 生产固定镜像 | `ghcr.io/alle-group/sub2api@sha256:082232bab72f5d662a6a3f1524fa823b338fab54369a8284bf6525ce805a4179` |
| Registry index digest | `sha256:082232bab72f5d662a6a3f1524fa823b338fab54369a8284bf6525ce805a4179` |
| `linux/amd64` manifest digest | `sha256:4f65abb8b69bf6ed73f81f787c877d1b845b4720fc783703cdd127e35f038eee` |
| `linux/arm64` manifest digest | `sha256:394c869f2b58046d11fd9d7e61f578d1bf0010130cb6963ce036759e28fde1f7` |
| 生产镜像 ID | `sha256:49e4e686e1330f3e83c86b3e52624ae62948b2a03b4fcd8c0748730ad9756002` |
| 镜像构建时间 | `2026-07-27T22:46:28.354317684Z` |
| 目标平台 | `linux/amd64` |
| Main Code Quality | [`30308674860`](https://github.com/Alle-Group/sub2api/actions/runs/30308674860) |
| Release workflow | [`30309844691`](https://github.com/Alle-Group/sub2api/actions/runs/30309844691) |
| GitHub Release | [`release-1.0.4`](https://github.com/Alle-Group/sub2api/releases/tag/release-1.0.4) |
| VERSION 同步提交 | `d739d788014b2d7cea7a595a2972c38c9375fd26` |

## 根因与修复范围

计费去重表允许保存最多 255 字符的请求 ID，但 `usage_logs.request_id` 只能保存 64 字符。旧路径可能先完成扣费，随后因超长 ID 写入 usage log 失败；Generic Responses 的 fallback ID 在同一次执行重入时还会重新生成，存在绕过去重的风险。

- 最终计费 ID 超过 64 字节时，统一归一化为 `sha256:<Raw URL-safe Base64 SHA-256>`，总长度固定为 50 字符。
- Generic `/v1/responses`、`/v1/responses/compact` 及对应 Responses 执行路径在 `ForwardResult` 上缓存执行级计费 ID，优先复用响应 ID，其次使用上游请求 ID，最后生成内部 fallback ID。
- 同一次执行的计费重入复用同一个 ID，不同执行生成不同 fallback ID。
- 普通 Messages 和 Chat Completions 保持原有 context-first 行为，不缓存 context ID。
- `UsageBillingCommand.Normalize` 增加相同的长度保护，避免其他调用方绕过归一化。
- 本次没有 Ent Schema、SQL migration、Wire、运行时配置或 Compose 兼容性变化。
- 本次只修复继续漏计的技术路径，没有调查、计算或补偿历史漏计金额。

## 构建与发布验证

- PR `#29` 已通过独立 Code Quality run [`30307671398`](https://github.com/Alle-Group/sub2api/actions/runs/30307671398)，并 squash merge 到 `main`。
- 发布提交的 Main Code Quality push run 全部适用任务通过，覆盖前后端质量、安全、后端 unit/integration、lint、构建和部署契约。
- Release workflow 对 tag 固定提交重新执行相同质量门禁；GoReleaser、GitHub Release、GHCR 多平台镜像和 VERSION 同步均成功。
- 本地定向计费测试、生产包编译、`go vet -tags=unit ./internal/service`、`gofmt` 和 `git diff --check` 均通过；两轮独立审查未发现阻断问题。
- GitHub Packages API 与生产服务器上的 `docker buildx imagetools inspect --raw` 对 index、`linux/amd64` 和 `linux/arm64` digest 的读回一致。
- `release-1.0.4` 是正文非空的 annotated tag，精确指向已合入 `main` 的提交；没有使用或覆盖 `latest`。

本机完整 service 包仍会被既有的 Content Moderation runtime snapshot 和 Ollama singleflight 时序用例影响；两项均不在本次计费调用链上，GitHub Linux unit/integration/build 已全部通过。本机 `CGO_ENABLED=0` 且没有 `gcc`/`clang`，因此未执行 `-race`。

## 部署前保护

部署前从实际运行容器重新读取的精确回滚点为：

```text
版本：1.0.3
OCI revision：13248c9931255c5ca8bdc4ed41c2536445659ea9
固定镜像：ghcr.io/alle-group/sub2api@sha256:028dbb92507fdb4903b13b68a4a71e84f6a200920b75c7a89040fa037f7595c9
生产镜像 ID：sha256:09a0e0e77f1533dda8234222e6012fed5952d0f6c84d8c0905e8c2ca1e43166b
应用容器 ID：16c1937cd7fee70619648fc1f1d86602dd483055369c493e9c83e0acfaeeabc1
```

部署前应用为 `running`、`healthy`、`RestartCount=0`、`OOMKilled=false`，公开 `/health` 正常。磁盘可用空间约 `24G`。本次没有数据库 schema 或数据迁移，因此没有为镜像切换新增数据库备份；恢复边界保持不变，不回滚 PostgreSQL 或 Redis 数据卷。

## 部署范围

生产使用：

```text
/srv/sub2api-prod/compose.apps.yml
/srv/sub2api-prod/compose.release-1.0.4.yml
```

新覆盖文件从现场 `compose.release-1.0.3.yml` 复制，只把 `services.sub2api.image` 替换为新的固定 registry index digest。文件权限为 `0644 root:root`，SHA-256 为 `069a9974e0e27a910236d663b4b511e40d145da643d5fd986f15d30901416738`；`docker compose config --quiet` 通过，解析后的应用镜像与目标 digest 精确一致。

部署命令只重建应用容器：

```bash
docker compose \
  -f compose.apps.yml \
  -f compose.release-1.0.4.yml \
  up -d --no-build --no-deps sub2api
```

PostgreSQL、Redis、Caddy、OTel Collector、Langfuse X-Ray Portal、moderation adapter 和 prompt audit adapter 的容器 ID 在部署前后均未变化。

## 验收结果

- 新应用容器 ID 为 `ceb47804b9bc7ca142f5bf6a1148655c20c4793d25363f2d57400eea94a288fb`；固定镜像、生产镜像 ID、`linux/amd64` 平台、OCI version `1.0.4` 和 OCI revision 均匹配。
- 即时检查和启动约 8 分钟后的复验均为 `running`、`healthy`、`RestartCount=0`、`OOMKilled=false`、`ExitCode=0`。
- `https://sub.scitrace.cc/health` 返回 HTTP `200` 和 `{"status":"ok"}`；`/setup/status` 返回 `code=0`、`needs_setup=false`、`step=completed`。
- `/api/v1/settings/public` 返回公开版本 `1.0.4`；前端首页返回 HTTP `200` 和 `text/html`。
- 未携带 API Key 的 `/v1/models` 返回 HTTP `401`；带调用方 `Idempotency-Key` 但未认证的 `/v1/responses` 同样返回 HTTP `401`，没有产生上游消费。
- 从新容器启动时间开始统计，`DPANIC/PANIC/FATAL`、migration error、`usage billing request fingerprint conflict` 和 `record_usage_failed` 均为 `0`。
- 部署后的真实业务流量中可观察到 Responses 请求正常完成，但本次没有读取用户余额、账单金额或发起专用的可计费生产请求。

日志窗口内仍存在此前已知的 Prompt Audit adapter 不可用错误、个别失效账号的 quota 查询警告及正常连接取消；这些信号与本次计费 ID 修复无关，也没有造成应用不健康或请求转发停止。

## 验收边界与后续项

- 生产没有提供可供无人值守并发测试的专用 API Key。核心计费 ID 行为由 tag 制品一致性、定向 unit/integration 测试、完整 CI 和部署后的被动错误日志窗口共同验证。
- 历史漏计范围、金额与补偿不在本次上线范围内，必须作为独立审计任务处理。
- 本次没有修复既有 Prompt Audit endpoint/协议问题，也没有处理失效上游账号；两者不阻断当前 API 转发和本次计费修复验收。

## 回滚

如发现本次发布回归，只将 `sub2api` 应用切回部署前已验证的固定镜像：

```text
ghcr.io/alle-group/sub2api@sha256:028dbb92507fdb4903b13b68a4a71e84f6a200920b75c7a89040fa037f7595c9
```

在 `/srv/sub2api-prod` 执行：

```bash
docker compose \
  -f compose.apps.yml \
  -f compose.release-1.0.3.yml \
  up -d --no-build --no-deps sub2api
```

不要回滚或重建 PostgreSQL、Redis 数据卷及其他服务。镜像回滚不会撤销已经成功写入的 usage 或账单记录，也不会恢复历史漏计数据。
