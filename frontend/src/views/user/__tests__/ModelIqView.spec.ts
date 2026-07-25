import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ModelIqView from '@/views/user/ModelIqView.vue'
import type {
  ModelIqComparison,
  ModelIqCurrentResponse,
  ModelIqDayResult,
  ModelIqLatestResult,
} from '@/api/modelIq'

const { getCurrentModelIqMock, refreshCurrentModelIqMock } = vi.hoisted(() => ({
  getCurrentModelIqMock: vi.fn(),
  refreshCurrentModelIqMock: vi.fn(),
}))

vi.mock('@/api/modelIq', () => ({
  getCurrentModelIq: getCurrentModelIqMock,
  refreshCurrentModelIq: refreshCurrentModelIqMock,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      locale: { value: 'en-US' },
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'modelIq.updatedAt') return `Updated ${String(params?.time ?? '')}`
        return key
      },
    }),
  }
})

function day(overrides: Partial<ModelIqDayResult> = {}): ModelIqDayResult {
  return {
    date: '2026-07-14-pm',
    score: 90,
    status: 'green',
    passed: 6,
    tasks: 10,
    invalid: 0,
    total_tokens: 1000,
    input_tokens: 700,
    cached_input_tokens: 100,
    output_tokens: 200,
    wall_seconds: 60,
    wall_time_human: '1 min',
    ...overrides,
  }
}

function latest(overrides: Partial<ModelIqLatestResult> = {}): ModelIqLatestResult {
  return {
    ...day(),
    date: '2026-07-15-pm',
    model: 'gpt-5.6-sol',
    reasoning_effort: 'high',
    valid_tasks: 10,
    cost_usd: 10,
    ...overrides,
  }
}

function comparison(label: string, overrides: Partial<ModelIqLatestResult>): ModelIqComparison {
  const current = latest(overrides)
  return {
    label,
    model: current.model,
    reasoning_effort: current.reasoning_effort,
    latest: current,
    recent_days: [
      day({ date: '2026-07-09-pm', score: current.score - 15 }),
      day({ date: '2026-07-15-pm', score: current.score }),
    ],
  }
}

function currentResponse(
  comparisons: Record<string, ModelIqComparison> = {},
  options: { stale?: boolean } = {},
): ModelIqCurrentResponse {
  return {
    fetched_at: new Date().toISOString(),
    monitored_at: new Date().toISOString(),
    status: 'ok',
    stale: options.stale ?? false,
    model_iq: {
      comparisons,
    },
  }
}

function mountView() {
  return mount(ModelIqView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: true,
        LoadingSpinner: true,
      },
    },
  })
}

