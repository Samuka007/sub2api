# 4Sub2 Git 与 Agent 协作规范

本文档是 `Alle-Group/sub2api` 的唯一详细协作流程。Issue、分支名、assignee 和 PR 标题用于建议性协作追踪；CI 只承担可执行测试、构建、安全、部署与 release 契约门禁，不校验这些协作元数据。

## 1. 协作原则与安全边界

1. `main` 始终是已通过可执行质量门禁、可发布的内部稳定主线。
2. 建议通过 Issue、独立分支和 Pull Request 追踪仓库变更，但这些元数据不构成 CI 门禁。
3. 禁止直接修改、提交或推送 `main`、`vendor/main`。
4. 建议一个 Issue、一个分支、一个 Pull Request 只解决一个问题域；同一会话中属于当前问题域和验收目标的追加改动应复用当前 Issue、分支和 Pull Request，只有可独立交付、风险边界不同或脱离当前目标的问题才新建 Issue。
5. Agent 必须在独立 `git worktree` 中工作以保护用户工作区；人工开发建议使用独立分支。
6. 测试、运行 smoke 和 AI 评审是不同证据，任何一项不能替代另一项。
7. 服务器只部署明确的内部 annotated tag 和固定镜像，不部署浮动 `main` 或 `latest`。

## 2. 远程仓库与长期分支

| 名称 | 地址 | 用途 |
| --- | --- | --- |
| `origin` | `git@github.com:Alle-Group/sub2api.git` | 团队仓库，可按授权推送 |
| `upstream` | `git@github.com:Wei-Shaw/sub2api.git` | 官方仓库，只读 |

| 分支 | 生命周期 | 说明 |
| --- | --- | --- |
| `vendor/main` | 长期 | 官方 `upstream/main` 的纯镜像，禁止内部提交 |
| `main` | 长期 | 内部稳定主线，只能通过 Pull Request 合入 |
| `feature/<slug>` | 临时 | 用户可见功能 |
| `fix/<slug>` | 临时 | 普通缺陷修复 |
| `hotfix/<slug>` | 临时 | 基于最新 `origin/main` 的紧急生产修复；合并后发布独立修复 tag |
| `sync/v<tag>` | 临时 | 上游版本同步 |
| `docs/<slug>` | 临时 | 纯长期文档变更 |
| `chore/<slug>` | 临时 | 工程、依赖或维护修改 |
| `refactor/<slug>` | 临时 | 不改变外部行为的重构 |
| `test/<slug>` | 临时 | 独立测试或测试基础设施修改 |
| `otel/<slug>` | 临时 | OpenTelemetry 追踪功能 |

`dev_xq`、`dev_sh` 只作为历史归档，不接收新提交。禁止对长期分支和已共享分支强制推送。

## 3. 标准变更流程

### 3.1 建议建立并认领 Issue

先搜索是否已有同一问题：

```bash
gh issue list --repo Alle-Group/sub2api --state open --limit 100
gh issue view <number> --repo Alle-Group/sub2api
```

复用现有 Issue 时，建议认领并补全正文：

```bash
gh issue edit <number> --repo Alle-Group/sub2api --add-assignee @me
```

没有对应 Issue 且需要长期追踪时，可创建并将自己设为 assignee：

```bash
gh issue create --repo Alle-Group/sub2api --assignee @me \
  --title '<中文标题>' --body-file /tmp/sub2api-issue.md
```

Issue 正文建议包含：

- 问题与用户/系统影响。
- 目标和明确非目标。
- 计划方案及关键取舍。
- 可判定的验收标准。
- 上游、下游、数据、配置、安全、兼容性和回滚影响。

Issue 是协作建议而非开工或 CI 门禁；使用 Issue 时应在方案或范围变化后及时更新，避免记录失真。

### 3.2 建议从最新主线创建隔离工作区

Agent 的标准路径：

```bash
git fetch origin main
git worktree add -b <scope>/<short-slug> ../sub2api-<short-slug> origin/main
cd ../sub2api-<short-slug>
```

创建后确认：

```bash
git branch --show-current
git merge-base --is-ancestor origin/main HEAD
git status --porcelain=v1 -b
```

如果目标分支或 worktree 已存在，先确认其归属和状态；不得删除、复用、stash 或覆盖其他人的工作。人工开发者可在原 clone 中 `git switch -c`，但分支仍必须基于最新 `origin/main`。

