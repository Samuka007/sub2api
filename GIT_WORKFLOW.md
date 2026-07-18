# 4Sub2 Git 协作与上游同步规范

本文档是 `Jonesxq/4Sub2` 的 Git 工作约定。目标是在两人协作开发的同时，持续、可审计地吸收 `Wei-Shaw/sub2api` 的官方更新，并保证生产环境可以明确定位和回滚。

## 1. 核心原则

1. `main` 始终是已经审核、测试通过、可以发布的内部稳定主线。
2. 服务器只部署明确的内部 Git tag 和固定 Docker 镜像，不部署浮动的 `main` 或 `latest`。
3. 每项内部需求使用独立分支，不再使用长期个人开发分支。
4. 每次上游升级使用独立的 `sync/<tag>` 分支和 Pull Request。
5. 上游同步、内部功能和生产修复不能混在同一个 Pull Request 中。
6. 禁止对 `main`、`vendor/main` 和已经共享的分支强制推送。

## 2. 远程仓库

每位开发者使用两个远程：

| 名称 | 地址 | 用途 |
| --- | --- | --- |
| `origin` | `git@github.com:Jonesxq/4Sub2.git` | 团队私有仓库，可推送 |
| `upstream` | `git@github.com:Wei-Shaw/sub2api.git` | Sub2API 官方仓库，只读 |

首次配置：

```bash
git clone git@github.com:Jonesxq/4Sub2.git
cd 4Sub2
git remote add upstream git@github.com:Wei-Shaw/sub2api.git
git remote set-url --push upstream no_push
git fetch upstream --tags
```

`upstream` 的 push 地址故意设置为无效值，避免误推送到官方仓库。

## 3. 分支结构

| 分支 | 生命周期 | 说明 |
| --- | --- | --- |
| `vendor/main` | 长期 | 官方 `upstream/main` 的纯镜像，禁止内部提交 |
| `main` | 长期 | 内部稳定主线，只能通过 Pull Request 合入 |
| `feature/<说明>` | 临时 | 内部功能，例如 `feature/model-price-export` |
| `fix/<说明>` | 临时 | 普通问题修复，例如 `fix/quota-refresh-timeout` |
| `hotfix/<说明>` | 临时 | 生产紧急修复，从当前生产 tag 创建 |
| `sync/<官方tag>` | 临时 | 上游升级，例如 `sync/v0.1.162` |
| `docs/<说明>` | 临时 | 纯文档修改 |
| `chore/<说明>` | 临时 | 工程、依赖和维护修改 |

`dev_xq` 和 `dev_sh` 是迁移前的个人分支。新结构启用后应冻结，只保留归档，不再接收新提交。

## 4. 日常功能开发

开始需求前，从最新 `main` 创建分支：

```bash
git fetch origin
git switch main
git pull --ff-only origin main
git switch -c feature/short-description
```

开发完成后：

```bash
git push -u origin feature/short-description
```

然后创建 `feature/short-description -> main` 的 Pull Request。要求：

- 一个 Pull Request 只解决一个问题。
- 至少由另一位开发者审核。
- CI 全部通过，所有讨论已解决。
- 功能 Pull Request 使用 squash merge。
- 合并后删除临时分支。

如果开发期间 `main` 已更新，提交审核前执行：

```bash
git fetch origin
git rebase origin/main
git push --force-with-lease
```

只能对自己尚未合并的临时分支使用 `--force-with-lease`，不得用于共享分支。

## 5. 同步 Sub2API 官方版本

`.upstream-version` 记录当前已经吸收的官方 tag 和 commit。上游升级必须更新该文件。

以升级到 `v0.1.162` 为例：

```bash
git fetch origin
git fetch upstream --tags --prune
git switch main
git pull --ff-only origin main
git switch -c sync/v0.1.162
git merge --no-ff v0.1.162 -m "chore(upstream): merge Sub2API v0.1.162"
```

合并完成后，把 `.upstream-version` 更新为官方 tag 和完整 commit：

```text
UPSTREAM_TAG=v0.1.162
UPSTREAM_COMMIT=<v0.1.162 对应的完整提交 SHA>
```

