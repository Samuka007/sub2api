## 关联 Issue

Closes #

- [ ] Issue 正文包含问题、方案、验收标准和影响
- [ ] PR 作者是该 Issue 的 assignee

## 问题与方案

<!-- 为什么修改、采用什么方案、关键取舍。 -->

## 范围与非目标

<!-- 本次修改包含与明确不包含的内容。 -->

## 变更内容

<!-- 按用户可见行为或独立模块列出。 -->

## 验证证据

- [ ] 分支同步最新 `origin/main` 后，`make pr-check` 完整通过
- [ ] 用户可见成功路径 smoke 通过
- [ ] 错误/边界路径已验证
- [ ] 所有 skip、未运行或环境阻塞已明确列出

测试与 smoke 命令、退出结果、关键输出：

```text

```

## AI 评审

- [ ] 已按 `.agent/skills/reviewing-code-changes/SKILL.md` 评审最终 `origin/main...HEAD` 差异
- [ ] 已采纳 P0/P1 全部修复并完成相关测试与定向复审
- [ ] 当前没有阻塞 finding

评审结论、修复 finding、保留 P2 及理由：

```text

```

## 文档

- [ ] README 已更新，或本次不需要更新并说明原因
- [ ] 长期 API/部署/运维/Skill 文档已同步
- [ ] 未提交 `docs/superpowers/`、`openspec/changes/` 或评审中间产物

说明：

## 数据、配置与兼容性

- 数据库/数据：
- API/上下游：
- 新增或修改配置：
- 向后兼容性：
- 权限/安全/隐私：
- 性能/并发：

## 发布、冒烟与回滚

- 发布方式：
- 生产冒烟：
- 回滚镜像或步骤：
- 不可逆数据影响：

## 上游同步

<!-- 非同步 PR 填“不适用”。同步 PR 必须填写官方 tag/commit、release note、冲突和私有功能回归。 -->

- 官方 tag：
- 官方 commit：
- Release note：
- `.upstream-version`：
- 冲突及处理：
