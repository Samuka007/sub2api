---
name: reviewing-code-changes
description: |
  对 PR、暂存区或本地 diff 执行证据优先、测试优先的中文代码评审；按风险选择单 reviewer 或一次并行多角色对抗式 reviewer 批次，并校验、融合、裁定 findings。

  触发条件（满足任一）：
  1. 用户要求评审代码变更、PR、staged changes 或全部本地改动（如“帮我 review 这个 PR”“评审暂存改动”）。
  2. 用户要求检查测试覆盖、测试可信度、联调风险或安全问题（例如“这些测试会不会假通过”“检查 integration risk”）。
  3. 用户明确要求复审修复或执行对抗式多智能体评审（如“复审 accepted P1”“做多角色 code review”）。

  不触发（留给 diagnosing-bugs、verification-before-completion 或 antcode-skill）：
  - 只定位已发生的故障、异常或性能回归，留给 diagnosing-bugs。
  - 只运行完成门禁而不评审变更，留给 verification-before-completion。
  - 只执行 AntCode 评论、投票、合并或 reviewer 管理，留给 antcode-skill。
  默认路由：未说明评审范围时使用 all；明确说 staged/暂存区时使用 staged，空 diff 不自动改审其他范围。普通窄范围变更默认单 reviewer；明确要求多智能体，或涉及安全、权限、数据删除、数据库、发布链路、跨模块契约时，使用一次并行多角色对抗批次。
---

# 代码变更评审

## 核心规则（最高优先级）

按以下优先级处理冲突：用户与系统安全边界 > 可验证事实 > 评审完整性 > 输出形式与效率。

### 安全与权限边界

- **禁止**：用户未明确要求时执行 `git commit`、`git push`、创建或提交 PR、远端评论、投票、合并或修改 reviewer | 原因：评审授权不等于写仓库或写远端授权 | 替代：只输出本地中文报告，并列出可供用户确认的后续动作。
- **禁止**：把 merge、删除分支、回滚、覆盖远端等不可逆操作夹带在评审流程中 | 原因：评审只建立结论，不授予处置权 | 替代：交给对应工具或 Skill 先做 dry-run/preview，展示影响范围并取得用户明确确认。
- **禁止**：把 token、Cookie、私钥、完整鉴权 URL 或其他凭证放入事实包、subagent prompt 或报告 | 原因：并行上下文和报告可能扩大泄露面 | 替代：遮蔽凭证，仅保留判断风险所需的字段名、调用位置和脱敏证据。
- **禁止**：让 reviewer、正方、反方或裁定 agent 修改代码 | 原因：评审者同时修复会污染原始证据并削弱独立性 | 替代：让它们只产出结构化发现；用户要求修复时由主控在评审结束后另行处理。

### 事实、测试与误报边界

- **禁止**：用经验、mock、样例数据或“看起来正确”补齐缺失的接口契约、需求或运行结果 | 原因：静态推断不能证明真实联调或运行行为 | 替代：填写 `missing_evidence`，标记 `[待补证据]` 或 `[未验证]`，并说明已搜索范围。
- **禁止**：报告没有仓库相对 `file`、正整数 `line`、非空 `evidence` 和风险链条的 finding | 原因：无法定位和复核的问题不可执行且容易误报 | 替代：补齐代码、测试、日志或文档证据；仍无法定位则丢弃。
- **禁止**：把纯格式、命名偏好、无行为影响的重构建议列为 finding | 原因：噪声会掩盖真实缺陷并误导优先级 | 替代：只保留会影响行为证明、用户结果、安全、并发、性能或长期契约的风险。
- **禁止**：把测试通过写成联调通过，或把未运行的命令写成验证成功 | 原因：测试替身与真实依赖不同，未执行也没有运行证据 | 替代：分别陈述测试证据、真实契约证据和缺失项，只引用新鲜退出码、stdout 或产物。
- **禁止**：AntCode HTTP/retry PR 在取得同一 PR 的 diff、commits 和 review rules 后，未先在用户可见答复中列出 integration-search 三源计划便形成联调结论 | 原因：真实工作仓库、本地设计/接口文档与 Yuque 任一未检索，都会把 mock 自洽误当成真实契约 | 替代：计划逐项列出三源；每源检索后记录命中证据，未命中记录 `missing_evidence`；三源全部完成前只报告计划或进度，不给出联调结论。
- 先读需求、设计和 PR 描述，再审测试，最后审实现；测试是否证明边界、失败路径、状态迁移、幂等和真实契约优先于代码风格。
- 主控必须复核 subagent 证据；subagent 只提供候选 finding，不决定最终结论。
- 反方给出有效反证时必须拒绝或降级；结论强度不得超过证据强度。

