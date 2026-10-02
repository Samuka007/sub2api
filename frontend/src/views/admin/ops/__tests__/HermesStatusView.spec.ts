import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import HermesStatusView from '../HermesStatusView.vue'
import type { OpsHermesStatusResponse } from '@/api/admin/ops'

const { getHermesStatus } = vi.hoisted(() => ({
  getHermesStatus: vi.fn()
}))

vi.mock('@/api/admin/ops', async () => {
  const actual = await vi.importActual<typeof import('@/api/admin/ops')>('@/api/admin/ops')
  return {
    ...actual,
    opsAPI: {
      ...actual.opsAPI,
      getHermesStatus
    }
  }
})

vi.mock('@/utils/format', () => ({
  formatDateTime: (value: string | Date | null | undefined) => (value ? `formatted:${String(value)}` : ''),
  formatNumber: (value: number | null | undefined) => (value == null ? '0' : String(value))
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (params) return `${key}:${Object.values(params).map(String).join(':')}`
        return key
      }
    })
  }
})

const AppLayoutStub = defineComponent({
  template: '<div data-testid="app-layout"><slot /></div>'
})

const RouterLinkStub = defineComponent({
  props: { to: { type: [String, Object], default: '' } },
  template: '<a :href="String(to)"><slot /></a>'
})

function snapshot(overrides: Partial<OpsHermesStatusResponse> = {}): OpsHermesStatusResponse {
  return {
    enabled: true,
    status: 'running',
    healthy: true,
    lifecycle_state: 'running',
    lease_held: true,
    lease_healthy: true,
    started_at: '2026-08-12T00:00:00Z',
    observed_at: '2026-08-12T00:05:00Z',
    last_lease_acquired_at: '2026-08-12T00:00:01Z',
    last_lease_lost_at: null,
    last_reacquired_at: null,
    next_run_at: '2026-08-13T00:00:00Z',
    current_run: null,
    last_run: {
      started_at: '2026-08-12T00:01:00Z',
      completed_at: '2026-08-12T00:01:02Z',
      trigger: 'interval',
      listed: 10,
      checked: 8,
      recovered: 2,
      exhausted: 1,
      unknown: 0,
      cas_misses: 1,
      errors: 0,
      duration_ms: 2000,
      last_error: null
    },
    config: {
      interval_seconds: 86400,
      batch_size: 50,
      concurrency: 3,
      timeout_seconds: 75,
      jitter_seconds: 10
    },
    ...overrides
  }
}

function mountView() {
  return mount(HermesStatusView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        RouterLink: RouterLinkStub
      }
    }
  })
}

describe('HermesStatusView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('loads and renders a healthy worker snapshot on first mount', async () => {
    getHermesStatus.mockResolvedValue(snapshot())

    const wrapper = mountView()
    await flushPromises()

    expect(getHermesStatus).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-testid="hermes-status-badge"]').text()).toContain('admin.ops.hermes.status.healthy')
    expect(wrapper.find('[data-testid="hermes-config-card"]').text()).toContain('86400')
    expect(wrapper.find('[data-testid="hermes-last-run-card"]').text()).toContain('2')
    expect(wrapper.find('[data-testid="hermes-empty"]').exists()).toBe(false)
  })

  it('renders a disabled state without treating it as an error', async () => {
    getHermesStatus.mockResolvedValue(snapshot({ enabled: false, status: 'disabled', healthy: false, lease_held: false, lease_healthy: false }))

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="hermes-status-badge"]').text()).toContain('admin.ops.hermes.status.disabled')
    expect(wrapper.find('[data-testid="hermes-refresh-error"]').exists()).toBe(false)
  })

  it('keeps the last good snapshot visible when a refresh fails', async () => {
    getHermesStatus.mockResolvedValueOnce(snapshot()).mockRejectedValueOnce(new Error('sensitive upstream detail'))

    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-testid="hermes-refresh"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="hermes-last-run-card"]').text()).toContain('2')
    expect(wrapper.find('[data-testid="hermes-refresh-error"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('sensitive upstream detail')
  })

  it('shows a warning when the worker has lost its lease', async () => {
    getHermesStatus.mockResolvedValue(
      snapshot({
        healthy: false,
        status: 'reacquiring',
        lifecycle_state: 'reacquiring',
        lease_held: false,
        lease_healthy: false,
        last_lease_lost_at: '2026-08-12T00:04:00Z',
        last_reacquired_at: '2026-08-12T00:04:30Z'
      })
    )

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="hermes-status-badge"]').text()).toContain('admin.ops.hermes.status.warning')
    expect(wrapper.find('[data-testid="hermes-lease-card"]').text()).toContain('formatted:2026-08-12T00:04:30Z')
  })

  it('does not render green when the backend reports an explicit warning', async () => {
    getHermesStatus.mockResolvedValue(snapshot({ status: 'warning', healthy: true }))

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="hermes-status-badge"]').text()).toContain('admin.ops.hermes.status.warning')
  })
})
