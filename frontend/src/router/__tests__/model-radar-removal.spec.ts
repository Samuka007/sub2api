import { describe, expect, it } from 'vitest'
import router from '../index'

describe('model radar removal', () => {
  it('resolves the removed admin path to the not-found route', () => {
    expect(router.resolve('/admin/model-radar').name).toBe('NotFound')
  })
})
