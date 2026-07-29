# sub2api 本地开发指南

本文件只记录当前可执行的环境、生成和验证命令。协作顺序、Issue、分支、评审和 Pull Request 门禁见 [`GIT_WORKFLOW.md`](GIT_WORKFLOW.md)。产品与架构见 [`README.md`](README.md)。

## 1. 技术栈与版本

版本以 [`.github/workflows/code-quality.yml`](.github/workflows/code-quality.yml) 为准：

| 工具 | CI 版本 | 用途 |
| --- | --- | --- |
| Go | `1.26.5` | 后端构建与测试 |
| golangci-lint | `2.9` | Go 静态检查 |
| govulncheck | `1.6.0` | Go 依赖与可达漏洞检查 |
| Node.js | `24` | 前端构建与测试 |
| pnpm | `9.15.9` | 前端包管理 |
| Python | `3` | 仓库治理和审计脚本 |
| Docker Compose | 当前 Docker CLI 支持版本 | 部署契约与本地服务 |

后端使用 Go、Gin、Ent、PostgreSQL 和 Redis；前端使用 Vue 3、TypeScript、Vite、Vitest 和 pnpm。不得用 npm/yarn 改写 `frontend/pnpm-lock.yaml`。

版本检查：

```bash
go env GOVERSION
golangci-lint version
node --version
pnpm --version
docker compose version
python3 --version
```

## 2. 初始准备

```bash
git fetch origin main
pnpm --dir frontend install --frozen-lockfile
cd backend
go mod download
```

运行服务所需配置从仓库内 `.env.example` 或部署模板复制到被忽略的本地文件，再由环境变量注入敏感值。禁止把任何真实或本地测试凭据写进源码、文档、Issue、PR 或日志。

## 3. 聚焦开发命令

### 后端

```bash
make -C backend test-unit
make -C backend test-integration
make -C backend build
(cd backend && golangci-lint run ./...)
```

只运行目标包：

```bash
cd backend
go test -tags=unit ./internal/<package>/...
go test -tags=integration ./internal/<package>/...
```

### 前端

```bash
pnpm --dir frontend run lint:check
pnpm --dir frontend run typecheck
pnpm --dir frontend run test:run
pnpm --dir frontend run build
```

只运行目标测试：

```bash
pnpm --dir frontend exec vitest run <test-file-or-pattern>
```

### 代码生成

修改 Ent Schema：

```bash
cd backend
go generate ./ent
go generate ./cmd/server
```

修改 Wire Provider：

```bash
cd backend
go generate ./cmd/server
```

生成后检查并提交对应生成文件；不得手改生成代码掩盖源定义缺失。

## 4. Pull Request 完整门禁

创建 Pull Request 前从仓库根目录运行：

```bash
make pr-check
```

该命令执行：

1. 仓库治理、禁止过程文档和评审 Skill 完整性检查。
2. 部署脚本语法、Apple container、Model IQ Compose、安装 token 和模型追踪 Compose 契约测试。
3. 后端 unit、integration、build、golangci-lint、govulncheck。
4. 前端 frozen install、lint、typecheck、完整 Vitest、production build、生产依赖审计。

任何子命令失败都使门禁失败。修复后必须重新运行完整命令；不得用较窄测试代替最终门禁。

`make pr-check` 覆盖当前 CI 的本地可执行部分，但不证明真实外部服务或用户路径。行为变更还必须运行对应 smoke，并在 PR 中区分：

- 自动测试通过。
- 真实 smoke 通过。
- 被 skip 或因依赖缺失未验证的路径。

### 模型追踪专项 smoke

修改 `backend/internal/modeltrace/`、追踪配置、模型网关传输或 `.agent/skills/sub2api-model-trace-e2e/` 时，读取该 Skill 并运行适用命令：

```bash
.agent/skills/sub2api-model-trace-e2e/scripts/run_e2e.sh
```

需要完整 Langfuse 路径时使用 Skill 定义的 full smoke。必须验证真实 trace/generation/session 结果，不能只验证容器启动或 OTLP 请求成功。

## 5. 评审 Skill

仓库 Skill 的唯一真源是 [`.agent/skills/`](.agent/skills/)；Claude Code 和 Codex 分别通过 `.claude/skills`、`.codex/skills` 软链接自动发现同一份内容。仓库内置 [`.agent/skills/reviewing-code-changes/`](.agent/skills/reviewing-code-changes/)。准备本地全部差异：

```bash
python3 .agent/skills/reviewing-code-changes/scripts/prepare_review_context.py \
  --mode all --cwd . --out /tmp/sub2api-review-context.json
```

准备最终分支与主线差异：

```bash
python3 .agent/skills/reviewing-code-changes/scripts/prepare_review_context.py \
  --mode range --base origin/main --head HEAD --cwd . \
  --out /tmp/sub2api-review-context.json
```

随后按 `SKILL.md` 完成只读评审、finding 校验、误报复核和报告。主控修复已采纳的 P0/P1，重跑相关测试和定向复审。评审 JSON 写入 `/tmp` 或被忽略的 `code-reviews/`，不得提交。

## 6. 常见失败

### `package.json` 与 lockfile 不一致

运行 `pnpm --dir frontend install` 更新 lockfile，核对依赖变更后同时提交 `frontend/package.json` 与 `frontend/pnpm-lock.yaml`。最终仍使用 `--frozen-lockfile` 验证。

### Interface 改动导致 stub/mock 编译失败

使用 LSP references 查找所有实现和调用方，更新真实实现与测试替身；不得给接口增加无行为意义的默认实现绕过编译。

### Ent 或 Wire 改动未生效

运行第 3 节代码生成命令，确认源定义和生成文件同时变化。CI build 通过不代表运行时依赖图符合预期，仍需目标路径 smoke。

### E2E 被 skip

`go test` 退出码为 0 但输出包含 `SKIP` 时，只能声明测试命令完成，不能声明该端到端路径已验证。提供服务、配置和脱敏测试凭据后重跑，或明确阻塞 PR。

### 本地工具版本与 CI 不一致

先对齐第 1 节版本。禁止修改 workflow、放宽版本断言或关闭 lint 来适配个人环境。

## 7. 权威入口

- 协作与发布：[`GIT_WORKFLOW.md`](GIT_WORKFLOW.md)
- Agent 强制规则：[`AGENTS.md`](AGENTS.md)
- 产品与架构：[`README.md`](README.md)
- 文档分类：[`docs/README.md`](docs/README.md)
- CI 真源：[`.github/workflows/code-quality.yml`](.github/workflows/code-quality.yml)
