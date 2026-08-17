import type { AdminRole, AdminPermission } from '@/types'

/**
 * 角色 → 权限集合映射，与后端 domain/admin_permissions.go 保持一致。
 * super_admin 短路通过任意权限，因此不在此表中列权限。
 */
const ROLE_PERMISSIONS: Record<AdminRole, AdminPermission[]> = {
  super_admin: [],
  billing_admin: [
    'admin.users.read',
    'admin.users.balance.adjust',
    'admin.redeem_codes.manage',
    'admin.promo_codes.manage',
  ],
  upstream_admin: [
    'admin.accounts.manage',
    'admin.groups.manage',
    'admin.proxies.manage',
    'admin.channels.manage',
    'admin.openai_oauth.manage',
    'admin.gemini_oauth.manage',
    'admin.antigravity_oauth.manage',
    'admin.grok_oauth.manage',
  ],
}

/** 是否为任一管理员角色（billing/upstream/super）。 */
export function hasAdminRole(roles: AdminRole[]): boolean {
  return (
    roles.includes('super_admin') ||
    roles.includes('billing_admin') ||
    roles.includes('upstream_admin')
  )
}

/** 角色集合是否授予给定权限；super_admin 短路通过。 */
export function hasPermission(roles: AdminRole[], permission: AdminPermission): boolean {
  if (roles.includes('super_admin')) return true
  for (const role of roles) {
    if ((ROLE_PERMISSIONS[role] ?? []).includes(permission)) return true
  }
  return false
}

/** 规范化角色数组：去空白、去重、排序、过滤未知值。 */
export function normalizeAdminRoles(roles: AdminRole[]): AdminRole[] {
  const known: AdminRole[] = ['super_admin', 'billing_admin', 'upstream_admin']
  return [...new Set(roles.filter((r) => known.includes(r)))].sort()
}

/** 全部可选管理员角色，用于多选控件。 */
export const ALL_ADMIN_ROLES: AdminRole[] = ['super_admin', 'billing_admin', 'upstream_admin']
