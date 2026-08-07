import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post } = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn() }))

vi.mock('@/api/client', () => ({ apiClient: { get, post } }))

import {
  batchDelete,
  detectAccountHealth,
  listAccountHealthCandidates
} from '@/api/admin/accounts'

describe('admin account health detection API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  it('lists accounts for the selected groups without sending group metadata in a body', async () => {
    const controller = new AbortController()
    const result = {
      items: [{
        id: 42,
        name: 'existing@example.com',
        platform: 'openai',
        type: 'oauth',
        status: 'active',
        group_id: 1,
        group_name: 'OpenAI',
        group_ids: [1, 2],
        group_names: ['OpenAI', 'Backup']
      }],
      total: 1,
      page: 1,
      page_size: 100,
      pages: 1
    }
    get.mockResolvedValue({ data: result })

    await expect(
      listAccountHealthCandidates(1, 100, [1, 2], { signal: controller.signal })
    ).resolves.toBe(result)
    expect(get).toHaveBeenCalledWith('/admin/accounts/account-health-candidates', {
      params: { page: 1, page_size: 100, group_ids: '1,2' },
      signal: controller.signal
    })
  })

  it('posts the account and selected group IDs and forwards cancellation', async () => {
    const controller = new AbortController()
    const result = {
      account_id: 42,
      account_name: 'existing@example.com',
      group_id: 1,
      group_name: 'OpenAI',
      status: 'no_evidence'
    }
    post.mockResolvedValue({ data: result })

    await expect(detectAccountHealth(42, 1, { signal: controller.signal })).resolves.toBe(result)
    expect(post).toHaveBeenCalledWith(
      '/admin/accounts/42/account-health-detection',
      { group_id: 1 },
      { signal: controller.signal, timeout: 65_000 }
    )
  })

  it('batch deletes the selected account IDs', async () => {
    const result = {
      success: 2,
      failed: 0,
      success_ids: [42, 43],
      failed_ids: [],
      results: []
    }
    post.mockResolvedValue({ data: result })

    await expect(batchDelete([42, 43])).resolves.toBe(result)
    expect(post).toHaveBeenCalledWith('/admin/accounts/batch-delete', {
      account_ids: [42, 43]
    })
  })
})
