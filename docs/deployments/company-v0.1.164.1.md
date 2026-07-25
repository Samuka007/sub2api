# company-v0.1.164.1 生产部署记录

## 发布标识

| 项目 | 值 |
| --- | --- |
| 部署时间 | `2026-07-24 20:49:24`（Asia/Shanghai） |
| 内部标签 | `company-v0.1.164.1` |
| Git 提交 | `f3c6895a6f0b64eb6a638e3f89ea9b86a08c7c1d` |
| 官方基线 | `v0.1.164` / `cd8bb98c44303b2c8f04c0da340447c992f0cb7d` |
| 程序版本 | `0.1.164+company.1` |
| Docker 镜像 | `ghcr.io/jonesxq/sub2api:company-v0.1.164.1` |
| 镜像摘要 | `sha256:d64b9fbbb1d42b626b1fcf37604c521dc9cb48d558f809449408804790bdbe6e` |
| 平台 | `linux/amd64` |

## 构建与发布验证

- 前端通过 `pnpm 9.15.9 install --frozen-lockfile`、`pnpm typecheck`、`pnpm lint:check` 和 `pnpm test:run`。
- 后端通过 Go `1.26.5` unit/integration 测试和 `golangci-lint 2.9.0`。
- `make VERSION=0.1.164+company.1 build` 通过。
- GitHub CI、Branch Policy 和 Security Scan 全部通过。
- 镜像在本地构建为 `linux/amd64` 后推送到 GHCR；生产服务器只拉取并运行固定标签，没有拉取源码或执行构建。
- 服务器拉取后的仓库摘要、平台、程序版本和 Git 提交均与发布输入一致。
- GHCR 登录使用临时 Docker 配置；镜像拉取完成后已清除服务器上的临时认证目录。

本次发布经仓库负责人授权采用一次性作者自审例外，治理记录见 [PR #7](https://github.com/Alle-Group/sub2api/pull/7#issuecomment-5069850482)。

## 部署前保护

部署前已创建 PostgreSQL custom-format 全量备份，并通过 `pg_restore --list` 可读性校验：

| 项目 | 值 |
| --- | --- |
| 备份文件 | `/home/ubuntu/sub2api-deploy/backups/sub2api-pre-company-v0.1.164.1-20260724T124638Z.dump` |
| 文件大小 | `372234717` bytes |
| SHA-256 | `01a131c080dc625367ae4b56af6e20d1f114d73f69f2677d3d18ab8da83465ec` |

部署前应用镜像为：

```text
sub2api:company-v0.1.163.2
sha256:feb4b330cbca27bb952ebc4333d158f491eee7bb1f22757a5a0e59bd8d42a886
```

## 部署范围

部署使用以下 Compose 文件：

```text
docker-compose.yml
docker-compose.server-image.yml
docker-compose.moderation-adapter.yml
```

通过 `up -d --no-build sub2api` 只重建 `sub2api` 应用容器。以下服务保持原镜像、原容器和健康状态：

- `sub2api-postgres`
- `sub2api-redis`
- `sub2api-moderation-adapter`
- `sub2api-prompt-audit-adapter`

## 数据库迁移

以下迁移在 `schema_migrations` 中的完整文件名和 checksum 均与发布标签一致：

| 迁移 | SHA-256 | 应用时间（UTC） | 结果 |
| --- | --- | --- | --- |
| `172_composite_model_routes.sql` | `7e591f5fe2b05c5ee5c0b96cc0bcbdcbe32239981ec305033fea413fa4fe08eb` | `2026-07-24T12:49:25.043898Z` | `OK` |
| `186_alipay_mobile_precreate_deep_link.sql` | `7b64b8493a6e2896f798e428e7302b23d1e03ee8b58b31566c67dbfbccbfe96a` | `2026-07-24T12:49:25.098526Z` | `OK` |
| `186_group_auth_cache_image_generation.sql` | `e4c8272402c47adf29583d21c05346d3d6fc00e3b8d5f4ad1184d0dce7ed9ec7` | `2026-07-24T12:49:25.106182Z` | `OK` |

对象级检查确认 composite model routes 表、三个索引、三个检查约束、支付宝设置、分组认证缓存失效函数及其触发器均存在且启用。

## 验证结果

- `sub2api` 运行镜像摘要与 GHCR 摘要一致，状态为 `running` 和 `healthy`，重启次数为 `0`。
- PostgreSQL、Redis 和两个审核适配器均为 `healthy`，重启次数为 `0`。
- `https://sub2.samuka007.com/health` 和 `https://sub.scitrace.cc/health` 均返回 HTTP `200`。
- 两个域名的 `/api/v1/settings/public` 均返回 HTTP `200`，公开版本为 `0.1.164+company.1`。
- 前端首页返回 HTTP `200`；未携带 API Key 请求 `/v1/models` 返回 HTTP `401`。
- 部署后真实模型请求持续返回 HTTP `200`，内容审核和 prompt audit 链路继续工作。
- 从容器启动至最终观察窗口未发现 `ERROR`、`DPANIC`、`PANIC` 或 `FATAL` 日志。

## 回滚

如果新版本发生回归，只将 `sub2api` 应用容器切回以下已验证镜像：

```text
sub2api:company-v0.1.163.2
sha256:feb4b330cbca27bb952ebc4333d158f491eee7bb1f22757a5a0e59bd8d42a886
```

回滚时继续使用三份 Compose 文件和当前审核适配器镜像，并执行 `up -d --no-build sub2api`。不要回滚或重建 PostgreSQL、Redis 数据卷；本次已经应用的数据库迁移保留不动。
