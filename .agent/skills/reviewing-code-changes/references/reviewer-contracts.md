# 多角色评审契约

## 主控职责

当前 agent 是主控。主控负责准备事实包、派发 subagent、校验 JSON、融合发现、组织正反对抗、调用裁定 agent，并输出最终中文报告。subagent 只提供候选发现，不决定最终结论。

## 角色矩阵

| role | 关注点 | 必须优先看 |
| --- | --- | --- |
| 测试策略审查员 | 测试覆盖、测试设计、需求证明 | 边界值、等价类、判定表、状态迁移、异常路径 |
| 测试怀疑者 | 假阳性和弱断言 | mock 是否脱离真实契约、是否只测实现细节 |
| 需求守门员 | 需求、设计、PR 描述一致性 | 测试和实现是否偏离设计方案 |
| 缺陷猎手 | 逻辑错误、状态错误、空值、错误处理 | 可导致用户可见错误的路径 |
| 安全审查员 | 权限、注入、敏感信息、路径、鉴权 | 安全边界和越权面 |
| 并发性能审查员 | 并发、资源生命周期、复杂度、阻塞 | 竞态、幂等、重试、热点路径 |
| 可维护性审查员 | 接口契约、迁移兼容、长期维护风险 | 会阻碍后续修改的真实问题 |


## Subagent 输入模板

```markdown
你是【角色名】。只评审你负责的风险面，不做泛化 code review。

事实来源：
- 评审范围：{PR / staged / all}
- 事实包：{context 摘要或路径}
- 打乱 diff：{diff 摘要或路径}
- 需求/设计/文档：{已有证据；没有就写“缺失”}

硬规则：
- 只基于代码、测试、真实仓库和文档。
- 涉及联调时禁止自行 mock 或猜测接口行为。
- 缺少依据时填写 missing_evidence，不要把猜测写成结论。
- 测试问题优先；重点判断测试是否能证明实现。
- 输出纯 JSON 数组，不要 Markdown。
```

## Finding 输出契约

每个 subagent 只输出 JSON 数组：

```json
[
  {
    "file": "path/to/file.py",
    "line": 42,
    "role": "测试策略审查员",
    "category": "test_gap|false_positive|requirement_mismatch|bug|security|concurrency|performance|maintainability|integration_gap",
    "severity": "critical|important|minor",
    "confidence": 0.82,
    "evidence": "具体代码、测试或文档证据",
    "why_it_matters": "为什么这是用户可见风险或工程风险",
    "suggested_fix": "具体修复方向；不知道就写需要补什么证据",
    "test_gap": "应补的测试；没有就写空字符串",
    "missing_evidence": "缺少的仓库、文档、接口契约或命令输出；没有就写空字符串"
  }
]
```

约束：

- `file` 必须是仓库相对路径；无法定位则不要输出 finding。
- `line` 必须是正整数；只能定位到文件级时使用最接近的变更行。
- `confidence` 必须在 `[0, 1]`。
- `severity=critical` 只用于会造成数据错乱、安全问题、关键链路失败、严重假阳性测试的风险。
- 纯格式、命名、个人偏好不得输出。

## 正反对抗契约

对每个 `critical` / `important` 发现，主控启动三类 agent：

### 正方 agent

目标：证明 finding 是真实问题。

输出：

```json
{
  "finding_id": "F-001",
  "position": "support",
  "confidence": 0.0,
  "strongest_evidence": ["代码/测试/文档证据"],
  "risk_chain": "从代码到用户影响的链条",
  "required_test": "最小应补测试"
}
```

### 反方 agent

目标：证明 finding 是误报、证据不足或优先级过高。

输出：

```json
{
  "finding_id": "F-001",
  "position": "oppose",
  "confidence": 0.0,
  "counter_evidence": ["反证"],
  "downgrade_reason": "为什么应降级或拒绝",
  "missing_evidence": "还缺什么才能判断"
}
```

### 裁定 agent

目标：只基于代码、测试、文档和正反双方证据裁定，不引入新猜测。

输出：

```json
{
  "finding_id": "F-001",
  "verdict": "accepted|disputed|rejected",
  "severity": "critical|important|minor",
  "confidence": 0.0,
  "accepted_evidence": ["采纳依据"],
  "rejected_reason": "拒绝或降级理由",
  "required_followup": "需要补代码、补测试、补文档还是补联调证据"
}
```
## 主控裁决规则

- 裁定为 `accepted` 且 `confidence >= 0.70`：进入报告主问题。
- 裁定为 `disputed`：进入“低置信度/需补证据”。
- 裁定为 `rejected`：不进主问题，但可在“未采纳发现”一句话说明。
- 测试缺口如果会让关键实现无证据保护，不能因为实现“看起来正确”而降为 minor。
