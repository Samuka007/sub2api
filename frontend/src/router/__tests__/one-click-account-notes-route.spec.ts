import { describe, expect, it } from 'vitest'

import router from '@/router'

describe('one-click account notes route', () => {
  it('registers a lazy-loaded authenticated administrator page', () => {
    const route = router.getRoutes().find(({ name }) => name === 'AdminOneClickAccountNotes')

    expect(route).toBeDefined()
    expect(route?.path).toBe('/admin/one-click-account-notes')
    expect(route?.meta).toEqual(expect.objectContaining({
      requiresAuth: true,
      requiresAdmin: true,
      titleKey: 'admin.oneClickAccountNotes.title',
      descriptionKey: 'admin.oneClickAccountNotes.description'
    }))
    expect(typeof route?.components?.default).toBe('function')
  })
})