然后执行完整测试，推送并创建 `sync/v0.1.162 -> main` Pull Request：

```bash
git push -u origin sync/v0.1.162
```

同步 Pull Request 必须使用 merge commit，不能 squash。这样 Git 才能保留官方祖先关系，下一次升级可以基于正确的共同祖先计算差异。

`v0.1.161` 是仓库从历史快照方式迁移到正式上游祖先关系的唯一特殊版本。后续同步不得使用 `-s ours`、`--allow-unrelated-histories` 或整仓覆盖。

### 冲突处理规则

1. 先阅读官方 release note 和冲突文件的上游修改意图。
2. 优先保留官方结构，再重新接入内部行为。
3. 禁止对整个目录使用 `ours` 或 `theirs` 批量覆盖。
4. 每个有业务含义的冲突都要补充或执行回归测试。
5. 冲突解决和兼容性调整必须在同步 Pull Request 中说明。
6. 不要在同步 Pull Request 中顺便开发新功能。

`vendor/main` 由维护者更新，并且只能快进：

```bash
git fetch upstream
git push origin upstream/main:refs/heads/vendor/main
```

## 6. 检查要求

前端：

```bash
cd frontend
pnpm install --frozen-lockfile
pnpm typecheck
pnpm lint:check
pnpm test:run
```

后端：

```bash
cd backend
go test -tags=unit ./...
go test -tags=integration ./...
golangci-lint run ./...
```

修改 Ent Schema 后必须重新生成并提交生成文件：

```bash
cd backend
go generate ./ent
go generate ./cmd/server
```

修改 `package.json` 时必须同步提交 `pnpm-lock.yaml`。修改 Wire Provider 时必须重新生成并提交 `backend/cmd/server/wire_gen.go`。

## 7. 提交和 Pull Request

提交信息采用以下格式：

```text
feat(scope): 功能说明
fix(scope): 修复说明
test(scope): 测试说明
docs(scope): 文档说明
chore(upstream): merge Sub2API v0.1.162
```

Pull Request 必须填写：

- 改动目的和范围。
- 测试证据。
- 数据库、配置和兼容性影响。
- 上游同步信息（如适用）。
- 发布方案和回滚方案。

## 8. 发布与服务器部署

内部 tag 格式：

```text
company-v<官方版本>.<内部修订号>
```

例如：

```text
company-v0.1.161.1
company-v0.1.161.2
company-v0.1.162.1
```

发布步骤：

1. 同步或功能 Pull Request 合入 `main`。
2. 在 `main` 的待发布提交上创建内部 tag。
3. 使用该 tag 构建不可变 Docker 镜像。
4. 先记录当前生产镜像，再部署新镜像。
5. 等待容器健康检查，并执行关键接口冒烟测试。
6. 部署成功后记录 Git tag、commit、镜像名称和镜像摘要。

创建 tag：

```bash
git switch main
git pull --ff-only origin main
git tag -a company-v0.1.161.1 -m "4Sub2 release based on Sub2API v0.1.161"
git push origin company-v0.1.161.1
```

生产环境不得使用 `latest`。部署命令必须指定类似 `sub2api:company-v0.1.161.1` 的固定镜像。

回滚时只切回部署前记录的镜像，不回滚 PostgreSQL 或 Redis 数据卷。涉及不可逆数据库迁移的版本必须在发布前准备独立的数据恢复方案。

## 9. GitHub 仓库设置

`main` 建议启用以下保护：

- 必须通过 Pull Request 合并。
- 至少 1 个审批。
- 必须通过分支规范、后端、前端和安全检查。
- 必须解决全部 review conversation。
- 禁止 force push 和删除。
- 管理员同样遵守保护规则。

`vendor/main` 禁止 Pull Request 以外的人工修改，只允许维护者执行官方镜像快进。

## 10. 安全规则

- 禁止提交 SSH 私钥、API Token、数据库密码、JWT Secret、TOTP 密钥和生产 `.env`。
- 生产服务器私钥只用于 SSH，不复制到仓库、镜像或 CI Secret。
- 部署前检查 `git diff` 和镜像构建上下文，避免把本地配置打进镜像。
- 生产配置和数据备份不通过 Git 分发。
