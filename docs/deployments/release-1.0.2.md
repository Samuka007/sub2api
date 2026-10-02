# release-1.0.2 生产部署记录

## 发布标识

| 项目 | 值 |
| --- | --- |
| GitHub Release 时间 | `2026-07-28 01:16:49`（Asia/Shanghai） |
| 生产部署时间 | `2026-07-28 01:21:15`（Asia/Shanghai） |
| 发布标签 | `release-1.0.2` |
| Tag object | `3bc53acb56e12360dddb795baa0cf1bcbbced1bf` |
| Git 提交 | `bd2ebd1c874e9048470bbb699496da34e32aed57` |
| 功能 PR | [`Alle-Group/sub2api#22`](https://github.com/Alle-Group/sub2api/pull/22)（Plus 401 异常账号一键删除） |
| 流程 PR | [`Alle-Group/sub2api#25`](https://github.com/Alle-Group/sub2api/pull/25)（Pull Request 中文规则） |
| 官方基线 | `v0.1.164` / `cd8bb98c44303b2c8f04c0da340447c992f0cb7d` |
| 程序版本 | `1.0.2` |
| 发布镜像 | `ghcr.io/alle-group/sub2api:1.0.2` |
| 生产固定镜像 | `ghcr.io/alle-group/sub2api@sha256:22863680d90e9ed95348fc31e4dcddb433f33e09e804f9f755094fcdcf0d981f` |
| Registry index digest | `sha256:22863680d90e9ed95348fc31e4dcddb433f33e09e804f9f755094fcdcf0d981f` |
| `linux/amd64` manifest digest | `sha256:4a41a4b5b23c5db5e90e66c7063b6f598a9e3fb7d817b40cfa118694fda27b0a` |
| 生产镜像 ID | `sha256:82d76c02b5e3396092176c50350acae51bf0c815f2d222b43ab920247e1db1b1` |
| 镜像构建时间 | `2026-07-27T17:15:56.01883733Z` |
| 平台 | `linux/amd64` |
| Release workflow | [`30285763324`](https://github.com/Alle-Group/sub2api/actions/runs/30285763324) |
| GitHub Release | [`release-1.0.2`](https://github.com/Alle-Group/sub2api/releases/tag/release-1.0.2) |

## 发布内容

- Plus 配额异常页面仅对处于 `open` 且上游 HTTP 状态为 `401` 的异常账号显示一键删除入口。
- 删除操作在同一事务中锁定父账号与 Spark shadow，删除账号分组和定时测试计划，并软删除父账号及其 Spark shadow。
- Pull Request 标题和正文统一使用中文的仓库规则随本次发布主线一并固化。
- 本次变更没有 Ent Schema、SQL migration、新生产配置项或 Compose 兼容性变化。

删除动作造成的数据变化不能通过镜像回滚恢复，因此生产验收没有对真实账号执行删除，并在部署前创建了数据库全量备份。

## 构建与发布验证

- `release-1.0.2` 是正文非空的 annotated tag，指向已合入 `main` 的精确提交。
- Release workflow 的发布解析、部署契约、前后端安全检查、后端 lint、前端质量检查与构建、后端 unit/integration 测试与构建均通过。
- GoReleaser 成功发布各平台归档、校验文件和 GHCR 多平台镜像；GitHub Release 不是 draft 或 prerelease。
- GHCR index 同时包含 `linux/amd64` 和 `linux/arm64` manifest；生产拉取后的平台、OCI version 和 OCI revision 与发布输入一致。
- Release workflow 已将 `main` 上的 `backend/cmd/server/VERSION` 同步为 `1.0.2`。
- 没有使用或覆盖 `latest`，生产服务器没有拉取源码、安装构建依赖或执行构建。

## 部署前保护

部署前创建 PostgreSQL custom-format 全量备份，并通过 `pg_restore --list` 验证可读：

| 项目 | 值 |
| --- | --- |
| 备份文件 | `/srv/sub2api-prod/backups/sub2api-pre-release-1.0.2-20260727T165729Z.dump` |
| 文件大小 | `1145620821` bytes |
| SHA-256 | `e8a6b4a28affcd7f653af72d36e333aaa816cb4dc0341509e2512773134a747c` |

部署前应用回滚点从实际生产容器读取，不使用旧 `company-v*` 部署记录：

```text
版本：1.0.1
OCI revision：cf8e8ec4a436694dcdd3697d3e78143b62ccae14
固定镜像：ghcr.io/alle-group/sub2api@sha256:308bc3141f11355099e42196dcc76697f0cf927cef338d909729061d6a6bdd2e
生产镜像 ID：sha256:c5761e35f393e0205da2f26af326b329b401c72b98df59c8aefed7974424c15e
```

部署前该容器为 `running`、`healthy`，重启次数为 `0`。

## 部署范围

生产继续使用：

```text
/srv/sub2api-prod/compose.apps.yml
/srv/sub2api-prod/compose.release-1.0.2.yml
```

新覆盖文件权限为 `0644 root:root`，SHA-256 为 `a039b1321eb2439fda9c58f970663c5d077f0fcc263f42b28446e1f92a76f51e`。它沿用上一版本的运行时与模型追踪环境文件，只把 `sub2api` 指向新的固定 registry index digest。

部署命令只重新创建应用容器：

```bash
docker compose \
  -f compose.apps.yml \
  -f compose.release-1.0.2.yml \
  up -d --no-build --no-deps sub2api
```

PostgreSQL、Redis、Caddy、moderation adapter 和 prompt audit adapter 均未重建。

## 首次部署验收结果

- `sub2api` 实际运行新的固定 digest，镜像 ID、`linux/amd64` 平台、OCI version `1.0.2` 和 OCI revision `bd2ebd1c874e9048470bbb699496da34e32aed57` 均匹配。
- 应用容器连续两轮检查均为 `running`、`healthy`，重启次数为 `0`。
- PostgreSQL、Redis 和 Caddy 保持运行；两个审核适配器保持 `healthy`；所有相关容器重启次数均为 `0`。
- `https://sub.scitrace.cc/health` 从生产服务器和外部网络访问均返回 HTTP `200` 与 `{"status":"ok"}`。
- `/setup/status` 返回 HTTP `200`、`needs_setup=false`；`/api/v1/settings/public` 返回 `code=0`、`data.version=1.0.2`。
- 前端首页返回 HTTP `200` 和 HTML；未携带 API Key 请求 `/v1/models` 返回 HTTP `401`。
- 未携带管理员认证请求 `DELETE /api/v1/admin/openai/plus-quota-anomalies/0/account` 返回 HTTP `401`，认证保护位于删除逻辑之前。
- 从新容器启动至验收窗口，删除 outbox 入队失败、`error`、`panic` 和 `fatal` 级日志计数均为 `0`。

## 部署后主机异常重启

- `2026-07-28 01:37:08`（Asia/Shanghai；`2026-07-27 17:37:08 UTC`）生产主机的上一 boot 日志非正常终止，`2026-07-28 01:42:10`（Asia/Shanghai；`2026-07-27 17:42:10 UTC`）进入新的 boot；`last -x` 将上一 boot 标记为 `crash`，且没有正常关机记录。
- 来宾系统现有日志中未发现 OOM、kernel panic、I/O、watchdog 或 Docker 错误证据。主机异常重启的根因无法从来宾系统内确定，也没有证据表明它由 `release-1.0.2` 引起。
- 主机恢复后所有容器均已恢复。`sub2api` 首次恢复启动约 2 秒后以 exit code `1` 退出，随后由 `unless-stopped` 策略自动再次启动；最终复核时为 `running`、`healthy`，`RestartCount=1`、`OOMKilled=false`，固定镜像 digest 和镜像 ID 仍与本次发布一致。
- 恢复后的首轮 30 分钟日志窗口记录到 `prompt_guard_unavailable=442`、`usage billing request fingerprint conflict=281`；后续抽样确认两类问题持续出现，但不是主机异常重启的原因证据，也不归因于 Issue #20 的删除功能。
- Prompt Audit 当前为非阻断模式，只覆盖 1 个指定分组。最近 5 分钟的 `prompt_guard_unavailable` 共 `184` 条，全部来自异步任务的 `prompt_audit.scan_chunk_failed` 和 `prompt_audit.process_failed`（各 `92` 条），该窗口内没有同步阻断或上游未转发事件；数据库中最近 30 分钟有 `663` 个 job 在第 1 次尝试后终态失败。配置的 `http://moderation-adapter:8787/v1/chat/completions` 对无凭据空请求返回 `404`，而 `prompt-audit-adapter` 的同一路由返回 `401`，说明当前端点与扫描器所需协议不匹配。修复需要单独审批生产配置与 token 校验，本次发布未擅自修改。
- 最近 5 分钟的 `usage billing request fingerprint conflict` 共 `205` 条，全部来自 `openai.record_usage_failed`。冲突发生在上游响应已经返回之后，不会改写客户响应，但冲突执行不会扣费或写 usage log，属于实际记账缺口；修复已在独立 PR [`Alle-Group/sub2api#27`](https://github.com/Alle-Group/sub2api/pull/27) 中处理，尚未随本版本部署。
- 两个审核适配器最终均为 `running`、`healthy`、`RestartCount=0`；应用最终为 `running`、`healthy`、`RestartCount=1`、`OOMKilled=false`。容器健康只证明进程和健康接口可用，不代表上述业务链路已经恢复。

## 验收边界

生产环境没有专门可牺牲的 Plus 401 测试账号。本次没有为了烟测制造或删除真实账号，也没有读取、打印或落盘生产账号数据。成功删除、并发互斥、重复删除 `404`、状态变化冲突 `409`、Spark shadow 级联和 outbox 行为由发布工作流中的 unit/integration 测试覆盖。

后续如配置专用测试账号，可补一次已认证的端到端验证；该验证必须使用明确可删除的数据，并在执行前再次确认备份与影响范围。

## 回滚

应用回滚只切换回部署前已验证的固定镜像：

```text
ghcr.io/alle-group/sub2api@sha256:308bc3141f11355099e42196dcc76697f0cf927cef338d909729061d6a6bdd2e
```

在 `/srv/sub2api-prod` 执行：

```bash
docker compose \
  -f compose.apps.yml \
  -f compose.release-1.0.1.yml \
  up -d --no-build --no-deps sub2api
```

不要回滚或重建 PostgreSQL、Redis 数据卷和审核适配器。镜像回滚不会恢复管理员已经通过新功能删除的账号关联数据；如需数据恢复，应使用本次部署前备份并按独立的数据恢复流程处理。
