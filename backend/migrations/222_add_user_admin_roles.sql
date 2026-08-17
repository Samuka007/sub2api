-- 222: 引入权威多角色集合 users.roles（JSONB），并回填历史数据。
--
-- 背景：
--   users.role 只有 admin/user 单值，无法表达「一个用户同时拥有多个管理员角色」。
--   本迁移新增 roles JSONB 数组作为权威授权来源，role 列保留为 legacy 兼容摘要
--   （super_admin → "admin"，其余 → "user"），授权逻辑只读取 roles。
--
-- 幂等性：
--   全部语句使用 IF NOT EXISTS / 条件更新，可安全重放。
--   未知/空 role 值保持 fail-closed（不自动升级为管理员）。

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS roles JSONB NOT NULL DEFAULT '[]'::jsonb;

-- 历史 admin 回填为 super_admin，保留全部管理能力（升级不锁死）。
UPDATE users
SET roles = '["super_admin"]'::jsonb
WHERE role = 'admin'
  AND (roles IS NULL OR roles = '[]'::jsonb);

-- 历史 user 及任何未知值保持空角色集合（fail-closed）。
UPDATE users
SET roles = '[]'::jsonb
WHERE roles IS NULL;