### 3.3 实现与聚焦验证

1. 阅读 Issue、现有代码、测试、调用方和上下游契约。
2. 先闭合最小用户可见行为或缺陷复现，再实现最小正确修改。
3. 每个新行为补足能防止真实回归的测试；跨模块改动核对接口、失败、幂等、事务、并发和权限边界。
4. 立即运行与当前行为对应的聚焦测试和真实 smoke；失败先修根因，不关闭检查或放宽断言。
5. 不夹带无关重构、依赖升级、生成文件、工作笔记或另一个 Issue 的内容。

修改 Ent Schema 或 Wire Provider 时：

```bash
make -C backend generate
```

只修改 Wire Provider 时可以只运行 `go generate ./cmd/server`。修改 `frontend/package.json` 时必须同步更新 `frontend/pnpm-lock.yaml`。

### 3.4 同步长期文档

在功能和 smoke 已经可用后，再处理长期文档：

- 重大用户可见功能、长期架构、配置或协作流程变化：更新 `README.md` 的对应摘要和权威入口。
- API、部署、法律、发布或稳定运维事实：更新 `docs/` 中对应文档。
- 本地环境和验证命令变化：更新 `DEV_GUIDE.md`。
- Agent 强制行为变化：更新 `AGENTS.md` 和本流程。

禁止提交思考过程和过程性资产，包括 `docs/superpowers/`、`openspec/changes/`、临时 plan/spec、对话记录、事实包、review JSON 和任务草稿。具体分类见 [`docs/README.md`](docs/README.md)。

### 3.5 运行完整 PR 质量门禁

统一命令：

```bash
make pr-check
```

该命令必须真实完成当前 GitHub `Code Quality` 的本地可执行部分：

- 仓库治理和工作文档边界检查。
- 部署脚本语法与部署契约测试。
- 后端 unit、integration、build、golangci-lint 和 govulncheck。
- 前端 frozen install、lint、typecheck、完整 Vitest、production build 和依赖审计。

功能行为还必须运行对应的真实用户路径 smoke。通用测试发生 skip 不得表述为端到端验证。涉及 `backend/internal/modeltrace/` 时，按 `.agent/skills/sub2api-model-trace-e2e/` 运行对应 smoke 并记录 Langfuse/OTLP 证据；无法提供依赖时明确标记未验证，不能创建 PR。

任何失败都阻塞后续评审和 PR。修复后重跑失败项，最后重新运行 `make pr-check`，不得只贴历史成功输出。

### 3.6 按风险执行独立 AI 评审

仅修改 Markdown 等文档，且不涉及代码、配置、CI、依赖、生成物、运行行为或安全、权限、数据、数据库、发布边界的简单改动，可跳过本节 AI 评审；PR 中必须记录 skip 原因。其他改动按风险执行以下流程。

仓库内评审 Skill：

```text
.agent/skills/reviewing-code-changes/
```

未提交改动使用 `all` 事实包：

```bash
python3 .agent/skills/reviewing-code-changes/scripts/prepare_review_context.py \
  --mode all --cwd . --out /tmp/sub2api-review-context.json
```


需要评审时，严格按 `.agent/skills/reviewing-code-changes/SKILL.md` 执行一次独立评审：

1. Reviewer 只读，先审测试证明力，再审实现和上下游风险。
2. 主控逐条复核 finding；无路径、行号、证据或风险链条的发现不得采纳。
3. 主控自动修复已采纳的 P0/P1，并重跑相关聚焦测试。
4. 对修复执行 Skill 定义的定向复审；出现新的 P0/P1 时升级为新的全量评审，不在定向范围中顺手处理。
5. 直到没有已采纳的 P0/P1、`make pr-check` 再次通过，才算评审门禁通过。
6. P2 必须在 PR 中记录“已修复”或“不修及原因”，不得静默忽略。

评审事实包和中间 JSON 只能写入 `/tmp` 或被忽略的 `code-reviews/`，不得提交。

### 3.7 提交、推送与创建 Pull Request

Agent 只有在用户明确授权对应动作后，才能 commit、push 或创建 PR。授权创建 PR 时，仍须先展示变更范围、完整测试证据和评审结论。

提交信息：