### Reviewer 次数与路由边界

- 普通评审只执行一次单 reviewer pass：若当前主控未参与被审改动、尚未形成候选 finding，直接由主控在固定事实包上完成该 pass，不再套一层 subagent；若主控参与过实现、修复或已有预设结论，则仍启动一个独立 reviewer subagent。两种路径都不追加 re-reviewer，也不循环“评审—修复—再评审”。
- 多角色路由只启动一次并行 reviewer wave；正方、反方和裁定 agent 是同轮证据裁决，不是第二轮 reviewer。
- 只有用户明确要求复审，或安全、权限、数据删除、数据库、发布链路等高风险变更需要复核时，才进入复审场景；每次请求仍只运行一轮 reviewer。
- 定向复审派发前，用户可见答复必须给出 `route: targeted_re-review`、`finding_ids`、`files/hunks`、`repair_notes`、`roles` 和 `verifications`；各项只允许与上一轮 accepted finding 有可证明的直接关系，其中 `repair_notes` 必须说明原行为、修复意图以及实现/测试如何对应原 finding，`roles` 只保留原发现角色、测试角色和匹配该 finding 风险类别的角色。发现任何不属于原 accepted finding 的新 `critical`/`important` 时，即使它位于相同文件或 hunk，也必须改写为 `route: full_review_required`、标记 `targeted_result: stopped` 并停止当前定向结论；不得顺手增加角色、文件或验证，也不得自动启动全量评审。
- reviewer 输出不合法时显式报告并丢弃无效结果，不以第二次 reviewer 调用静默补救。

### 严重度与完成口径

- `critical`：可导致数据错乱、权限或安全突破、关键链路失败，或严重假阳性测试掩盖上述风险；报告映射为 `P0`。
- `important`：关键行为存在真实缺陷，或缺少失败、边界、幂等、状态迁移、契约测试而无法证明；报告映射为 `P1`。
- `minor`：有证据的局部测试或可维护性问题，不直接破坏关键行为；不得升级纯风格偏好。
- 只有不存在已采纳的 `critical`/`important`，且请求要求的验证已有新鲜成功证据时，才能给出“可以合入”；否则给出“阻塞合入”或“有条件通过”。

## 依赖清单

| 类型 | 名称 | 用途 | 必须/可选 |
|------|------|------|-----------|
| 脚本 | `scripts/prepare_review_context.py` | 从 Git diff 生成变更、测试、文档和联调线索事实包 | 必须 |
| 脚本 | `scripts/shuffle_diff.py` | 确定性打乱文件块和 hunk，降低多角色顺序偏见 | 多角色路由必须 |
| 脚本 | `scripts/validate_findings.py` | 校验 finding 字段、枚举、行号、置信度和证据 | 必须 |
| 脚本 | `scripts/fuse_findings.py` | 按位置与类别融合发现并生成 `F-NNN` 和初始置信度 | 必须 |
| CLI | `python3` | 执行四个确定性脚本并解析 JSON 产物 | 必须 |
| CLI | `git` | 获取仓库根目录及 staged、all、range、PR range diff | 必须 |
| CLI | `rg` | `--integration-search` 时在限定的 `work-root` 仓库中搜索联调线索 | 条件必须 |
| Skill | `antcode-skill` | 解析 AntCode PR，获取 diff、commits、review rules；执行经授权的远端动作 | AntCode PR 条件必须 |
| Skill | `skill-yuque-cli-guide` | 按 URL 或关键词读取真实设计和接口文档 | AntCode HTTP/retry PR 联调检索时必须；其他有 Yuque 线索时可选 |
| 工具 | subagent/task | 在主控不具独立性时执行隔离单 reviewer；执行多角色 reviewer wave 与裁定 | 主控参与过实现/已有预设结论时条件必须；多角色路由必须；满足 `reviewer_execution=direct` 时不使用 |
| 输入 | 需求、设计、PR 描述、CI/测试输出、真实接口契约 | 校准预期行为并提高结论强度 | 可选；缺失须标注 |

