import { beforeEach, describe, expect, it, vi } from 'vitest'

const { get, post, put } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn()
}))

vi.mock('@/api/client', () => ({
  apiClient: { get, post, put }
}))

import {
  getAutomation,
  listAnomalies,
  resolveAnomaly,
  runAutomation,
  updateAutomation
} from '@/api/admin/plusQuotaAutomation'

describe('admin Plus quota automation API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    put.mockReset()
  })

  it('reads and updates automation configuration', async () => {
    const overview = {
      config: {
        enabled: true,
        group_id: 12,
        interval_seconds: 900,
        utilization_threshold: 100
      },
      state: {
        running: false,
        trigger: 'scheduled',
        scanned: 5,
        eligible: 4,
        at_limit: 1,
        reset_count: 1,
        unauthorized: 0,
        failed: 0,
        no_credits: 0,
        cooldown: 0,
        skipped: 1
      }
    }

    get.mockResolvedValueOnce({ data: overview })
    put.mockResolvedValueOnce({ data: overview })

    await expect(getAutomation()).resolves.toEqual(overview)
    await expect(updateAutomation(overview.config)).resolves.toBeUndefined()

    expect(get).toHaveBeenCalledWith('/admin/openai/plus-quota-automation')
    expect(put).toHaveBeenCalledWith('/admin/openai/plus-quota-automation', overview.config)
  })

  it('runs the task and preserves a busy 409 for the view to handle', async () => {
    const overview = {
      config: {
        enabled: true,
        group_id: 12,
        interval_seconds: 900,
        utilization_threshold: 100
      },
      state: {
        running: true,
        trigger: 'manual',
        scanned: 0,
        eligible: 0,
        at_limit: 0,
        reset_count: 0,
        unauthorized: 0,
        failed: 0,
        no_credits: 0,
        cooldown: 0,
        skipped: 0
      }
    }

    post.mockResolvedValueOnce({ data: overview })
    await expect(runAutomation()).resolves.toEqual(overview)
    expect(post).toHaveBeenNthCalledWith(1, '/admin/openai/plus-quota-automation/run')

    const busy = { status: 409, code: 'PLUS_QUOTA_AUTOMATION_RUNNING' }
    post.mockRejectedValueOnce(busy)
    await expect(runAutomation()).rejects.toEqual(busy)
  })

  it('lists and resolves 401 anomalies through dedicated endpoints', async () => {
    const response = {
      items: [],
      total: 0,
      page: 2,
      page_size: 50
    }
    const controller = new AbortController()
    get.mockResolvedValueOnce({ data: response })
    post.mockResolvedValueOnce({ data: undefined })

    await expect(
      listAnomalies(
        { page: 2, page_size: 50, status: 'open', search: 'owner@example.com' },
        { signal: controller.signal }
      )
    ).resolves.toEqual(response)
    await expect(resolveAnomaly(42)).resolves.toBeUndefined()

    expect(get).toHaveBeenCalledWith('/admin/openai/plus-quota-anomalies', {
      params: {
        page: 2,
        page_size: 50,
        status: 'open',
        search: 'owner@example.com'
      },
      signal: controller.signal
    })
    expect(post).toHaveBeenCalledWith('/admin/openai/plus-quota-anomalies/42/resolve')
  })
})
