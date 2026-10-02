import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const testDirectory = dirname(fileURLToPath(import.meta.url))
const routerSource = readFileSync(resolve(testDirectory, '../index.ts'), 'utf8')
const sidebarSource = readFileSync(resolve(testDirectory, '../../components/layout/AppSidebar.vue'), 'utf8')

describe('Plus quota automation navigation', () => {
  it('registers a lazy-loaded admin-only route', () => {
    expect(routerSource).toContain("path: '/admin/plus-quota-automation'")
    expect(routerSource).toContain("name: 'AdminPlusQuotaAutomation'")
    expect(routerSource).toContain(
      "component: () => import('@/views/admin/PlusQuotaAutomationView.vue')"
    )

    const routeBlock = routerSource.match(
      /path: '\/admin\/plus-quota-automation'[\s\S]*?\n {2}\},/
    )
    expect(routeBlock?.[0]).toContain('requiresAuth: true')
    expect(routeBlock?.[0]).toContain('requiresAdmin: true')
    expect(routeBlock?.[0]).toContain("titleKey: 'admin.plusQuotaAutomation.title'")
    expect(routeBlock?.[0]).toContain(
      "descriptionKey: 'admin.plusQuotaAutomation.description'"
    )
  })

  it('places the menu entry immediately after account management', () => {
    expect(sidebarSource).toMatch(
      /\{ path: '\/admin\/accounts',[^\n]+\n\s*\{ path: '\/admin\/plus-quota-automation', label: t\('nav\.plusQuotaAutomation'\), icon: ChartIcon(?:, permission: '[^']+')? \}/
    )
  })
})