AI 负责准入、路由、证据复核、角色编排和用户沟通；脚本只负责确定性收集、打乱、校验和融合。不得手算脚本输出或伪造成功信号。

## 场景与执行 SOP

### 场景一：统一准入与两级路由

**触发**：收到任何 PR、本地 diff、测试覆盖、联调风险或修复复审请求。

**步骤**：

1. 执行准入守卫：确认存在可访问的 Git 仓库或用户提供的 diff，并固定范围为 `staged`、`all`、`range` 或 `pr`。
2. 若用户明确指定 `staged` 而 staged diff 为空，停止并报告“指定范围无可审变更”；不得自动切换到 `all`。
3. 若范围未说明，默认 `all`；若是 AntCode PR URL，固定为 `pr`，并在构建事实包或派发 reviewer 前先由 `antcode-skill` 获取同一 PR 的 diff、commits 和 review rules；任一项未取得都进入 AntCode PR 失败分支，不猜 base/head。
4. 记录需求、设计、PR 描述、CI 和测试输出是否存在；缺失不阻止静态评审，但必须降低相关结论强度。对 AntCode HTTP/retry PR，必须在上述 diff、commits、review rules 三项事实取得后，先向用户展示 integration-search 计划，明确列出真实工作仓库、本地设计/接口文档、Yuque 三源及逐源的命中/`missing_evidence` 记录方式，再继续检索。
5. 执行路由守卫：

| 条件 | Reviewer 路由 | 输入范围 |
|------|---------------|----------|
| 普通、窄范围、无高风险边界，且未明确要求多智能体 | 单 reviewer，一次 pass | 完整事实包和原始 diff |
| 明确要求多智能体或对抗评审 | 5-7 个角色，一次并行 wave | 事实包和不同 shuffle pass |
| 安全、权限、数据删除、数据库、发布链路、跨模块契约 | 5-7 个相关角色，一次并行 wave | 完整事实包、真实契约和验证证据 |
| 用户明确要求复审局部修复 | 单个定向 reviewer；高风险时改为一次多角色 wave | 修复 hunk、原 finding、最新验证 |
| subagent/task 不可用 | 不执行伪多角色评审 | 明确写“评审基础设施不可用”，不得声称完成 |

6. 远端写请求与评审分离：先完成报告；只有用户明确要求时，才交给 `antcode-skill` 预览将写入的内容。合并等不可逆动作必须再次确认。

### 场景二：准备可审计的变更上下文

**触发**：准入通过，已经固定评审范围和 reviewer 路由。

**步骤**：

1. 先读取用户提供的需求、设计、PR 描述和验证输出，记录来源；不要先形成结论再选择证据。
2. 在被评审仓库中运行 `scripts/prepare_review_context.py` 对应命令，输出固定到 `/tmp/review-context.json`。
3. 脚本退出码非零时保留原始 stderr，按失败文本处理：
   - `not a git repository`：停止，报告仓库路径错误。
   - `--base is required for --mode range`：要求补 base，不切换模式。
   - `PR mode needs an externally fetched diff...`：先用 `antcode-skill` 或用户提供的 base/head/diff，再重建事实包。