```text
feat(scope): 中文说明
fix(scope): 中文说明
test(scope): 中文说明
docs(scope): 中文说明
chore(scope): 中文说明
```

获得 commit 授权后，先提交当前已验证改动，再同步最终主线：

```bash
git fetch origin main
git rebase origin/main
```

rebase 完成后，必须基于最终树重新运行完整门禁并评审最终分支差异：

```bash
make pr-check
python3 .agent/skills/reviewing-code-changes/scripts/prepare_review_context.py \
  --mode range --base origin/main --head HEAD --cwd . \
  --out /tmp/sub2api-review-context.json
```

随后再次按 `.agent/skills/reviewing-code-changes/SKILL.md` 执行完整独立评审。rebase、冲突解决或评审修复只要改变最终树，就必须重新运行相关聚焦测试、`make pr-check` 和最终 diff 评审；不得复用同步前的证据。

只有自己的未合并临时分支可以在 rebase 后使用 `git push --force-with-lease`；禁止 `git push --force`。首次推送使用 `git push -u origin <branch>`。

Pull Request 标题建议遵循 Conventional Commits 形式 `type(scope): 中文描述`（scope 可选，`!` 表示破坏性变更），常用 type：`feat fix hotfix sync docs style refactor perf test build ci chore revert otel`。CI 不校验标题、Issue 状态、assignee 或分支命名。正文建议使用中文，并包含：

- 关联 Issue（如有，例如 `Closes #<issue>`）。
- 问题、方案、范围和非目标。
- 测试命令、退出结果、真实 smoke 证据和 skip/未验证项。
- `reviewing-code-changes` 结论、已修复 finding 和剩余 P2 决策。
- 数据库、配置、安全、兼容性、发布与回滚影响。
- README/长期文档是否更新及原因。

#### PR 正文标准模板

为统一正文结构，在上述要素基础上使用以下章节模板（中文填写，无内容的节注明"无"并说明原因）：

````markdown
## 关联 Issue
Closes #<编号>

## 背景 / 问题
<!-- 为什么要改；当前问题或需求 -->

## 改动
<!-- 做了什么；跨模块改动说明真实调用方与上下游契约 -->
-

## 修复 / 解决
<!-- 可观察变化；feat 与 fix 分点 -->

## 范围与非目标
- 包含：
- 不包含：（无关改动另开 PR）

## 测试证据
- [ ] `make pr-check` 通过
- [ ] 用户可见/错误路径真实 smoke（场景）
- [ ] 模型追踪改动跑 `.agent/skills/sub2api-model-trace-e2e/` smoke
<!-- 如实区分：自动测试通过 / 真实 smoke 通过 / skip 未验证 -->

## 评审
- [ ] 已运行 `reviewing-code-changes`
- [ ] P0/P1 已修复并定向重测

## 影响与风险
<!-- 用户可见行为、接口契约、数据、权限、安全、性能 -->

## 回滚
<!-- revert PR / feature flag / 配置回退 / 数据回滚预案 -->

---
### 提交前自检
- [ ] 分支已同步 `origin/main`，变更内聚可解释
- [ ] commit 作者/提交者身份真实（关联 GitHub 账号）
- [ ] 未提交凭证、客户数据、生产配置、本地路径、过程文档
- [ ] README/长期文档已按 AGENTS 第5节更新或确认无需
````

建议一个 PR 只解决一个问题域。合并方式按提交数和类型选择：单 commit 的 PR 用 **squash** 或 **rebase** 合并（避免产生 `Merge pull request` 噪音 commit）；仅同步 PR（见第4节）用 **merge commit** 以保留官方祖先关系；其余多 commit PR 用 **squash**。合并前必须满足：

- `Code Quality` 和全部 required checks 通过。
- PR 作者可在其余合并条件满足后自行合并。
- 所有 review conversation 已解决。
- 分支仍可干净合入最新 `main`。

合并后删除临时分支和对应 worktree；不得删除仍有未提交改动的 worktree。

## 4. 上游同步

`.upstream-version` 记录当前吸收的官方 tag 和 commit。每次同步使用独立 `sync/<tag>` Issue、分支和 Pull Request：

```bash
git fetch origin main
git fetch upstream --tags --prune
git worktree add -b sync/vX.Y.Z ../sub2api-sync-vX.Y.Z origin/main
cd ../sub2api-sync-vX.Y.Z
git merge --no-ff vX.Y.Z -m 'chore(upstream): merge Sub2API vX.Y.Z'
```

