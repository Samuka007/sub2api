import { describe, expect, it } from 'vitest'

import router from '@/router'

describe('account health detector route', () => {
  it('registers an authenticated administrator page', () => {
    const route = router.getRoutes().find(({ name }) => name === 'AdminAccountHealthDetector')

    expect(route).toBeDefined()
    expect(route?.path).toBe('/admin/account-health-detector')
    expect(route?.meta).toEqual(expect.objectContaining({
      requiresAuth: true,
      requiresAdmin: true,
      titleKey: 'admin.accountHealthDetector.title',
      descriptionKey: 'admin.accountHealthDetector.description'
    }))
    expect(typeof route?.components?.default).toBe('function')
  })
})
