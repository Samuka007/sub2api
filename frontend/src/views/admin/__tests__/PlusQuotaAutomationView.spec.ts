import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'

import PlusQuotaAutomationView from '../PlusQuotaAutomationView.vue'
import type { PlusQuotaAutomationOverview } from '@/api/admin/plusQuotaAutomation'

const {
  getAutomation,
  updateAutomation,
  runAutomation,
  listAnomalies,
  exportAnomalyNotes,
  resolveAnomaly,
  deleteAccount,
  getGroups,
  showError,
  showSuccess,
  showWarning
} = vi.hoisted(() => ({
  getAutomation: vi.fn(),
  updateAutomation: vi.fn(),
  runAutomation: vi.fn(),
  listAnomalies: vi.fn(),
  exportAnomalyNotes: vi.fn(),
  resolveAnomaly: vi.fn(),
  deleteAccount: vi.fn(),
  getGroups: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    plusQuotaAutomation: {
      getAutomation,
      updateAutomation,
      runAutomation,
      listAnomalies,
      exportAnomalyNotes,
      resolveAnomaly,
      deleteAnomalyAccount: deleteAccount
    },
    groups: {
      getAllIncludingInactive: getGroups
    }
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showWarning
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const idleOverview = (): PlusQuotaAutomationOverview => ({
  config: {
    enabled: true,
    group_id: 1,
    interval_seconds: 3600,
    utilization_threshold: 100
  },
  state: {
    running: false,
    trigger: '',
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
})

const runningOverview = (): PlusQuotaAutomationOverview => ({
  ...idleOverview(),
  state: {
    ...idleOverview().state,
    running: true,
    trigger: 'manual',
    started_at: '2026-07-23T08:00:00Z'
  }
})

const AppLayoutStub = { template: '<div><slot /></div>' }
const TablePageLayoutStub = {
  template: `
    <div>
      <slot name="actions" />
      <slot name="filters" />
      <slot name="table" />
      <slot />
      <slot name="pagination" />
    </div>
  `
}
const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.account_id">
        <slot name="cell-actions" :row="row" />
      </div>
    </div>
  `
}
const ConfirmDialogStub = defineComponent({
  props: {
    show: {
      type: Boolean,
      default: false
    },
    title: String,
    message: String,
    confirmText: String,
    danger: Boolean
  },
  emits: ['confirm', 'cancel'],
  template: `
    <button
      v-if="show"
      data-test="confirm-dialog"
      :data-title="title"
      :data-message="message"
      :data-danger="String(danger)"
      @click="$emit('confirm')"
    >
      {{ confirmText }}
    </button>
  `
})

function mountView(): VueWrapper {
  return mount(PlusQuotaAutomationView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        ConfirmDialog: ConfirmDialogStub,
        DataTable: DataTableStub,
        Pagination: true,
        SearchInput: true,
        Select: true,
        Toggle: true,
        Icon: true
      }
    }
  })
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
}

function findButtonByText(wrapper: VueWrapper, text: string) {
  const button = wrapper.findAll('button').find((item) => item.text().includes(text))
  if (!button) throw new Error(`button not found: ${text}`)
  return button
}

describe('admin PlusQuotaAutomationView', () => {
  beforeEach(() => {
    getAutomation.mockReset()
    updateAutomation.mockReset()
    runAutomation.mockReset()
    listAnomalies.mockReset()
    exportAnomalyNotes.mockReset()
    resolveAnomaly.mockReset()
    deleteAccount.mockReset()
    getGroups.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    showWarning.mockReset()

    getAutomation.mockResolvedValue(idleOverview())
    getGroups.mockResolvedValue([{
      id: 1,
      name: 'Plus',
      platform: 'openai',
      status: 'active'
    }])
    listAnomalies.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20
    })
    exportAnomalyNotes.mockResolvedValue({ blob: null, count: 0, filename: null })
  })

  it('downloads the complete server snapshot as one note per line in a TXT file', async () => {
    const wrapper = mountView()
    await flushPromises()
    const exportBlob = new Blob(
      ['\uFEFFFirst account note\nLine 1 Line 2 Line 3 Line 4\nLast account note'],
      { type: 'text/plain;charset=utf-8' }
    )
    exportAnomalyNotes.mockResolvedValueOnce({
      blob: exportBlob,
      count: 3,
      filename: 'server-account-notes.txt'
    })

    let downloadedFilename = ''
    const originalCreateObjectURL = window.URL.createObjectURL
    const originalRevokeObjectURL = window.URL.revokeObjectURL
    window.URL.createObjectURL = vi.fn(() => 'blob:account-notes') as typeof window.URL.createObjectURL
    window.URL.revokeObjectURL = vi.fn(() => {}) as typeof window.URL.revokeObjectURL
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function () {
      downloadedFilename = this.download
    })

    try {
      await wrapper.get('[data-test="export-account-notes"]').trigger('click')
      await flushPromises()

      expect(exportAnomalyNotes).toHaveBeenCalledWith(
        expect.objectContaining({ signal: expect.any(AbortSignal) })
      )
      expect(window.URL.createObjectURL).toHaveBeenCalledWith(exportBlob)
      expect(downloadedFilename).toBe('server-account-notes.txt')
      expect(clickSpy).toHaveBeenCalledTimes(1)
      expect(window.URL.revokeObjectURL).toHaveBeenCalledWith('blob:account-notes')
      expect(showSuccess).toHaveBeenCalledWith(
        'admin.plusQuotaAutomation.messages.accountNotesExported'
      )
    } finally {
      wrapper.unmount()
      window.URL.createObjectURL = originalCreateObjectURL
      window.URL.revokeObjectURL = originalRevokeObjectURL
      clickSpy.mockRestore()
    }
  })

  it('does not download when open anomalies have no non-empty notes', async () => {
    getAutomation.mockResolvedValue({
      ...idleOverview(),
      config: { ...idleOverview().config, group_id: 0 }
    })
    const wrapper = mountView()
    await flushPromises()
    exportAnomalyNotes.mockResolvedValueOnce({ blob: null, count: 0, filename: null })
    const originalCreateObjectURL = window.URL.createObjectURL
    window.URL.createObjectURL = vi.fn(() => 'blob:should-not-exist') as typeof window.URL.createObjectURL

    try {
      const exportButton = wrapper.get('[data-test="export-account-notes"]')
      expect(exportButton.attributes('disabled')).toBeUndefined()
      await exportButton.trigger('click')
      await flushPromises()

      expect(showWarning).toHaveBeenCalledWith(
        'admin.plusQuotaAutomation.messages.noAccountNotesToExport'
      )
      expect(window.URL.createObjectURL).not.toHaveBeenCalled()
      expect(showSuccess).not.toHaveBeenCalled()
    } finally {
      wrapper.unmount()
      window.URL.createObjectURL = originalCreateObjectURL
    }
  })

  it('reports account note export failures', async () => {
    const wrapper = mountView()
    await flushPromises()
    exportAnomalyNotes.mockRejectedValueOnce(new Error('account notes unavailable'))

    await wrapper.get('[data-test="export-account-notes"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('account notes unavailable')
    expect(showSuccess).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('shows the dedicated busy message when a manual run returns 409', async () => {
    runAutomation.mockRejectedValue({ status: 409 })
    const wrapper = mountView()
    await flushPromises()

    await findButtonByText(
      wrapper,
      'admin.plusQuotaAutomation.actions.runNow'
    ).trigger('click')
    await wrapper.get('[data-test="confirm-dialog"]').trigger('click')
    await flushPromises()

    expect(runAutomation).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('admin.plusQuotaAutomation.messages.runBusy')
    expect(showSuccess).not.toHaveBeenCalled()

    wrapper.unmount()
  })

  it('disables manual runs until a saved target group is available', async () => {
    getAutomation.mockResolvedValue({
      ...idleOverview(),
      config: {
        ...idleOverview().config,
        group_id: 0
      }
    })

    const wrapper = mountView()
    await flushPromises()

    const runButton = findButtonByText(
      wrapper,
      'admin.plusQuotaAutomation.actions.runNow'
    )
    expect(runButton.attributes('disabled')).toBeDefined()
    await runButton.trigger('click')
    expect(wrapper.find('[data-test="confirm-dialog"]').exists()).toBe(false)

    wrapper.unmount()
  })

  it('schedules another status poll after a silent polling failure', async () => {
    getAutomation
      .mockResolvedValueOnce(runningOverview())
      .mockRejectedValueOnce(new Error('temporary network failure'))

    const pollCallbacks: Array<() => Promise<void>> = []
    const setTimeoutSpy = vi
      .spyOn(window, 'setTimeout')
      .mockImplementation((callback: TimerHandler) => {
        pollCallbacks.push(callback as () => Promise<void>)
        return 1
      })

    const wrapper = mountView()
    await flushPromises()
    expect(pollCallbacks).toHaveLength(1)

    const firstPoll = pollCallbacks.shift()
    expect(firstPoll).toBeDefined()
    await firstPoll?.()
    await flushPromises()

    expect(getAutomation).toHaveBeenCalledTimes(2)
    expect(pollCallbacks).toHaveLength(1)
    expect(showError).not.toHaveBeenCalled()

    wrapper.unmount()
    setTimeoutSpy.mockRestore()
  })

  it('keeps polling when a failed refresh supersedes an in-flight silent poll', async () => {
    const silentOverview = deferred<PlusQuotaAutomationOverview>()
    getAutomation
      .mockResolvedValueOnce(runningOverview())
      .mockReturnValueOnce(silentOverview.promise)
      .mockRejectedValueOnce(new Error('manual refresh failed'))

    const pollCallbacks: Array<() => Promise<void>> = []
    const setTimeoutSpy = vi
      .spyOn(window, 'setTimeout')
      .mockImplementation((callback: TimerHandler) => {
        pollCallbacks.push(callback as () => Promise<void>)
        return 1
      })

    const wrapper = mountView()
    await flushPromises()

    const pollPromise = pollCallbacks.shift()?.()
    await flushPromises()
    await wrapper.get('button[title="common.refresh"]').trigger('click')
    await flushPromises()

    expect(getAutomation).toHaveBeenCalledTimes(3)
    expect(showError).toHaveBeenCalledWith('manual refresh failed')
    expect(pollCallbacks).toHaveLength(1)

    silentOverview.resolve(runningOverview())
    await pollPromise
    await flushPromises()
    expect(pollCallbacks).toHaveLength(1)

    wrapper.unmount()
    setTimeoutSpy.mockRestore()
  })

  it('does not restart polling when an in-flight overview request finishes after unmount', async () => {
    const pendingOverview = deferred<PlusQuotaAutomationOverview>()
    getAutomation.mockReturnValue(pendingOverview.promise)
    const setTimeoutSpy = vi.spyOn(window, 'setTimeout')

    const wrapper = mountView()
    await flushPromises()
    wrapper.unmount()

    pendingOverview.resolve(runningOverview())
    await flushPromises()

    expect(setTimeoutSpy).not.toHaveBeenCalled()
    setTimeoutSpy.mockRestore()
  })

  it('clears foreground loading when a silent poll supersedes a manual refresh', async () => {
    const foregroundOverview = deferred<PlusQuotaAutomationOverview>()
    const silentOverview = deferred<PlusQuotaAutomationOverview>()
    getAutomation
      .mockResolvedValueOnce(runningOverview())
      .mockReturnValueOnce(foregroundOverview.promise)
      .mockReturnValueOnce(silentOverview.promise)

    const pollCallbacks: Array<() => Promise<void>> = []
    const setTimeoutSpy = vi
      .spyOn(window, 'setTimeout')
      .mockImplementation((callback: TimerHandler) => {
        pollCallbacks.push(callback as () => Promise<void>)
        return 1
      })

    const wrapper = mountView()
    await flushPromises()

    const refreshButton = wrapper.get('button[title="common.refresh"]')
    await refreshButton.trigger('click')
    await flushPromises()
    expect(refreshButton.attributes('disabled')).toBeDefined()

    const pollPromise = pollCallbacks.shift()?.()
    await flushPromises()
    foregroundOverview.resolve(runningOverview())
    await flushPromises()

    expect(refreshButton.attributes('disabled')).toBeUndefined()

    silentOverview.resolve(idleOverview())
    await pollPromise
    await flushPromises()

    wrapper.unmount()
    setTimeoutSpy.mockRestore()
  })

  it('syncs the config form after the initial overview request recovers', async () => {
    getAutomation
      .mockRejectedValueOnce(new Error('initial overview failed'))
      .mockResolvedValueOnce(idleOverview())

    const wrapper = mountView()
    await flushPromises()

    expect(findButtonByText(wrapper, 'common.save').attributes('disabled')).toBeDefined()

    await wrapper.get('button[title="common.refresh"]').trigger('click')
    await flushPromises()

    expect(findButtonByText(wrapper, 'common.save').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  it('loads groups without waiting for the anomaly list', async () => {
    const pendingAnomalies = deferred<{
      items: []
      total: number
      page: number
      page_size: number
    }>()
    listAnomalies.mockReturnValueOnce(pendingAnomalies.promise)

    const wrapper = mountView()
    await flushPromises()

    expect(getGroups).toHaveBeenCalledWith('openai')

    wrapper.unmount()
    pendingAnomalies.resolve({ items: [], total: 0, page: 1, page_size: 20 })
    await flushPromises()
  })

  it('does not report a group loading failure after unmount', async () => {
    const pendingGroups = deferred<never[]>()
    getGroups.mockReturnValueOnce(pendingGroups.promise)

    const wrapper = mountView()
    await flushPromises()
    expect(getGroups).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    pendingGroups.reject(new Error('late group failure'))
    await flushPromises()

    expect(showError).not.toHaveBeenCalled()
  })

  it('disables an inactive configured group until the automation is turned off or retargeted', async () => {
    getGroups.mockResolvedValue([{
      id: 1,
      name: 'Retired Plus',
      platform: 'openai',
      status: 'inactive'
    }])

    const wrapper = mountView()
    await flushPromises()

    expect(findButtonByText(wrapper, 'common.save').attributes('disabled')).toBeDefined()
    expect(
      findButtonByText(wrapper, 'admin.plusQuotaAutomation.actions.runNow').attributes('disabled')
    ).toBeDefined()

    wrapper.unmount()
  })

  it('returns to the last valid page after resolving its final anomaly', async () => {
    const anomaly = {
      account_id: 42,
      account_name: 'plus-forty-two',
      email: 'plus42@example.com',
      group_id: 1,
      stage: 'query',
      http_status: 401,
      first_detected_at: '2026-07-23T08:00:00Z',
      last_detected_at: '2026-07-23T08:00:00Z',
      count: 1,
      status: 'open' as const,
      last_error: 'unauthorized'
    }
    listAnomalies
      .mockResolvedValueOnce({ items: [anomaly], total: 21, page: 2, page_size: 20 })
      .mockResolvedValueOnce({ items: [], total: 20, page: 2, page_size: 20 })
      .mockResolvedValueOnce({ items: [], total: 20, page: 1, page_size: 20 })
    resolveAnomaly.mockResolvedValue(undefined)

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('button[title="admin.plusQuotaAutomation.actions.resolve"]').trigger('click')
    await flushPromises()

    expect(resolveAnomaly).toHaveBeenCalledWith(42)
    expect(listAnomalies).toHaveBeenNthCalledWith(
      3,
      expect.objectContaining({ page: 1, page_size: 20 }),
      expect.any(Object)
    )

    wrapper.unmount()
  })

  it('returns to a valid page when another resolve supersedes the list refresh', async () => {
    const anomaly = {
      account_id: 42,
      account_name: 'plus-forty-two',
      email: 'plus42@example.com',
      group_id: 1,
      stage: 'query',
      http_status: 401,
      first_detected_at: '2026-07-23T08:00:00Z',
      last_detected_at: '2026-07-23T08:00:00Z',
      count: 1,
      status: 'open' as const,
      last_error: 'unauthorized'
    }
    const secondAnomaly = {
      ...anomaly,
      account_id: 43,
      account_name: 'plus-forty-three',
      email: 'plus43@example.com'
    }
    const resolveRefresh = deferred<{
      items: []
      total: number
      page: number
      page_size: number
    }>()
    const concurrentRefresh = deferred<{
      items: []
      total: number
      page: number
      page_size: number
    }>()
    listAnomalies
      .mockResolvedValueOnce({ items: [anomaly, secondAnomaly], total: 22, page: 2, page_size: 20 })
      .mockReturnValueOnce(resolveRefresh.promise)
      .mockReturnValueOnce(concurrentRefresh.promise)
      .mockResolvedValueOnce({ items: [], total: 20, page: 1, page_size: 20 })
    resolveAnomaly.mockResolvedValue(undefined)

    const wrapper = mountView()
    await flushPromises()

    const resolveButtons = wrapper.findAll(
      'button[title="admin.plusQuotaAutomation.actions.resolve"]'
    )
    expect(resolveButtons).toHaveLength(2)
    await resolveButtons[0].trigger('click')
    await flushPromises()
    await resolveButtons[1].trigger('click')
    await flushPromises()

    resolveRefresh.resolve({ items: [], total: 20, page: 2, page_size: 20 })
    concurrentRefresh.resolve({ items: [], total: 20, page: 2, page_size: 20 })
    await flushPromises()

    expect(listAnomalies).toHaveBeenNthCalledWith(
      4,
      expect.objectContaining({ page: 1, page_size: 20 }),
      expect.any(Object)
    )

    wrapper.unmount()
  })

  it('requires destructive confirmation, disables the row, and refreshes anomalies and counts after deletion', async () => {
    const anomaly = {
      account_id: 42,
      account_name: 'plus-forty-two',
      email: 'plus42@example.com',
      group_id: 1,
      stage: 'query',
      http_status: 401,
      first_detected_at: '2026-07-23T08:00:00Z',
      last_detected_at: '2026-07-23T08:00:00Z',
      count: 1,
      status: 'open' as const,
      last_error: 'unauthorized'
    }
    const deletion = deferred<{ message: string }>()
    listAnomalies
      .mockResolvedValueOnce({ items: [anomaly], total: 1, page: 1, page_size: 20 })
      .mockResolvedValueOnce({ items: [], total: 0, page: 1, page_size: 20 })
    deleteAccount.mockReturnValueOnce(deletion.promise)

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="delete-anomaly-account"]').trigger('click')
    expect(deleteAccount).not.toHaveBeenCalled()

    const confirmButton = wrapper.get('[data-test="confirm-dialog"]')
    expect(confirmButton.attributes('data-title')).toBe(
      'admin.plusQuotaAutomation.deleteConfirm.title'
    )
    expect(confirmButton.attributes('data-message')).toBe(
      'admin.plusQuotaAutomation.deleteConfirm.message'
    )
    expect(confirmButton.attributes('data-danger')).toBe('true')

    await confirmButton.trigger('click')
    await flushPromises()

    expect(deleteAccount).toHaveBeenCalledWith(42)
    expect(wrapper.get('[data-test="delete-anomaly-account"]').attributes('disabled')).toBeDefined()
    expect(
      wrapper.get('button[title="admin.plusQuotaAutomation.actions.resolve"]').attributes('disabled')
    ).toBeDefined()
    expect(listAnomalies).toHaveBeenCalledTimes(1)
    expect(getAutomation).toHaveBeenCalledTimes(1)

    deletion.resolve({ message: 'ok' })
    await flushPromises()

    expect(showSuccess).toHaveBeenCalledWith(
      'admin.plusQuotaAutomation.messages.accountDeleted'
    )
    expect(listAnomalies).toHaveBeenCalledTimes(2)
    expect(getAutomation).toHaveBeenCalledTimes(2)

    wrapper.unmount()
  })

  it('only shows account deletion for open HTTP 401 anomalies', async () => {
    const eligible = {
      account_id: 42,
      account_name: 'eligible-plus-account',
      email: 'eligible@example.com',
      group_id: 1,
      stage: 'query',
      http_status: 401,
      first_detected_at: '2026-07-23T08:00:00Z',
      last_detected_at: '2026-07-23T08:00:00Z',
      count: 1,
      status: 'open' as const,
      last_error: 'unauthorized'
    }
    listAnomalies.mockResolvedValueOnce({
      items: [
        eligible,
        { ...eligible, account_id: 43, status: 'resolved' as const },
        { ...eligible, account_id: 44, http_status: 403 }
      ],
      total: 3,
      page: 1,
      page_size: 20
    })

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.findAll('[data-test="delete-anomaly-account"]')).toHaveLength(1)

    wrapper.unmount()
  })

  it('keeps the anomaly actionable and reports the API error when deletion fails', async () => {
    const anomaly = {
      account_id: 42,
      account_name: 'plus-forty-two',
      email: 'plus42@example.com',
      group_id: 1,
      stage: 'query',
      http_status: 401,
      first_detected_at: '2026-07-23T08:00:00Z',
      last_detected_at: '2026-07-23T08:00:00Z',
      count: 1,
      status: 'open' as const,
      last_error: 'unauthorized'
    }
    listAnomalies
      .mockResolvedValueOnce({
        items: [anomaly],
        total: 1,
        page: 1,
        page_size: 20
      })
      .mockResolvedValueOnce({
        items: [anomaly],
        total: 1,
        page: 1,
        page_size: 20
      })
    deleteAccount.mockRejectedValueOnce({
      response: { data: { detail: 'Cannot delete this account' } }
    })

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="delete-anomaly-account"]').trigger('click')
    await wrapper.get('[data-test="confirm-dialog"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('Cannot delete this account')
    expect(showSuccess).not.toHaveBeenCalled()
    expect(listAnomalies).toHaveBeenCalledTimes(2)
    expect(getAutomation).toHaveBeenCalledTimes(1)
    expect(wrapper.get('[data-test="delete-anomaly-account"]').attributes('disabled')).toBeUndefined()

    wrapper.unmount()
  })

  it('warns and refreshes when the server rejects a stale anomaly deletion', async () => {
    const anomaly = {
      account_id: 42,
      account_name: 'plus-forty-two',
      email: 'plus42@example.com',
      group_id: 1,
      stage: 'query',
      http_status: 401,
      first_detected_at: '2026-07-23T08:00:00Z',
      last_detected_at: '2026-07-23T08:00:00Z',
      count: 1,
      status: 'open' as const,
      last_error: 'unauthorized'
    }
    listAnomalies
      .mockResolvedValueOnce({ items: [anomaly], total: 1, page: 1, page_size: 20 })
      .mockResolvedValueOnce({ items: [], total: 0, page: 1, page_size: 20 })
    deleteAccount.mockRejectedValueOnce({
      status: 409,
      message: 'account is no longer an open Plus quota HTTP 401 anomaly'
    })

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="delete-anomaly-account"]').trigger('click')
    await wrapper.get('[data-test="confirm-dialog"]').trigger('click')
    await flushPromises()

    expect(deleteAccount).toHaveBeenCalledWith(42)
    expect(showWarning).toHaveBeenCalledWith(
      'admin.plusQuotaAutomation.messages.deleteAccountNoLongerEligible'
    )
    expect(listAnomalies).toHaveBeenCalledTimes(2)

    wrapper.unmount()
  })
})
