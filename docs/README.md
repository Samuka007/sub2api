# 文档目录与提交边界

`docs/` 只保存合并后仍对用户、维护者、运行系统或审计有长期价值的事实。Agent 的思考过程、计划、规格草稿、任务状态和评审中间产物不是仓库文档。

## 可提交文档

| 类别 | 目录或文件 | 内容 | 更新时机 |
| --- | --- | --- | --- |
| 产品与架构入口 | `README.md` | 重大用户可见能力、长期架构、仓库入口 | 对应行为或架构变化时 |
| 协作规约 | `AGENTS.md`、`GIT_WORKFLOW.md`、`DEV_GUIDE.md` | 强制原则、协作流程、可执行命令 | 流程或门禁变化时 |
| 稳定功能/API | `docs/*.md` | 已实现且仍维护的功能、接口和配置 | 契约变化时 |
| 部署与发布 | `docs/deployments/` | 已执行发布的版本、镜像、验证和回滚证据 | 发布完成后 |
| 法律 | `docs/legal/` | 已批准的法律与合规文本 | 审批后 |
| 可执行 Agent 流程 | `.agent/skills/<name>/` | `SKILL.md`、脚本、reference、测试 prompt；`.claude/skills`、`.codex/skills` 仅为自动发现软链接 | Skill 行为或发现入口变化时 |

稳定功能文档：

- [`LANGFUSE_SESSION_GRAPH.md`](LANGFUSE_SESSION_GRAPH.md)：热存储侧 trace 解析、
  history 去重与 session 图输出契约。
- [`PROMPT_AUDIT_DEEPSEEK.md`](PROMPT_AUDIT_DEEPSEEK.md)：使用官方 DeepSeek
  `deepseek-v4-flash` 进行提示词影子审计的 Adapter、Secret、验证和回滚契约。

任何新长期文档都必须有明确读者、权威来源、更新触发条件和可验证事实。能更新现有权威文档时，不创建第二份同义说明。

## 禁止提交的工作材料

以下内容只保存在本地临时目录或 GitHub Issue/PR，不进入版本库：

- `docs/superpowers/` 下的 plan、spec 和执行清单。
- `openspec/changes/` 下的 proposal、design、tasks、spec、source freeze 和证据包。
- Agent 对话、思维记录、任务 scratchpad、会话导出和个人笔记。
- `/tmp` 生成的 review context、finding、benchmark、截图和调试转储。
- `code-reviews/`、`.code-review-state` 和其他评审中间状态。
- 本地路径、测试凭据、生产配置、客户数据和未脱敏日志。

OpenSpec 的仓库级配置和 schema 可以保留在 `openspec/config.yaml`、`openspec/schemas/`，用于本地生成；生成的 change 资产在形成 Issue/PR 后应从工作区删除。

## 从工作材料提炼长期事实

1. 需求、方案、验收和取舍写入 GitHub Issue。
2. 代码与测试成为行为真源。
3. PR 记录差异、验证、评审、影响和回滚。
4. 只有合并后仍需查阅的稳定结论，才提炼进 README、API/部署文档或 Skill reference。
5. 发布完成后记录不可变 tag、commit、镜像 digest 和新鲜验证结果；不复制开发日记。

## README 更新判断

必须更新：

- 新增、删除或显著改变用户可见功能。
- 改变长期架构、部署拓扑、公开 API 或配置入口。
- 改变 Issue、分支、测试、评审、PR、发布等仓库协作入口。

通常不更新：

- 不改变行为的内部重构。
- 单个测试、mock、日志或私有辅助函数调整。
- 只在 Issue/PR 有价值的排查过程和一次性命令输出。

无法判断时，优先更新现有最窄权威文档；不要新建“最终版”“新版”“补充说明”等并行文件。
