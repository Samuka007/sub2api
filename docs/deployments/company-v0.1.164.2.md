# company-v0.1.164.2 生产部署记录

## 发布标识

| 项目 | 值 |
| --- | --- |
| 部署时间 | `2026-07-25 18:49:40`（Asia/Shanghai） |
| 内部标签 | `company-v0.1.164.2` |
| Tag object | `2401a711ebde0a0ce3cac66ed0991f37d445ab81` |
| Git 提交 | `2bbf9814653933b2f79cbcccd00c5f4587eed1ba` |
| 功能 PR | [`Alle-Group/sub2api#13`](https://github.com/Alle-Group/sub2api/pull/13)（导出异常账号备注） |
| 官方基线 | `v0.1.164` / `cd8bb98c44303b2c8f04c0da340447c992f0cb7d` |
| 程序版本 | `0.1.164+company.2` |
| Docker 镜像 | `ghcr.io/jonesxq/sub2api:company-v0.1.164.2` |
| Registry index digest | `sha256:f078cf67ce296bb0eba458bbba1621e6f5080cdee4aaf11c44692fed4b98ebe3` |
| `linux/amd64` manifest digest | `sha256:e4615442d813446ed7632d6e795213c324f94903e9381368019205b9614becb3` |
| 镜像构建时间 | `2026-07-25T09:07:53Z` |
| 平台 | `linux/amd64` |

## 发布内容

- Plus 异常账号页面新增一键导出备注功能。
- 只导出当前异常且备注非空的账号；TXT 中每个账号的备注占一行。
- 没有可导出的备注时接口返回空结果，不生成无意义文件。
- 本次变更没有新增数据库迁移，也没有新增生产配置项。

## 构建与发布验证

- 前端通过 `pnpm install --frozen-lockfile`、`pnpm typecheck`、`pnpm lint:check` 和 `pnpm test:run`。
- 后端在 `linux/amd64`、Go `1.26.5` 环境通过 unit/integration 测试；`golangci-lint 2.9.0` 返回 `0 issues`。
- `make VERSION=0.1.164+company.2 build` 通过，功能 PR 的 GitHub CI 通过。
- 镜像在本地构建为 `linux/amd64` 后推送到 GHCR；生产服务器只拉取并运行固定标签，没有拉取源码或执行构建。
- 镜像内程序版本、OCI revision、构建时间、平台和 registry digest 均与本记录一致。
- 没有推送或覆盖 `latest`。

## 部署前保护

部署前已创建 PostgreSQL custom-format 全量备份，并独立通过 `pg_restore --list` 可读性校验：

| 项目 | 值 |
| --- | --- |
| 备份文件 | `/home/ubuntu/sub2api-deploy/backups/sub2api-pre-company-v0.1.164.2-20260725T104404Z.dump` |
| 文件大小 | `762147496` bytes |
| SHA-256 | `a66a11e7f94071a66066d451ade0fa0fc74de74ada309aa788250f82498e491c` |

部署前应用镜像为：

```text
ghcr.io/jonesxq/sub2api:company-v0.1.164.1
sha256:d64b9fbbb1d42b626b1fcf37604c521dc9cb48d558f809449408804790bdbe6e
```

## 部署范围

部署继续使用以下 Compose 文件：

```text
docker-compose.yml
docker-compose.server-image.yml
docker-compose.moderation-adapter.yml
```

只重新创建 `sub2api` 应用容器，没有重建 PostgreSQL、Redis 或审核适配器。审核适配器继续使用：

```text
sub2api-moderation-adapter:company-v0.1.163.2-prompt-guard.1
sub2api-moderation-adapter:responses-v3-chunked
```

## 验证结果

- `sub2api` 实际运行固定镜像 `.2`，OCI revision 为 `2bbf9814653933b2f79cbcccd00c5f4587eed1ba`；状态为 `running`、`healthy`，重启次数为 `0`。
- PostgreSQL、Redis 和两个审核适配器均为 `healthy`，重启次数为 `0`。
- `https://sub.scitrace.cc/health` 返回 HTTP `200` 和 `{"status":"ok"}`。
- `/api/v1/settings/public` 返回 HTTP `200`，公开版本为 `0.1.164+company.2`。
- 前端首页返回 HTTP `200`；未携带 API Key 请求 `/v1/models` 返回 HTTP `401`。
- 从新容器启动至验收窗口没有发现 `ERROR`、`DPANIC`、`PANIC` 或 `FATAL` 日志。
- 导出接口的 TXT 类型、BOM、逐行格式、计数响应头、空结果及错误分支已由后端测试覆盖；前端按钮、文件名、成功/空结果提示已由前端测试覆盖，并在发布前完成本地验证。

## 验收缺口

生产开启 Turnstile，直接调用登录接口会按预期返回 `turnstile verification failed`。发布后自动化浏览器连接不可用，因此未在生产页面完成一次真实验证码登录和下载点击；没有读取、打印或落盘生产账号备注。该缺口不影响容器、公开接口和运行状态验收，后续应在已登录的管理页面补一次人工下载确认。

## 回滚

如发现回归，只将 `sub2api` 应用容器切回以下已验证镜像：

```text
ghcr.io/jonesxq/sub2api:company-v0.1.164.1
sha256:d64b9fbbb1d42b626b1fcf37604c521dc9cb48d558f809449408804790bdbe6e
```

回滚时继续使用三份 Compose 文件并执行 `up -d --no-build sub2api`。不要回滚或重建 PostgreSQL、Redis 数据卷；两个审核适配器继续保持当前固定镜像。
