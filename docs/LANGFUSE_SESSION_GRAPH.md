# Langfuse trace session 图

`scripts/langfuse_session_graph.py` 在热存储侧把 Langfuse trace 转换为可增长的
session 图。它只处理数据，不读取网关请求，也不修改 Langfuse；热存储 reader 负责把
一个不可变时间窗口以 JSONL 流传入。

## 输入契约

每个不可变窗口只能包含一个 project；混入多个 `project_id` 会直接失败，避免同名
session 跨 project 合并。每行是一个 trace JSON 对象。原始行至少应提供 `id`，并可提供 `timestamp`、
`project_id`、`user_id`、`session_id`、`metadata`、`input` 和 `output`。同一 `id`
重复出现时保留时间戳最新的一行；无 `id` 的行不参与构图并计入
`invalid_trace_rows`。

reader 也可以传入 `_compact: true` 的热侧预处理行，并提供
`history_sequence`、`history_has_response`、`history_turns` 以及
`input_<envelope-key>` 字段。`history_sequence` 必须由热侧按消息顺序计算稳定的
SHA-256 指纹。compact 行不包含消息正文，因此不会生成新的内容对象；对应对象必须已由
热侧写入内容寻址存储。

## 恢复规则

证据按以下顺序应用，冲突时不猜测：

1. Langfuse 已有 `session_id`，或请求 envelope 中显式的 session/conversation ID。
2. 独立的 thread ID：同一 thread 有唯一 session 锚点时继承该根；无锚点且至少有
   两条 trace 时建立 provisional thread 节点。thread ID 永远不直接当作 root session ID。
3. OpenAI Responses 的 `previous_response_id` 链。生成 ID 以链根 trace 为锚，追加
   turn 不会改变已有 session ID。
4. 同一 project/user/protocol 范围内的精确 history 前缀。最长历史形成叶节点，共享
   前缀形成 trunk，并用 `history_fork` 边连接分支。

单条 user 输入和空输入默认排除。只有 user、没有任何 assistant/tool 响应的多消息历史
保持 unresolved。重复 response ID、多个 session 锚点等冲突也保持 unresolved，并在
`dispositions` 和报告中给出原因。

## 去重与输出

运行示例：

```bash
python3 scripts/langfuse_session_graph.py \
  --input /path/to/hot-traces.jsonl \
  --objects-output /path/to/window.message-objects.jsonl \
  --assignments-output /path/to/window.assignments.jsonl \
  --graph-output /path/to/window.graph.json
```

输出职责如下：

- `message-objects.jsonl`：按规范化消息的 SHA-256 寻址，每个 user/assistant/tool
  内容只写一次。保留消息语义字段和 PII，不执行手机号、邮箱、身份证号等脱敏。
- `assignments.jsonl`：trace 到 session 的归属、来源和置信度。
- `graph.json`：session 节点、fork 边、未处理原因，以及每条 trace 的
  `parent_trace_id + append_message_ids`。图中没有消息正文。

内容对象只承载用于 session 恢复的 conversation history。完整 trace、observation、
system/developer 消息和请求 envelope 的保真归档由归档 writer 负责，不能用对象流替代。
三个输出路径必须属于同一个不可变窗口；命令最后原子写入 graph，因此下游只在 graph
出现后消费其余两个文件，不得复用或覆盖已完成窗口的路径。

`report.raw_coverage` 以全部有效 trace 为分母；`eligible_coverage` 排除明确不表达
session 的空输入、单 user 输入和其他不可恢复输入。`history_references` 与
`unique_history_fingerprints` 的差值表示 history 引用去重数量。