4. 退出码为零后，同时核对 stdout 含 `wrote /tmp/review-context.json`、产物可解析、`schema_version` 为 `1.0` 且 `diff` 非空；只满足 stdout 不算成功。
5. diff 涉及 RPC、HTTP、MQ、DB schema、配置中心、SDK 或跨仓库调用时，使用 `--integration-search`，并保持 `--work-repo-limit 20 --doc-limit 80` 的有界搜索。
6. 对 AntCode HTTP/retry PR，按用户可见计划完成三源台账；每源必须记录 `hit`（来源定位与契约摘录）或 `missing_evidence`（已搜索范围、关键词与失败输出）：
   - **真实工作仓库**：根据 `integration_terms` 和 `work_repo_matches` 检索真实下游仓库与调用方。
   - **本地设计/接口文档**：根据 `doc_files` 与相同关键词核查本地设计、接口和契约文档。
   - **Yuque**：按 `skill-yuque-cli-guide` 使用事实包中的 URL 或关键词执行只读检索；无 URL 也必须搜索，`yuque.suggested_commands` 只是待执行建议，不能当成命中。
7. 三源尚未全部得到 `hit` 或 `missing_evidence` 记录时，不得形成“联调通过”“联调失败”“风险已排除”等联调结论，只能向用户报告计划或检索进度；三源完成后，任一来源为 `missing_evidence` 时，将依赖该来源的判断降级为 `disputed`，只列入“联调依据缺口”，不得猜测或创建 mock 代替。
8. 当 AntCode PR 属于跨模块契约，且 diff 涉及 HTTP client、retry logic 或 downstream mock 任一项时，沿用场景一的一次多角色 reviewer wave，并在派发前完成、向全部角色传入以下逐项证据清单：
   - **真实契约与 mock**：用上述三源台账命中的真实下游仓库、调用方、本地文档或 Yuque 原文作来源，逐项比对 mock 的请求、HTTP 状态、业务状态和响应字段；mock 自洽或测试通过不能代替真实契约。
   - **Timeout**：测试必须覆盖实际 timeout 触发路径，并断言对外错误或状态、是否进入 retry 以及该次调用的可观察结果，不能只断言抛出异常。
   - **业务失败与响应缺字段**：测试必须覆盖 transport 成功但业务失败、关键响应字段缺失两条独立路径，并断言错误映射、返回状态和 retry 决策。
   - **幂等与重试副作用**：测试必须覆盖首次请求可能已产生效果后再次 retry 的路径，证明真实幂等键、去重或等效契约能阻止重复写入、扣款、发消息等副作用；不能只断言调用次数。
   - **逐项裁决**：每项都记录契约来源与测试位置；任一真实契约未取得时，把具体缺口写入 `missing_evidence`，将依赖该契约的结论裁定为 `disputed`，报告只能列入“联调依据缺口”，不得写“联调通过”或同义结论。
9. 仅多角色路由运行 `scripts/shuffle_diff.py`。要求退出码为零、stdout 含真实 `passes=N blocks=M`，产物可解析且 `blocks > 0`；每个角色只接收一个 pass，避免继承主控结论。

### 场景三：执行一次单 Reviewer 评审

**触发**：路由守卫判定为普通或窄范围变更。

**步骤**：

1. 先判定 reviewer 独立性并记录 `reviewer_execution=direct|independent_subagent`：当前主控未参与被审改动且尚未形成候选 finding 时，直接以事实包、原始 diff、需求证据执行一次 pass；否则启动一个独立 reviewer subagent，且只传这些冻结输入，不传主控猜测或预期答案。不得为追求速度把参与过实现的主控标成 `direct`。
2. 要求 reviewer 先按 `references/test-review-methods.md` 审测试证明力，再审实现；涉及安全或联调时把该风险面加入同一 reviewer 的重点，不另开第二轮。
3. reviewer 只输出 JSON 数组。每个 finding 必须包含：

