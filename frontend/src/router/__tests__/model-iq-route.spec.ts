import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const testDirectory = dirname(fileURLToPath(import.meta.url))
const routerSource = readFileSync(resolve(testDirectory, '../index.ts'), 'utf8')
const sidebarSource = readFileSync(resolve(testDirectory, '../../components/layout/AppSidebar.vue'), 'utf8')

describe('model IQ navigation', () => {
  it('registers an authenticated non-admin route', () => {
    expect(routerSource).toContain("path: '/model-iq'")
    expect(routerSource).toContain("name: 'ModelIq'")
    expect(routerSource).toContain("component: () => import('@/views/user/ModelIqView.vue')")

    const routeBlock = routerSource.match(/path: '\/model-iq'[\s\S]*?\n {2}\},/)
    expect(routeBlock?.[0]).toContain('requiresAuth: true')
    expect(routeBlock?.[0]).toContain('requiresAdmin: false')
    expect(routeBlock?.[0]).toContain("titleKey: 'modelIq.title'")
  })

  it('places the ChartIcon entry immediately after profile navigation', () => {
    expect(sidebarSource).toMatch(
      /\{ path: '\/profile',[^\n]+\n\s*\{ path: '\/model-iq', label: t\('nav\.modelIq'\), icon: ChartIcon \}/,
    )
  })
})
