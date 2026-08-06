# Sub2API 项目级 Agent 规约

本文件定义所有 Agent 必须遵守的仓库原则、安全边界和协作建议。详细命令以 [`GIT_WORKFLOW.md`](GIT_WORKFLOW.md) 与 [`DEV_GUIDE.md`](DEV_GUIDE.md) 为准；产品与架构事实以 [`README.md`](README.md) 为准。

## 1. 规则优先级

1. 系统与用户明确指令。
2. 本文件的安全、授权和协作门禁。
3. `GIT_WORKFLOW.md` 的执行流程。
4. `DEV_GUIDE.md`、代码、测试和具体 Skill 的领域规则。

发现文档与代码、CI 或运行结果冲突时，以当前代码、CI 和新鲜运行证据为准，并在本次分支中修正文档；不得自行绕过门禁。

## 2. Issue 与变更追踪建议

建议为代码、配置、CI、依赖、仓库行为或长期文档变更关联 GitHub Issue；Issue、assignee、分支命名和 PR 标题用于协作与追踪，不作为 CI 阻塞门禁。

- 优先复用现有 Issue；需要明确执行者时可使用 `gh issue edit <number> --add-assignee @me`。
- 建议在开工前补齐问题、目标、非目标、方案、验收标准、上下游影响和风险，避免一句标题或占位正文。
- 同一会话中，先判断新增改动是否属于当前 Issue 的同一问题域和验收目标；同类或当前目标所需的治理调整应复用现有 Issue、分支和 Pull Request。只有可独立交付、风险边界不同或脱离当前验收目标的问题才新建 Issue，避免把无关改动塞入当前分支。
- 本仓库使用 GitHub 与 `gh` CLI。禁止路由到 AntCode 或 `antcode-skill`。

## 3. 分支与 worktree

- 禁止直接修改、提交或推送 `main`、`vendor/main`。
- 建议每项变更从最新 `origin/main` 创建语义清晰的独立分支。
- Agent 必须在独立 `git worktree` 中修改；这是保护用户工作区的本地安全边界，CI 不校验 worktree 或分支命名。
- 人工开发者可在普通 clone 中工作，建议使用独立分支。
- 推荐分支前缀：`feature/`、`fix/`、`hotfix/`、`sync/`、`docs/`、`chore/`、`refactor/`、`test/`、`otel/`。

标准起点：

```bash
git fetch origin main
git worktree add -b <scope>/<short-slug> ../sub2api-<short-slug> origin/main
```

## 4. 实现原则

- 先读 Issue、现有测试、调用链和相邻实现，再修改；禁止在同一仓库创造第二套约定。
- 修复根因，不用吞异常、关闭检查、伪造 fallback、硬编码环境或只特判样例掩盖问题。
- 代码必须保持接口契约、失败语义、数据一致性、并发边界、安全边界和性能成本；跨模块改动必须核对真实调用方与上下游契约。
- 新行为以可观察测试保护；测试必须能在合理缺陷下失败，不测试源码文本、mock 调用次数或偶然实现细节。
- 不做与 Issue 无关的重构、依赖升级、文档扩写或兼容 shim。废弃路径应连同全部调用方一起清理。
- 不得提交凭证、客户数据、生产配置、本地环境、生成缓存或评审中间产物。

## 5. 文档边界

长期文档必须服务用户、维护者或运行系统；思考过程和 Agent 工作材料只留在本地。

- `README.md`：产品能力、架构和协作入口。重大用户可见功能、长期架构或协作流程变化必须同步；普通实现细节不写入。
- `GIT_WORKFLOW.md`：Issue、分支/worktree、测试、评审、PR、同步、发布和回滚的唯一详细流程。
- `DEV_GUIDE.md`：当前可执行的本地环境与验证命令。
- `docs/`：已经稳定的功能、接口、部署、法律和发布记录；目录职责见 `docs/README.md`。
- `.agent/skills/`：全部仓库级 Agent Skill 的唯一可写真源；`.claude/skills` 与 `.codex/skills` 必须是指向 `../.agent/skills` 的相对软链接，禁止复制出多份 Skill。
- 禁止提交 `docs/superpowers/`、`openspec/changes/`、临时 plan/spec、评审事实包、对话记录和任务草稿。OpenSpec 框架配置可以保留，生成的 change 资产必须本地保存或进入 Issue/PR 后删除。

## 6. Pull Request 前门禁

Issue 关联、assignee、分支命名和 PR 标题属于协作建议，不进入 CI 阻塞门禁。使用这些元数据时应保持真实、可追溯。

创建 Pull Request 前，以下代码交付条件必须满足：

1. 分支已同步最新 `origin/main`，变更范围内聚且可解释。
2. 运行 `make pr-check`，所有适用测试、构建、lint、部署契约和安全检查真实通过。
3. 用户可见或错误路径变更完成一次真实 smoke；涉及模型追踪时额外执行 `.agent/skills/sub2api-model-trace-e2e/` 对应 smoke。
4. 纯文档且不涉及代码、配置、CI、依赖、生成物、运行行为或安全、权限、数据、数据库、发布边界的简单改动无需 AI 评审，PR 中记录 skip 原因即可。其余改动建议运行 `.agent/skills/reviewing-code-changes/`；高风险变更必须评审，Reviewer 只读，发现 P0/P1 后由主控修复并重跑相关测试。
5. README 和长期文档已按第 5 节更新，工作文档和评审中间产物未被跟踪。
6. PR 正文建议使用中文，按 [`GIT_WORKFLOW.md`](GIT_WORKFLOW.md) 3.7 节「PR 正文标准模板」填写（关联 Issue、背景/问题、改动、修复、范围与非目标、测试证据、评审、影响与风险、回滚、提交前自检）。

测试通过不等于评审通过，静态评审也不等于运行验证。任一适用代码交付门禁失败或缺少证据时，不得声称可以合入。

## 7. GitHub 与授权

- GitHub 操作优先使用 `gh`，本地 Git 操作使用 `git`。
- 用户未明确授权时，禁止 `git commit`、`git push`、`gh pr create`、合并、发布、删除分支或改写远端状态。
- “实现/修改/修复”不等于提交授权；“提交”“推送”“创建 PR”只授权用户明确点名的动作。
- 只允许对自己未合并的临时分支使用 `--force-with-lease`；禁止 `--force`。
- PR 必须通过 GitHub `Code Quality` 并解决全部讨论；满足这些条件后，PR 作者可以自行合并。
- Agent 只创建 Pull Request，不会自动合并 PR；任何合并必须由用户明确点名授权，并满足第 6 节门禁。

## 8. 项目专项规则

- 模型请求追踪实现位于 `backend/internal/modeltrace/`，行为映射位于 `.agent/skills/sub2api-model-trace-e2e/references/otel-spec-mapping.md`。
- 禁止直接连接或查询 `langfuse-clickhouse-1`；所有 Langfuse ClickHouse 查询必须通过 `langfuse-clickhouse-read-proxy-1` 执行。
- 修改 Ent Schema 后运行 `go generate ./ent`；修改 Wire Provider 后运行 `go generate ./cmd/server`，并提交对应生成文件。
- 修改 `frontend/package.json` 时同步更新 `frontend/pnpm-lock.yaml`。
- 禁止在生产路径硬编码本地 endpoint；禁止在日志、文档、Issue 或 PR 中记录任何真实或本地测试凭据。