| 字段 | 约束 |
|------|------|
| `file` | 仓库相对路径 |
| `line` | 正整数，定位到最接近的相关行 |
| `role` | 非空 reviewer 角色 |
| `category` | `test_gap`、`false_positive`、`requirement_mismatch`、`bug`、`security`、`concurrency`、`performance`、`maintainability`、`integration_gap` 之一 |
| `severity` | `critical`、`important`、`minor` 之一 |
| `confidence` | `[0, 1]` 数值 |
| `evidence` | 非空、可复核的代码、测试、日志或文档证据 |
| `why_it_matters` | 从当前行为到用户或工程影响的风险链条 |
| `suggested_fix` | 具体修复方向；未知时说明应补证据 |
| `test_gap` | 应补的可观察行为测试；无则空字符串 |
| `missing_evidence` | 缺失契约、仓库、文档或输出；无则空字符串 |

4. 把原始 JSON 写入 `/tmp/reviewer-findings.json`，运行 `scripts/validate_findings.py`。退出码为 1 或输出字段错误时，报告该 reviewer 结果无效并丢弃；不得重新调用 reviewer 补造字段。
5. 校验成功后运行 `scripts/fuse_findings.py`。即使只有一个 reviewer，也用融合产物统一 `finding_id`、排序和 schema。
6. 主控逐条打开 `file:line` 及必要上下文，执行误报过滤；无法复核的 finding 进入 disputed 或 rejected，不进入主问题。
7. 不自动修复、不自动复审。根据最新代码、测试和文档证据输出一次报告。

### 场景四：执行一次多角色对抗式评审

**触发**：用户明确要求多智能体/对抗评审，或路由守卫命中高风险边界。

**步骤**：

1. 读取 `references/reviewer-contracts.md` 全文，选择 5-7 个互补角色；至少包含测试策略审查员、测试怀疑者、缺陷猎手及与变更风险匹配的安全、并发性能、需求或可维护性角色。
2. 在一次并行 reviewer wave 中派发所有角色。每个角色只看自己的风险面、一个 shuffle pass、同一事实包和 Finding schema，不继承其他角色输出。
3. 分别运行 `scripts/validate_findings.py`；只聚合合法数组。任一非法结果都记录失败原因并丢弃，不补字段、不重跑 reviewer。
4. 若有效角色不足 5 个，标记“多角色评审不完整”，不得给出“可以合入”；仍可报告已经复核的有效 finding。
5. 将合法 finding 原样拼接为 `/tmp/all-findings.json`，运行 `scripts/fuse_findings.py`。脚本按 `(file, line, category)` 精确分桶；不同位置的语义近重复由主控在报告层合并，不篡改原证据。
6. 对每个融合后的 `critical`/`important`，并行启动正方和反方 agent；随后由裁定 agent 只依据事实包与双方证据给出 `accepted`、`disputed` 或 `rejected`。
7. 裁定 `accepted` 且 `confidence >= 0.70` 才进入主问题；`disputed` 进入低置信度/待补证据；`rejected` 不进入主问题，可简述拒绝原因。
8. 正方、反方和裁定 agent 不得引入新 finding。发现新风险只能作为缺口记录，不能绕过 Finding 校验与本轮 reviewer wave。
9. 完成裁定后停止，不自动启动 re-reviewer 或第二个 reviewer wave。

### 场景五：融合、严重度与误报过滤

**触发**：单 reviewer 或多角色产出已经通过 schema 校验。

**步骤**：

1. 运行融合脚本并核对真实算法：以同桶最大原始 `confidence` 为起点；两个及以上独立角色加 `0.15`；存在 `test_gap` 加 `0.20`；存在 `missing_evidence` 减 `0.10`；类别为 `test_gap`、`false_positive` 或 `integration_gap` 再加 `0.05`；最后限制到 `[0, 1]`。
2. 把融合分视为初始排序信号，不视为真伪证明；多角色模式以裁定结果为准，单 reviewer 模式由主控完成同等证据复核。
3. 对每条 finding 依次检查：
   - `file:line` 是否存在并指向相关行为，而非只碰巧位于变更文件。
   - `evidence` 是否支持声称的失败路径，还是只复述代码。
   - 现有测试、调用方、类型约束、事务边界或文档是否提供反证。
   - 风险是否能沿调用链到达用户、安全、数据或可观察工程影响。
   - 缺失真实契约时，结论是否已经降级而非把 mock 当真。
