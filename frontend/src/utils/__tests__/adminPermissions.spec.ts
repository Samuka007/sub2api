import { describe, it, expect } from 'vitest'
import { hasAdminRole, hasPermission, normalizeAdminRoles, ALL_ADMIN_ROLES } from '@/utils/adminPermissions'
import type { AdminRole } from '@/types'

describe('adminPermissions', () => {
  it('识别任意管理员角色', () => {
    expect(hasAdminRole(['super_admin'])).toBe(true)
    expect(hasAdminRole(['billing_admin'])).toBe(true)
    expect(hasAdminRole(['upstream_admin'])).toBe(true)
    expect(hasAdminRole(['billing_admin', 'upstream_admin'])).toBe(true)
    expect(hasAdminRole([])).toBe(false)
  })

  it('super_admin 短路通过任意权限', () => {
    expect(hasPermission(['super_admin'], 'admin.super')).toBe(true)
    expect(hasPermission(['super_admin'], 'admin.accounts.manage')).toBe(true)
    expect(hasPermission(['super_admin'], 'admin.users.balance.adjust')).toBe(true)
  })

  it('billing_admin 只拥有计费权限', () => {
    expect(hasPermission(['billing_admin'], 'admin.users.balance.adjust')).toBe(true)
    expect(hasPermission(['billing_admin'], 'admin.redeem_codes.manage')).toBe(true)
    expect(hasPermission(['billing_admin'], 'admin.promo_codes.manage')).toBe(true)
    expect(hasPermission(['billing_admin'], 'admin.accounts.manage')).toBe(false)
    expect(hasPermission(['billing_admin'], 'admin.super')).toBe(false)
  })

  it('upstream_admin 只拥有上游权限', () => {
    expect(hasPermission(['upstream_admin'], 'admin.accounts.manage')).toBe(true)
    expect(hasPermission(['upstream_admin'], 'admin.groups.manage')).toBe(true)
    expect(hasPermission(['upstream_admin'], 'admin.proxies.manage')).toBe(true)
    expect(hasPermission(['upstream_admin'], 'admin.channels.manage')).toBe(true)
    expect(hasPermission(['upstream_admin'], 'admin.users.balance.adjust')).toBe(false)
    expect(hasPermission(['upstream_admin'], 'admin.super')).toBe(false)
  })

  it('多角色取并集，且不放大到超级权限', () => {
    const roles: AdminRole[] = ['billing_admin', 'upstream_admin']
    expect(hasPermission(roles, 'admin.users.balance.adjust')).toBe(true)
    expect(hasPermission(roles, 'admin.accounts.manage')).toBe(true)
    expect(hasPermission(roles, 'admin.super')).toBe(false)
  })

  it('normalizeAdminRoles 去重排序并过滤未知值', () => {
    expect(normalizeAdminRoles(['upstream_admin', 'super_admin', 'upstream_admin'] as AdminRole[]))
      .toEqual(['super_admin', 'upstream_admin'])
    expect(ALL_ADMIN_ROLES).toEqual(['super_admin', 'billing_admin', 'upstream_admin'])
  })
})
