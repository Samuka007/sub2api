# 4Sub2 长期记忆

更新时间：`2026-07-28`

本文件只保存跨任务仍然有价值、且能从仓库或生产证据核验的事实。临时需求、方案原因和详细功能设计应分别维护在仓库对应的任务、决策或专题文档中。

## 当前状态

| 项目 | 当前事实 | 来源 |
| --- | --- | --- |
| 官方基线 | `v0.1.164` / `cd8bb98c44303b2c8f04c0da340447c992f0cb7d` | [`.upstream-version`](../.upstream-version) |
| 最近内部发布 | `release-1.0.4` | [`deployments/release-1.0.4.md`](deployments/release-1.0.4.md) |
| 发布 commit | `d4edac4efe6c30c3e8304b53ec74c8c1bb836a07` | 同上 |
| 生产固定镜像 | `ghcr.io/alle-group/sub2api@sha256:082232bab72f5d662a6a3f1524fa823b338fab54369a8284bf6525ce805a4179` | 同上 |
| 程序版本 | `1.0.4` | 同上 |
| 目标平台 | `linux/amd64` | 同上 |

`main` 在最近发布 tag 之后可以继续包含 VERSION 同步、文档或后续开发提交，因此不要把 `HEAD` 自动视为当前生产版本。

## 源码历史边界

当前内部源码由官方基线、历史冻结材料和已核验的内部补丁恢复并继续维护。它是可构建、可测试、可协作开发的功能版本，但不能声称与已经丢失的原始生产提交逐字节一致。详细证据见 [`../RECOVERY.md`](../RECOVERY.md)。

## 稳定技术事实

- 后端是使用 Gin、Wire、Ent、PostgreSQL 和 Redis 的 Go 服务。
- 前端是 Vue 3、TypeScript、Vite、Pinia 和 Vue I18n 应用。
- 生产 Go 二进制嵌入已经构建好的前端资源。
- Prompt Audit / Guard 位于独立的 `backend/internal/securityaudit` 模块。
- `origin` 是团队可写仓库，`upstream` 的 push URL 被设置为 `no_push`。

## 生产与发布事实

- 生产 SSH 入口为 `ssh root@38.244.20.220 -p 31589`；私钥不进入仓库、镜像或 CI Secret。
- 生产应用 Compose 项目位于 `/srv/sub2api-prod`；基础文件为 `compose.apps.yml`，每次发布使用独立的 `compose.release-<version>.yml` 固定镜像覆盖文件。
- 发布标签使用正文非空的 annotated tag：`release-MAJOR.MINOR.PATCH`。
- Release workflow 负责质量门禁、二进制归档、GHCR 镜像、GitHub Release 和主线 VERSION 同步。
- 编译、测试和镜像构建在 GitHub Actions、本地电脑或专用构建机完成。
- 生产服务器只拉取并运行固定 digest，执行 Compose `up -d --no-build`，不拉源码、不安装构建依赖。
- 发布前记录实际运行镜像并准备数据恢复；发布记录必须包含 Git tag、commit、镜像、registry index digest 和目标平台 manifest digest。
- 回滚只切换到部署前实际验证的固定镜像，不回滚 PostgreSQL 或 Redis 数据卷。
- `2026-07-28`，生产主机在 `release-1.0.2` 部署后发生一次原因未明的异常重启；容器已经恢复，来宾系统内的取证边界和最终状态见 [`deployments/release-1.0.2.md`](deployments/release-1.0.2.md)。

## 维护规则

出现以下事件时更新本文件：

- 吸收新的官方 tag：更新官方基线和日期。
- 完成新的内部发布：更新发布 tag、commit、镜像、版本和记录链接。
- 生产服务器、Compose 路径、目标架构或发布边界发生长期变化。
- 源码恢复结论或关键模块边界发生变化。

不要记录猜测、一次性排障过程、个人偏好或未确认计划。