4. 同一根因的重复 finding 只保留证据最强的一条并合并角色；相互冲突时展示最强正反证据，不按角色数量投票。
5. 测试缺口若使关键实现无法被证明，不因实现“看起来正确”降为 `minor`；反之，已有可信测试覆盖同一路径时应拒绝重复缺口。

### 场景六：验证证据并输出中文报告

**触发**：finding 已完成校验、融合和误报过滤。

**步骤**：

1. 核对产物链：`review-context.json` → 多角色时的 `review-shuffled.json` → validated findings → `fused-findings.json`；任何断点都写明失败，不静默跳过。
2. 只运行仓库已有、与变更相关、不会触达生产的定向测试或检查命令；不安装依赖、不改配置来制造通过。
3. 成功只引用新鲜的命令、退出码、stdout 或可验证产物。无法运行时写清命令、阻塞原因和 `[未验证]`，静态阅读不得表述为运行证明。
4. 读取 `references/output-format.md`，按“结论、必须先修、测试缺口、实现风险、联调依据缺口、低置信度/未采纳发现、建议验证命令”组织中文报告。
5. 每个 `P0/P1` 写明位置、类型、置信度、证据、风险链条、非动物类比、修复建议和应补测试；没有证据的栏目不要用套话填充。
6. 报告顶部写证据完整性：已读取的代码、测试、需求、文档和命令输出，以及缺失项。
7. 输出报告后停止。用户未明确要求时，不提交代码、不写远端、不替用户执行合并决策。

### 场景七：用户明确要求修复后复审

**触发**：用户在上一轮报告后明确要求复审具体修复；默认不自动进入本场景。

**步骤**：

1. 建立定向范围清单：只收集修复 hunk、上一轮 accepted finding、修复说明、相关上下文和最新验证输出，不重复发送无关完整历史。
2. 派发前先在用户可见答复中列出复审范围包：`route: targeted_re-review`；`finding_ids` 为本轮逐条复核的原 accepted finding；`files/hunks` 只含与这些 finding 直接相关的实现和测试变化行；`repair_notes` 记录原行为、修复意图以及每个实现/测试 hunk 如何对应原 finding；`roles` 只含原发现角色、测试角色和与原 finding 风险链匹配的角色；`verifications` 只含重放原失败路径及相关测试的命令与最新输出。任何字段缺证据都明确标记 `[待补证据]`；任何无法证明直接关系的角色、文件或验证都排除。
3. 在该范围包内完成路由：普通局部修复使用一个定向 reviewer；若原 finding 或其修复跨模块、改变契约或涉及高风险边界，使用一次 5-7 角色 wave，但所有角色仍必须来自范围包，不得用无关角色凑数。
4. reviewer 仍只运行一轮，并使用相同 Finding schema、校验、融合和误报过滤门禁。
5. 只有本轮未触发范围升级时，才逐条回答原 finding 是 `fixed`、`partially_fixed` 还是 `not_fixed`；证据必须来自范围包内的最新代码和验证输出。
6. 任一 reviewer 若发现任何不属于原 accepted finding 的新 `critical`/`important`，即使它位于相同文件或 hunk，也只能记录其 `file:line`、严重度、证据和越界原因，不得把它加入本轮校验/融合、不得追加角色/文件/验证。用户可见答复必须改为 `route: full_review_required`、`targeted_result: stopped`，写明升级触发项和“需另起新的全量评审”，随后立即停止；不得输出本轮 `fixed`/`partially_fixed`/`not_fixed` 或合入结论，也不得自动启动第二个 reviewer wave。

### 失败分支总表

