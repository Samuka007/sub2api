import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get } }))

import { listAccountHealthCandidates } from '@/api/admin/groups'

describe('admin group account health candidates API', () => {
  beforeEach(() => {
    get.mockReset()
  })

  it('lists detector candidates from the non-sensitive endpoint', async () => {
    const controller = new AbortController()
    const result = {
      items: [{ id: 42, name: 'OpenAI group', platform: 'openai', status: 'active' }],
      total: 1,
      page: 1,
      page_size: 100,
      pages: 1
    }
    get.mockResolvedValue({ data: result })

    await expect(
      listAccountHealthCandidates(1, 100, { signal: controller.signal })
    ).resolves.toBe(result)
    expect(get).toHaveBeenCalledWith('/admin/groups/account-health-candidates', {
      params: { page: 1, page_size: 100 },
      signal: controller.signal
    })
  })
})