describe('ModelIqView', () => {
  beforeEach(() => {
    getCurrentModelIqMock.mockReset()
    refreshCurrentModelIqMock.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('sorts by score, passed tasks, cost, and duration in that order', async () => {
    getCurrentModelIqMock.mockResolvedValue(currentResponse({
      alpha: comparison('Alpha', { score: 100, passed: 8, cost_usd: 20, wall_seconds: 50 }),
      bravo: comparison('Bravo', { score: 100, passed: 8, cost_usd: 10, wall_seconds: 70 }),
      charlie: comparison('Charlie', { score: 100, passed: 8, cost_usd: 10, wall_seconds: 30 }),
      delta: comparison('Delta', { score: 110, passed: 7, cost_usd: 0.00005, wall_seconds: 90 }),
    }))

    const wrapper = mountView()
    await flushPromises()

    const labels = wrapper.findAll('[data-testid="model-iq-row"] [data-testid="model-iq-label"]')
      .map((node) => node.text())
    expect(labels).toEqual(['Delta', 'Charlie', 'Bravo', 'Alpha'])
    expect(wrapper.get('[data-testid="model-iq-desktop-table"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="model-iq-mobile-list"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="model-iq-desktop-table"]').text()).toContain('<$0.0001')
    expect(wrapper.get('[data-testid="model-iq-label"]').attributes('title')).toBe('Delta')
    expect(wrapper.get('[data-testid="model-iq-refresh-note"]').text()).toContain('modelIq.autoRefreshNote')
    expect(getCurrentModelIqMock).toHaveBeenCalledWith(expect.objectContaining({ signal: expect.any(AbortSignal) }))

    wrapper.unmount()
  })

  it('shows the stale notice when the proxy marks the response stale', async () => {
    getCurrentModelIqMock.mockResolvedValue(currentResponse({
      terra: comparison('GPT-5.6 Terra max', { score: 105, passed: 7 }),
    }, { stale: true }))

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="model-iq-stale"]').text()).toContain('modelIq.staleSource')
    wrapper.unmount()
  })

  it('uses the latest measurement time when the upstream monitored time is older', async () => {
    const data = currentResponse({
      sol: comparison('GPT-5.6 Sol high', {
        date: '2026-07-19T14:51:18Z',
        score: 105,
        passed: 7,
      }),
    })
    data.monitored_at = '2026-07-18T11:35:05Z'
    getCurrentModelIqMock.mockResolvedValue(data)

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="model-iq-updated-at"]').text()).toContain('07/19/2026')
    expect(wrapper.get('[data-testid="model-iq-desktop-table"]').text()).not.toContain('2026-07-19T14:51:18Z')
    wrapper.unmount()
  })

  it('keeps the last successful ranking and marks it stale when refresh fails', async () => {
    getCurrentModelIqMock.mockResolvedValueOnce(currentResponse({
      sol: comparison('GPT-5.6 Sol high', { score: 105, passed: 7 }),
    }))
    refreshCurrentModelIqMock.mockRejectedValueOnce({ status: 503 })

    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.findAll('[data-testid="model-iq-row"]')).toHaveLength(1)

    await wrapper.get('[data-testid="model-iq-refresh"]').trigger('click')
    await flushPromises()

    expect(wrapper.findAll('[data-testid="model-iq-row"]')).toHaveLength(1)
    expect(wrapper.get('[data-testid="model-iq-stale"]').text()).toContain('modelIq.staleRefreshFailed')
    expect(wrapper.find('[data-testid="model-iq-error"]').exists()).toBe(false)
    expect(refreshCurrentModelIqMock).toHaveBeenCalledWith(expect.objectContaining({
      signal: expect.any(AbortSignal),
    }))
    expect(getCurrentModelIqMock).toHaveBeenCalledTimes(1)

    wrapper.unmount()
  })

  it('refreshes every hour and clears the timer when unmounted', async () => {
    vi.useFakeTimers()
    getCurrentModelIqMock.mockResolvedValue(currentResponse({
      luna: comparison('GPT-5.6 Luna max', { score: 135, passed: 9 }),
    }))

    const wrapper = mountView()
    await flushPromises()
    expect(getCurrentModelIqMock).toHaveBeenCalledTimes(1)

    await vi.advanceTimersByTimeAsync(60 * 60 * 1000)
    await flushPromises()
    expect(getCurrentModelIqMock).toHaveBeenCalledTimes(2)
    expect(refreshCurrentModelIqMock).not.toHaveBeenCalled()

    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(60 * 60 * 1000)
    expect(getCurrentModelIqMock).toHaveBeenCalledTimes(2)
  })

  it('aborts an in-flight request when unmounted', async () => {
    let requestSignal: AbortSignal | undefined
    getCurrentModelIqMock.mockImplementation(({ signal }: { signal?: AbortSignal }) => {
      requestSignal = signal
      return new Promise<ModelIqCurrentResponse>(() => {})
    })

    const wrapper = mountView()
    await flushPromises()
    expect(wrapper.get('[data-testid="model-iq-loading-skeleton"]').attributes('aria-busy')).toBe('true')
    expect(requestSignal?.aborted).toBe(false)

    wrapper.unmount()
    expect(requestSignal?.aborted).toBe(true)
  })

  it('shows a retryable error when the initial request fails', async () => {
    getCurrentModelIqMock.mockRejectedValue({ response: { status: 429 } })

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="model-iq-error"]').text()).toContain('modelIq.errors.rateLimited')
    expect(wrapper.find('[data-testid="model-iq-desktop-table"]').exists()).toBe(false)

    wrapper.unmount()
  })

  it('shows the network-specific message for Axios network failures', async () => {
    getCurrentModelIqMock.mockRejectedValue({ code: 'ERR_NETWORK' })

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-testid="model-iq-error"]').text()).toContain('modelIq.errors.network')
    wrapper.unmount()
  })
})
