import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const testDirectory = dirname(fileURLToPath(import.meta.url))
const routerSource = readFileSync(resolve(testDirectory, '../index.ts'), 'utf8')
const storeViewSource = readFileSync(resolve(testDirectory, '../../views/user/RedeemCodeStoreView.vue'), 'utf8')

describe('catfk store route', () => {
  it('uses the redeem code store as the authenticated purchase page', () => {
    expect(routerSource).toContain("path: '/purchase'")
    expect(routerSource).toContain("component: () => import('@/views/user/RedeemCodeStoreView.vue')")

    const routeBlock = routerSource.match(/path: '\/purchase',[\s\S]*?\n {2}\},/)
    expect(routeBlock?.[0]).toContain('requiresAuth: true')
    expect(routeBlock?.[0]).toContain('requiresPayment: false')
    expect(routeBlock?.[0]).not.toContain('PaymentView.vue')
  })

  it('redirects the former preview URL to the production purchase page', () => {
    const routeBlock = routerSource.match(/path: '\/purchase\/store-preview'[\s\S]*?\n {2}\},/)
    expect(routeBlock?.[0]).toContain("redirect: '/purchase'")
  })

  it('includes the account redemption entry on the store page', () => {
    expect(storeViewSource).toContain('data-testid="redeem-entry-button"')
    expect(storeViewSource).toContain('@click="goToRedeem"')
    expect(storeViewSource).toContain("将兑换码兑换为 API 额度")
  })
})