同步要求：

1. 更新 `.upstream-version` 为官方 tag 和完整 commit。
2. 先读官方 release note 和冲突双方意图；优先保留官方结构，再重新接入内部行为。
3. 禁止整目录使用 `ours`/`theirs`，禁止 `-s ours`、`--allow-unrelated-histories` 或整仓覆盖。
4. 每个有业务含义的冲突都要有回归测试和 PR 说明。
5. 完成 `make pr-check`、私有关键路径 smoke 和独立评审。
6. 同步 PR 使用 merge commit，保留官方祖先关系；不得在其中开发新功能。

`vendor/main` 只能由维护者从 `upstream/main` 快进：

```bash
git fetch upstream
git push origin upstream/main:refs/heads/vendor/main
```

## 5. 预发布、生产发布与回滚

候选和生产 tag 都必须无前导零；候选序号从 1 开始：

```text
pre-release-MAJOR.MINOR.PATCH-rc.N
release-MAJOR.MINOR.PATCH
```

发布顺序：

1. PR 合入 `main`，确认同一 `origin/main` commit 的独立 `Code Quality` push run 全部通过。
2. 在该 commit 创建带非空正文的 annotated `pre-release-*` tag 并推送。`Pre-release` workflow 对固定 SHA 再跑完整质量门禁，只发布 linux/amd64 GHCR 候选镜像和 GitHub Pre-release，不更新 `VERSION`、不生成生产 Release。
3. 测试服务器必须拉取 Pre-release 页面记录的精确 `image@sha256:digest`，不得使用浮动 tag。使用 `standard` 模式和可计费测试账号执行真实上游 smoke；至少覆盖 Responses JSON、SSE、`/responses/compact`、WebSocket 多 turn，同一 `X-Client-Request-ID` 下逐 turn 核对 upstream response ID、`usage_logs.request_id`、`usage_billing_dedup`、余额或套餐用量变化，并覆盖上游失败、重试、并发和持续稳定性。
4. 在关联 Issue 或 PR 记录候选 tag、commit、镜像 digest、测试窗口、请求与计费核对结果、稳定性结果、失败项和回滚演练。任一计费不闭合、测试被 skip、证据缺失或测试服务器不是候选 digest 时，不得创建生产 tag；修复后必须从新 commit 创建新的 `rc.N`。
5. 验收通过后，只能在与已发布 Pre-release 相同的 commit 创建 annotated `release-*` tag。Release workflow 会验证同版本 GitHub Pre-release 存在、tag 为 annotated 且指向同一 commit，然后再次执行质量门禁并生成生产 Release、归档和固定版本镜像。
6. 部署生产前记录当前镜像和 digest；部署新镜像后等待健康检查并执行关键用户路径 smoke，记录 Git tag、commit、镜像和 digest。失败时切回部署前固定镜像。

生产和测试服务器都不得使用 `latest`。数据库和 Redis 数据卷不随镜像回滚；涉及不可逆迁移时必须在预发布前准备独立数据恢复方案。Pre-release 镜像和 GitHub Pre-release 是不可变证据；失败的候选不得覆盖或复用 tag，必须递增 `rc.N`。

## 6. GitHub 仓库保护

`main` 建议配置：

- 只能通过 Pull Request 合并。
- required checks 只包含可执行测试、构建、安全和部署/release 契约，不包含分支名、Issue 关联或归属、PR 标题等协作策略。
- 所有 review conversation 已解决；不强制非作者审批，PR 作者在其余条件满足后可以自行合并。
- 禁止 force push 和删除，管理员同样遵守。

文件内规则和 PR 模板不能替代 GitHub branch protection；仓库管理员应定期核对实际 ruleset 与本节一致。

## 7. 安全与清洁度

- 禁止提交 SSH 私钥、API Token、Cookie、数据库密码、JWT Secret、TOTP 密钥、生产 `.env`、客户数据或本地测试凭据。
- 禁止把本地 endpoint、个人绝对路径、工作区快照和生成缓存写进生产代码或长期文档。
- PR 前检查 `git status --porcelain`、待提交文件清单和构建上下文；未知文件先查来源，不得自动删除或提交。
- 生产配置和备份不通过 Git 分发。