| 失败 | 显式处理 | 不允许的降级 |
|------|----------|--------------|
| 仓库不可访问或指定 diff 为空 | 报告路径、mode 和实际错误后停止 | 自动改审其他范围 |
| AntCode PR 事实获取失败 | 保留 `antcode-skill` 错误并列缺失字段 | 猜 PR 内容或 base/head |
| 联调仓库/契约/Yuque 文档缺失 | 记录搜索范围到 `missing_evidence` | 自编 mock 响应并宣称联调通过 |
| reviewer JSON 非法 | 记录 validator 错误并丢弃 | 人工补字段或再调 reviewer |
| 有效多角色少于 5 个 | 标记评审不完整，禁止“可以合入” | 冒充完整对抗评审 |
| 验证命令失败或未运行 | 引用真实退出码/stderr，标记未通过或未验证 | 仅凭静态阅读写成功 |
| 远端动作未获明确授权 | 停在本地报告 | 评论、投票、合并、改 reviewer 或提交 |

## 命令速查

以下命令从被评审仓库根目录执行；`.agent/skills/` 是仓库 Skill 唯一真源，`.claude/skills` 与 `.codex/skills` 是自动发现链接。示例中的 `/path/to/repo` 和 Git ref 需要替换为真实值。

### 准备事实包

```bash
python3 .agent/skills/reviewing-code-changes/scripts/prepare_review_context.py --mode staged --cwd /path/to/repo --out /tmp/review-context.json
python3 .agent/skills/reviewing-code-changes/scripts/prepare_review_context.py --mode all --cwd /path/to/repo --out /tmp/review-context.json
python3 .agent/skills/reviewing-code-changes/scripts/prepare_review_context.py --mode range --cwd /path/to/repo --base origin/main --head HEAD --out /tmp/review-context.json
python3 .agent/skills/reviewing-code-changes/scripts/prepare_review_context.py --mode pr --cwd /path/to/repo --diff-file /tmp/pr.diff --out /tmp/review-context.json
```

联调事实搜索：

```bash
python3 .agent/skills/reviewing-code-changes/scripts/prepare_review_context.py --mode all --cwd /path/to/repo --integration-search --work-root "$HOME/work" --work-repo-limit 20 --doc-limit 80 --out /tmp/review-context.json
```

真实成功信号：退出码 `0`，stdout 包含 `wrote /tmp/review-context.json` 和 `changed_files=N test_files=N docs=N`；同时产物必须可解析且 `diff` 非空。

### 打乱多角色输入

```bash
python3 .agent/skills/reviewing-code-changes/scripts/shuffle_diff.py --context /tmp/review-context.json --passes 7 --seed 20260622 --out /tmp/review-shuffled.json
```

真实成功信号：退出码 `0`，stdout 包含 `wrote /tmp/review-shuffled.json` 和 `passes=7 blocks=N`，且 `N > 0`。

### 校验 Finding

```bash
python3 .agent/skills/reviewing-code-changes/scripts/validate_findings.py /tmp/reviewer-findings.json --out /tmp/findings.validated.json
```

真实成功信号：退出码 `0` 且 stdout 为 `valid findings: N`；字段错误时退出码为 `1` 并逐行输出错误。

### 融合 Finding

```bash
python3 .agent/skills/reviewing-code-changes/scripts/fuse_findings.py /tmp/findings.validated.json --out /tmp/fused-findings.json
python3 .agent/skills/reviewing-code-changes/scripts/fuse_findings.py /tmp/all-findings.json --out /tmp/fused-findings.json
```

真实成功信号：退出码 `0`，stdout 包含 `wrote /tmp/fused-findings.json` 和 `fused_findings=N`；产物顶层必须含 `schema_version: "1.0"` 与 `findings`。

## 参考文档（按需读取）

| 场景 | 文件 |
|------|------|
| 单 reviewer 需要角色输入和 Finding schema 时只读对应章节；多角色路由需全文读取角色、正反对抗和裁定契约 | `references/reviewer-contracts.md` |
| 评审测试覆盖、mock 可信度、边界、状态迁移、幂等或联调测试时 | `references/test-review-methods.md` |
| findings 已裁定并准备输出最终中文报告时 | `references/output-format.md` |

参考文档服从本文件的最高优先级规则：其中多角色与对抗段只在对应路由启用，不能覆盖默认单 reviewer、单轮 reviewer 和用户授权边界。
