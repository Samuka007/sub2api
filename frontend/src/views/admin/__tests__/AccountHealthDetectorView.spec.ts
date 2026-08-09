import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import type { AccountHealthCandidate, AccountHealthDetectionResult } from '@/api/admin/accounts'
import type { GroupAccountHealthCandidate } from '@/api/admin/groups'
import AccountHealthDetectorView from '../AccountHealthDetectorView.vue'

const {
  listGroups,
  listAccounts,
  detectAccountHealth,
  exportAccountNotes,
  batchDelete,
  showError,
  showWarning,
  showSuccess
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  listAccounts: vi.fn(),
  detectAccountHealth: vi.fn(),
  exportAccountNotes: vi.fn(),
  batchDelete: vi.fn(),
  showError: vi.fn(),
  showWarning: vi.fn(),
  showSuccess: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: { listAccountHealthCandidates: listGroups },
    accounts: {
      listAccountHealthCandidates: listAccounts,
      detectAccountHealth,
      exportAccountNotes,
      batchDelete
    }
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError, showWarning, showSuccess })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const vue = await vi.importActual<typeof import('vue')>('vue')
  const locale = vue.ref('zh')
  return {
    ...actual,
    useI18n: () => ({
      locale,
      t: (key: string, params?: Record<string, unknown>) => params
        ? `${key}:${JSON.stringify(params)}`
        : key
    })
  }
})

const AppLayoutStub = { template: '<div><slot /></div>' }
const TablePageLayoutStub = {
  template: `
    <main>
      <slot name="actions" />
      <slot name="filters" />
      <slot name="table" />
      <slot name="pagination" />
    </main>
  `
}

const SearchInputStub = defineComponent({
  inheritAttrs: false,
  props: {
    modelValue: { type: String, default: '' },
    disabled: { type: Boolean, default: false }
  },
  emits: ['update:modelValue'],
  setup(_props, { emit }) {
    return { onInput: (event: Event) => emit('update:modelValue', (event.target as HTMLInputElement).value) }
  },
  template: '<div v-bind="$attrs"><input :value="modelValue" :disabled="disabled" @input="onInput" /></div>'
})

const SelectStub = defineComponent({
  inheritAttrs: false,
  props: {
    modelValue: { type: [String, Number, Boolean], default: '' },
    options: { type: Array, default: () => [] },
    disabled: { type: Boolean, default: false }
  },
  emits: ['update:modelValue'],
  setup(_props, { emit }) {
    return { onChange: (event: Event) => emit('update:modelValue', (event.target as HTMLSelectElement).value) }
  },
  template: `
    <select v-bind="$attrs" :value="modelValue" :disabled="disabled" @change="onChange">
      <option v-for="option in options" :key="option.value" :value="option.value">{{ option.label }}</option>
    </select>
  `
})

const DataTableStub = defineComponent({
  props: {
    columns: { type: Array, default: () => [] },
    data: { type: Array, default: () => [] },
    selectedKeys: { type: Array, default: () => [] },
    selectionDisabled: { type: Boolean, default: false }
  },
  emits: ['sort', 'update:selectedKeys'],
  template: `
    <section data-test="account-table">
      <span data-test="selected-keys">{{ selectedKeys.join(',') }}</span>
      <button
        v-if="data.length"
        type="button"
        data-test="select-first-account"
        :disabled="selectionDisabled"
        @click="$emit('update:selectedKeys', [data[0].id])"
      >select</button>
      <article v-for="row in data" :key="row.id" :data-test="'row-' + row.id">
        <slot name="cell-account_name" :row="row" />
        <slot name="cell-group_name" :row="row" />
        <slot name="cell-local_status" :value="row.local_status" />
        <slot name="cell-outcome" :row="row" />
        <slot name="cell-ban_status" :value="row.ban_status" />
        <slot name="cell-plus_status" :row="row" />
        <slot name="cell-plus_date" :value="row.plus_date" />
        <slot name="cell-payment_method" :value="row.payment_method" />
        <slot name="cell-ban_date" :value="row.ban_date" />
        <slot name="cell-lifespan" :row="row" />
        <slot name="cell-score" :value="row.score" />
        <slot name="cell-message_page_count" :row="row" />
        <slot name="cell-evidence_summary" :value="row.evidence_summary" />
        <slot name="cell-elapsed_ms" :value="row.elapsed_ms" />
        <slot name="cell-checked_at" :row="row" />
        <slot name="cell-actions" :row="row" />
      </article>
      <slot v-if="data.length === 0" name="empty" />
    </section>
  `
})

const ConfirmDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
    title: { type: String, default: '' },
    message: { type: String, default: '' }
  },
  emits: ['confirm', 'cancel'],
  template: `
    <section v-if="show" data-test="confirm-dialog">
      <span>{{ title }}</span><span>{{ message }}</span>
      <button type="button" data-test="confirm-delete" @click="$emit('confirm')">confirm</button>
      <button type="button" data-test="cancel-delete" @click="$emit('cancel')">cancel</button>
    </section>
  `
})

const PaginationStub = { template: '<nav data-test="pagination" />' }

function group(overrides: Partial<GroupAccountHealthCandidate> = {}): GroupAccountHealthCandidate {
  return { id: 1, name: 'OpenAI primary', platform: 'openai', status: 'active', ...overrides }
}

function account(overrides: Partial<AccountHealthCandidate> = {}): AccountHealthCandidate {
  return {
    id: 101,
    name: 'first@example.com',
    platform: 'openai',
    type: 'oauth',
    status: 'active',
    group_id: 1,
    group_name: 'OpenAI primary',
    group_ids: [1],
    group_names: ['OpenAI primary'],
    ...overrides
  }
}

function detection(overrides: Partial<AccountHealthDetectionResult> = {}): AccountHealthDetectionResult {
  return {
    account_id: 101,
    account_name: 'first@example.com',
    group_id: 1,
    group_name: 'OpenAI primary',
    account_email: 'first@example.com',
    status: 'no_evidence',
    score: 0,
    language: '',
    evidence: [],
    message_date: '',
    associated_email: '',
    messages_scanned: 1,
    pages_scanned: 1,
    plus_detected: false,
    plus_score: 0,
    plus_language: '',
    plus_date: '',
    payment_method: '',
    lifespan_status: 'unavailable',
    lifespan_seconds: null,
    ban_date: '',
    elapsed_ms: 20,
    checked_at: '2026-08-07T08:00:00Z',
    ...overrides
  }
}

function page<T>(items: T[], current = 1, pages = 1, total = items.length) {
  return { items, total, page: current, page_size: 1000, pages }
}

function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function mockBlobDownload(url: string) {
  const originalCreateObjectURL = window.URL.createObjectURL
  const originalRevokeObjectURL = window.URL.revokeObjectURL
  const createObjectURL = vi.fn(() => url)
  const revokeObjectURL = vi.fn(() => {})
  let filename = ''
  let href = ''
  window.URL.createObjectURL = createObjectURL as typeof window.URL.createObjectURL
  window.URL.revokeObjectURL = revokeObjectURL as typeof window.URL.revokeObjectURL
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function () {
    filename = this.download
    href = this.href
  })

  return {
    createObjectURL,
    revokeObjectURL,
    click,
    get filename() {
      return filename
    },
    get href() {
      return href
    },
    restore() {
      window.URL.createObjectURL = originalCreateObjectURL
      window.URL.revokeObjectURL = originalRevokeObjectURL
      click.mockRestore()
    }
  }
}

function mountView() {
  return mount(AccountHealthDetectorView, {
    global: {
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        SearchInput: SearchInputStub,
        Select: SelectStub,
        DataTable: DataTableStub,
        ConfirmDialog: ConfirmDialogStub,
        Pagination: PaginationStub,
        Icon: true
      }
    }
  })
}

async function selectGroupAndLoad(wrapper: ReturnType<typeof mountView>, groupID = 1) {
  await wrapper.get(`[data-test="group-${groupID}"]`).setValue(true)
  await wrapper.get('[data-test="load-accounts"]').trigger('click')
  await flushPromises()
}

describe('admin AccountHealthDetectorView', () => {
  beforeEach(() => {
    listGroups.mockReset()
    listAccounts.mockReset()
    detectAccountHealth.mockReset()
    exportAccountNotes.mockReset()
    batchDelete.mockReset()
    showError.mockReset()
    showWarning.mockReset()
    showSuccess.mockReset()

    listGroups.mockResolvedValue(page([group(), group({ id: 2, name: 'OpenAI backup', status: 'inactive' })]))
    listAccounts.mockResolvedValue(page([account()]))
    detectAccountHealth.mockResolvedValue(detection())
    exportAccountNotes.mockResolvedValue({
      blob: new Blob(['\uFEFF\n'], { type: 'text/plain;charset=utf-8' }),
      count: 1,
      filename: 'account-health-notes.txt'
    })
    batchDelete.mockResolvedValue({ success: 1, failed: 0, success_ids: [101], failed_ids: [], results: [] })
  })

  it('loads groups without selecting or detecting them automatically', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(listGroups).toHaveBeenCalledWith(1, 1000, expect.objectContaining({ signal: expect.any(Object) }))
    expect((wrapper.get('[data-test="group-1"]').element as HTMLInputElement).checked).toBe(false)
    expect(wrapper.get('[data-test="load-accounts"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="start-scan"]').attributes('disabled')).toBeDefined()
    expect(listAccounts).not.toHaveBeenCalled()
    expect(detectAccountHealth).not.toHaveBeenCalled()
  })

  it('loads existing accounts only after the administrator selects groups', async () => {
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="group-1"]').setValue(true)
    await wrapper.get('[data-test="group-2"]').setValue(true)
    await wrapper.get('[data-test="load-accounts"]').trigger('click')
    await flushPromises()

    expect(listAccounts).toHaveBeenCalledWith(
      1,
      1000,
      [1, 2],
      expect.objectContaining({ signal: expect.any(Object) })
    )
    expect(wrapper.get('[data-test="row-101"]').text()).toContain('first@example.com')
    expect(wrapper.get('[data-test="start-scan"]').attributes('disabled')).toBeUndefined()
  })

  it('checks every loaded account and sends each account group binding', async () => {
    listAccounts.mockResolvedValue(page([
      account(),
      account({ id: 202, name: 'second@example.com', group_id: 2, group_name: 'OpenAI backup', group_ids: [2], group_names: ['OpenAI backup'] })
    ]))
    detectAccountHealth.mockImplementation(async (id: number, groupID: number) => detection({
      account_id: id,
      account_name: id === 101 ? 'first@example.com' : 'second@example.com',
      group_id: groupID
    }))
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('')
    await wrapper.get('[data-test="start-scan"]').trigger('click')
    await flushPromises()

    expect(detectAccountHealth).toHaveBeenCalledTimes(2)
    expect(detectAccountHealth).toHaveBeenCalledWith(101, 1, expect.objectContaining({ signal: expect.any(Object) }))
    expect(detectAccountHealth).toHaveBeenCalledWith(202, 2, expect.objectContaining({ signal: expect.any(Object) }))
  })

  it('shows an account notes format failure on the affected account row', async () => {
    detectAccountHealth.mockResolvedValue(detection({ status: 'format_error', format_issue: 'missing_url' }))
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    await wrapper.get('[data-test="start-scan"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="row-101"]').text()).toContain('admin.accountHealthDetector.outcome.format_error')
    expect(wrapper.get('[data-test="row-101"]').text()).toContain('admin.accountHealthDetector.formatIssue.missing_url')
  })

  it('downloads notes for exactly the selected accounts', async () => {
    listAccounts.mockResolvedValue(page([
      account(),
      account({ id: 202, name: 'second@example.com' }),
      account({ id: 303, name: 'not-selected@example.com' })
    ]))
    const exportBlob = new Blob(['\uFEFFfirst note\nsecond note'], { type: 'text/plain;charset=utf-8' })
    exportAccountNotes.mockResolvedValueOnce({
      blob: exportBlob,
      count: 2,
      filename: 'selected-account-notes.txt'
    })
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    const exportButton = wrapper.get('[data-test="export-selected-notes"]')
    expect(exportButton.attributes('disabled')).toBeDefined()
    wrapper.findComponent(DataTableStub).vm.$emit('update:selectedKeys', [202, 101])
    await flushPromises()
    expect(exportButton.attributes('disabled')).toBeUndefined()

    const download = mockBlobDownload('blob:selected-account-notes')

    try {
      await exportButton.trigger('click')
      await flushPromises()

      expect(exportAccountNotes).toHaveBeenCalledWith(
        [202, 101],
        expect.objectContaining({ signal: expect.any(AbortSignal) })
      )
      expect(download.createObjectURL).toHaveBeenCalledWith(exportBlob)
      expect(download.href).toBe('blob:selected-account-notes')
      expect(download.filename).toBe('selected-account-notes.txt')
      expect(download.click).toHaveBeenCalledTimes(1)
      expect(download.revokeObjectURL).toHaveBeenCalledWith('blob:selected-account-notes')
      expect(showSuccess).toHaveBeenCalledWith(
        'admin.accountHealthDetector.messages.accountNotesExported:{"count":2}'
      )
    } finally {
      wrapper.unmount()
      download.restore()
    }
  })

  it('exports filtered accounts in the current table sort order', async () => {
    listAccounts.mockResolvedValue(page([
      account({ id: 202, name: 'Alpha included' }),
      account({ id: 101, name: 'Charlie included' }),
      account({ id: 404, name: 'Delta excluded' }),
      account({ id: 303, name: 'Beta included' })
    ]))
    const exportBlob = new Blob(['\uFEFFnotes\n'], { type: 'text/plain;charset=utf-8' })
    exportAccountNotes.mockResolvedValueOnce({
      blob: exportBlob,
      count: 3,
      filename: 'sorted-filtered-account-notes.txt'
    })
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    await wrapper.get('[data-test="account-search"] input').setValue('included')
    wrapper.findComponent(DataTableStub).vm.$emit('sort', 'account_name', 'desc')
    await flushPromises()

    expect(wrapper.findAll('[data-test^="row-"]').map((row) => row.attributes('data-test'))).toEqual([
      'row-101',
      'row-303',
      'row-202'
    ])

    await wrapper.get('[data-test="select-filtered-accounts"]').trigger('click')
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('101,303,202')

    const download = mockBlobDownload('blob:sorted-filtered-account-notes')
    try {
      await wrapper.get('[data-test="export-selected-notes"]').trigger('click')
      await flushPromises()

      expect(exportAccountNotes).toHaveBeenCalledWith(
        [101, 303, 202],
        expect.objectContaining({ signal: expect.any(AbortSignal) })
      )
      expect(download.createObjectURL).toHaveBeenCalledWith(exportBlob)
      expect(download.click).toHaveBeenCalledTimes(1)
    } finally {
      wrapper.unmount()
      download.restore()
    }
  })

  it('downloads an empty account note represented by a BOM and line terminator', async () => {
    const exportBlob = new Blob(['\uFEFF\n'], { type: 'text/plain;charset=utf-8' })
    exportAccountNotes.mockResolvedValueOnce({
      blob: exportBlob,
      count: 1,
      filename: 'empty-account-note.txt'
    })
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)
    await wrapper.get('[data-test="select-first-account"]').trigger('click')
    const download = mockBlobDownload('blob:empty-account-note')

    try {
      await wrapper.get('[data-test="export-selected-notes"]').trigger('click')
      await flushPromises()

      expect(download.createObjectURL).toHaveBeenCalledWith(exportBlob)
      expect(download.filename).toBe('empty-account-note.txt')
      expect(download.click).toHaveBeenCalledTimes(1)
      expect(download.revokeObjectURL).toHaveBeenCalledWith('blob:empty-account-note')
      expect(showWarning).not.toHaveBeenCalled()
      expect(showSuccess).toHaveBeenCalledWith(
        'admin.accountHealthDetector.messages.accountNotesExported:{"count":1}'
      )
    } finally {
      wrapper.unmount()
      download.restore()
    }
  })

  it('exports exactly 5000 selected accounts', async () => {
    const loadedAccounts = Array.from({ length: 5000 }, (_, index) => account({
      id: index + 1,
      name: `account-${index + 1}`
    }))
    const selectedIDs = loadedAccounts.map((item) => item.id)
    const exportBlob = new Blob(['\uFEFFnotes\n'], { type: 'text/plain;charset=utf-8' })
    listAccounts.mockImplementation(async (currentPage: number) => {
      const start = (currentPage - 1) * 1000
      return page(loadedAccounts.slice(start, start + 1000), currentPage, 5, 5000)
    })
    exportAccountNotes.mockResolvedValueOnce({
      blob: exportBlob,
      count: 5000,
      filename: 'account-health-notes.txt'
    })
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)
    expect(listAccounts.mock.calls.map(([currentPage, pageSize, groupIDs]) => [
      currentPage,
      pageSize,
      groupIDs
    ])).toEqual(Array.from({ length: 5 }, (_, index) => [index + 1, 1000, [1]]))
    wrapper.findComponent(DataTableStub).vm.$emit('update:selectedKeys', selectedIDs)
    await flushPromises()
    const download = mockBlobDownload('blob:5000-account-notes')

    try {
      await wrapper.get('[data-test="export-selected-notes"]').trigger('click')
      await flushPromises()

      expect(exportAccountNotes).toHaveBeenCalledWith(
        selectedIDs,
        expect.objectContaining({ signal: expect.any(AbortSignal) })
      )
      expect(download.createObjectURL).toHaveBeenCalledWith(exportBlob)
      expect(showWarning).not.toHaveBeenCalled()
    } finally {
      wrapper.unmount()
      download.restore()
    }
  })

  it('explains the export limit before sending an oversized selection', async () => {
    const loadedAccounts = Array.from({ length: 5001 }, (_, index) => account({
      id: index + 1,
      name: `account-${index + 1}`
    }))
    listAccounts.mockResolvedValue(page(loadedAccounts))
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    wrapper.findComponent(DataTableStub).vm.$emit(
      'update:selectedKeys',
      loadedAccounts.map((item) => item.id)
    )
    await flushPromises()
    await wrapper.get('[data-test="export-selected-notes"]').trigger('click')

    expect(exportAccountNotes).not.toHaveBeenCalled()
    expect(showWarning).toHaveBeenCalledWith(
      'admin.accountHealthDetector.messages.accountNoteExportLimit:{"count":5000}'
    )
    wrapper.unmount()
  })

  it('reports selected account note export failures', async () => {
    exportAccountNotes.mockRejectedValueOnce(new Error('unavailable'))
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)
    await wrapper.get('[data-test="select-first-account"]').trigger('click')

    const exportButton = wrapper.get('[data-test="export-selected-notes"]')
    await exportButton.trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith(
      'admin.accountHealthDetector.messages.exportAccountNotesFailed'
    )
    expect(showSuccess).not.toHaveBeenCalled()
    for (const selector of [
      '[data-test="export-selected-notes"]',
      '[data-test="delete-selected"]',
      '[data-test="start-scan"]',
      '[data-test="group-1"]',
      '[data-test="load-accounts"]',
      '#account-health-concurrency'
    ]) {
      expect(wrapper.get(selector).attributes('disabled')).toBeUndefined()
    }
    expect(wrapper.findComponent(DataTableStub).props('selectionDisabled')).toBe(false)

    const download = mockBlobDownload('blob:retried-account-notes')
    try {
      await exportButton.trigger('click')
      await flushPromises()

      expect(exportAccountNotes).toHaveBeenCalledTimes(2)
      expect(download.createObjectURL).toHaveBeenCalledTimes(1)
      expect(download.click).toHaveBeenCalledTimes(1)
      expect(showSuccess).toHaveBeenCalledWith(
        'admin.accountHealthDetector.messages.accountNotesExported:{"count":1}'
      )
    } finally {
      wrapper.unmount()
      download.restore()
    }
  })

  it.each(['resolve', 'reject'] as const)(
    'aborts a pending note export on unmount and ignores a late %s',
    async (settlement) => {
      const pendingExport = deferred<{ blob: Blob; count: number; filename: string | null }>()
      exportAccountNotes.mockReturnValueOnce(pendingExport.promise)
      const wrapper = mountView()
      await flushPromises()
      await selectGroupAndLoad(wrapper)
      await wrapper.get('[data-test="select-first-account"]').trigger('click')
      const download = mockBlobDownload(`blob:late-${settlement}`)

      try {
        await wrapper.get('[data-test="export-selected-notes"]').trigger('click')
        const signal = exportAccountNotes.mock.calls[0]?.[1]?.signal as AbortSignal
        expect(signal.aborted).toBe(false)

        wrapper.unmount()
        expect(signal.aborted).toBe(true)

        if (settlement === 'resolve') {
          pendingExport.resolve({
            blob: new Blob(['\uFEFFlate note\n'], { type: 'text/plain;charset=utf-8' }),
            count: 1,
            filename: 'late-account-notes.txt'
          })
        } else {
          pendingExport.reject(new Error('late export failure'))
        }
        await flushPromises()

        expect(download.createObjectURL).not.toHaveBeenCalled()
        expect(download.click).not.toHaveBeenCalled()
        expect(showSuccess).not.toHaveBeenCalled()
        expect(showError).not.toHaveBeenCalled()
      } finally {
        download.restore()
      }
    }
  )

  it('locks scoped actions while selected account notes are exporting', async () => {
    listAccounts.mockResolvedValue(page([
      account({ id: 101, name: 'Charlie account' }),
      account({
        id: 202,
        name: 'Alpha account',
        group_id: 2,
        group_name: 'Zulu group',
        group_ids: [2],
        group_names: ['Zulu group']
      })
    ]))
    const pendingExport = deferred<{ blob: Blob; count: number; filename: string | null }>()
    exportAccountNotes.mockReturnValueOnce(pendingExport.promise)
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)
    await wrapper.get('[data-test="select-first-account"]').trigger('click')
    const resolvedBlob = new Blob(['\uFEFFnote\n'], { type: 'text/plain;charset=utf-8' })
    const download = mockBlobDownload('blob:pending-account-notes')

    try {
      await wrapper.get('[data-test="export-selected-notes"]').trigger('click')

      for (const selector of [
        '[data-test="export-selected-notes"]',
        '[data-test="delete-selected"]',
        '[data-test="delete-all"]',
        '[data-test="delete-account-101"]',
        '[data-test="start-scan"]',
        '[data-test="group-1"]',
        '[data-test="load-accounts"]',
        '#account-health-concurrency',
        '[data-test="account-search"] input',
        '[data-test="local-status-filter"]',
        '[data-test="outcome-filter"]',
        '[data-test="ban-status-filter"]',
        '[data-test="plus-status-filter"]',
        '[data-test="mobile-sort-field"]',
        '[data-test="mobile-sort-order"]'
      ]) {
        expect(wrapper.get(selector).attributes('disabled')).toBeDefined()
      }
      expect(wrapper.findComponent(DataTableStub).props('selectionDisabled')).toBe(true)
      expect(wrapper.get('[data-test="group-search"] input').attributes('disabled')).toBeDefined()
      expect((wrapper.findComponent(DataTableStub).props('columns') as Array<{ sortable?: boolean }>)
        .some((column) => column.sortable)).toBe(false)

      const rowOrder = () => wrapper.findAll('[data-test^="row-"]')
        .map((row) => row.attributes('data-test'))
      expect(rowOrder()).toEqual(['row-202', 'row-101'])
      wrapper.getComponent('[data-test="account-search"]').vm.$emit('update:modelValue', 'Charlie')
      wrapper.getComponent('[data-test="local-status-filter"]').vm.$emit('update:modelValue', 'inactive')
      wrapper.getComponent('[data-test="outcome-filter"]').vm.$emit('update:modelValue', 'failed')
      wrapper.getComponent('[data-test="ban-status-filter"]').vm.$emit('update:modelValue', 'banned')
      wrapper.getComponent('[data-test="plus-status-filter"]').vm.$emit('update:modelValue', 'detected')
      wrapper.getComponent('[data-test="mobile-sort-field"]').vm.$emit('update:modelValue', 'group_name')
      await wrapper.get('[data-test="mobile-sort-order"]').trigger('click')
      wrapper.findComponent(DataTableStub).vm.$emit('sort', 'account_name', 'desc')
      await flushPromises()

      expect(rowOrder()).toEqual(['row-202', 'row-101'])
      expect((wrapper.get('[data-test="mobile-sort-field"]').element as HTMLSelectElement).value)
        .toBe('account_name')
      expect(wrapper.get('[data-test="mobile-sort-order"]').attributes('aria-label'))
        .toBe('admin.accountHealthDetector.filters.sortAscending')
      expect((wrapper.get('[data-test="account-search"] input').element as HTMLInputElement).value).toBe('')
      for (const selector of [
        '[data-test="local-status-filter"]',
        '[data-test="outcome-filter"]',
        '[data-test="ban-status-filter"]',
        '[data-test="plus-status-filter"]'
      ]) {
        expect((wrapper.get(selector).element as HTMLSelectElement).value).toBe('all')
      }

      pendingExport.resolve({ blob: resolvedBlob, count: 1, filename: 'pending-account-notes.txt' })
      await flushPromises()

      expect(download.createObjectURL).toHaveBeenCalledWith(resolvedBlob)
      expect(download.filename).toBe('pending-account-notes.txt')
      expect(download.click).toHaveBeenCalledTimes(1)
      expect(download.revokeObjectURL).toHaveBeenCalledWith('blob:pending-account-notes')
      expect(wrapper.get('[data-test="export-selected-notes"]').attributes('disabled')).toBeUndefined()
      expect(wrapper.get('[data-test="account-search"] input').attributes('disabled')).toBeUndefined()
      expect((wrapper.findComponent(DataTableStub).props('columns') as Array<{ sortable?: boolean }>)
        .some((column) => column.sortable)).toBe(true)
    } finally {
      wrapper.unmount()
      download.restore()
    }
  })

  it('disables note export while groups are loading and does not call the API', async () => {
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)
    await wrapper.get('[data-test="select-first-account"]').trigger('click')
    const pendingGroups = deferred<ReturnType<typeof page<GroupAccountHealthCandidate>>>()
    listGroups.mockReturnValueOnce(pendingGroups.promise)

    await wrapper.get('[data-test="reload-groups"]').trigger('click')
    const exportButton = wrapper.get('[data-test="export-selected-notes"]')
    expect(exportButton.attributes('disabled')).toBeDefined()
    await exportButton.trigger('click')
    expect(exportAccountNotes).not.toHaveBeenCalled()

    pendingGroups.resolve(page([group(), group({ id: 2, name: 'OpenAI backup', status: 'inactive' })]))
    await flushPromises()
    wrapper.unmount()
  })

  it('deletes one account through the row action after confirmation', async () => {
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    await wrapper.get('[data-test="delete-account-101"]').trigger('click')
    expect(wrapper.find('[data-test="confirm-dialog"]').exists()).toBe(true)
    expect(wrapper.get('[data-test="confirm-dialog"]').text()).toContain(
      'admin.accountHealthDetector.deleteDialog.single:{"name":"first@example.com"}'
    )
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(batchDelete).toHaveBeenCalledWith([101])
    expect(wrapper.find('[data-test="row-101"]').exists()).toBe(false)
    expect(showSuccess).toHaveBeenCalled()
  })

  it('uses table selection for scoped account actions', async () => {
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    await wrapper.get('[data-test="select-first-account"]').trigger('click')
    expect(wrapper.get('[data-test="selected-keys"]').text()).toBe('101')
    expect(detectAccountHealth).not.toHaveBeenCalled()

    await wrapper.get('[data-test="delete-selected"]').trigger('click')
    expect(wrapper.get('[data-test="confirm-dialog"]').text()).toContain(
      'admin.accountHealthDetector.deleteDialog.batch:{"count":1}'
    )
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()
    expect(batchDelete).toHaveBeenCalledWith([101])
  })

  it('supports one-click deletion of every loaded account', async () => {
    listAccounts.mockResolvedValue(page([
      account(),
      account({ id: 202, name: 'second@example.com' })
    ]))
    batchDelete.mockResolvedValue({ success: 2, failed: 0, success_ids: [101, 202], failed_ids: [], results: [] })
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    await wrapper.get('[data-test="delete-all"]').trigger('click')
    expect(wrapper.get('[data-test="confirm-dialog"]').text()).toContain(
      'admin.accountHealthDetector.deleteDialog.batch:{"count":2}'
    )
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(batchDelete).toHaveBeenCalledWith([101, 202])
    expect(wrapper.find('[data-test="row-101"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="row-202"]').exists()).toBe(false)
  })

  it('splits one-click deletion into bounded backend batches', async () => {
    const loadedAccounts = Array.from({ length: 501 }, (_, index) => account({
      id: index + 1,
      name: `account-${index + 1}`
    }))
    listAccounts.mockResolvedValue(page(loadedAccounts))
    batchDelete.mockImplementation(async (ids: number[]) => ({
      success: ids.length,
      failed: 0,
      success_ids: ids,
      failed_ids: [],
      results: []
    }))
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    await wrapper.get('[data-test="delete-all"]').trigger('click')
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(batchDelete).toHaveBeenCalledTimes(2)
    expect(batchDelete.mock.calls[0]?.[0]).toEqual(loadedAccounts.slice(0, 500).map((item) => item.id))
    expect(batchDelete.mock.calls[1]?.[0]).toEqual([501])
    expect(wrapper.get('[data-test="start-scan"]').attributes('disabled')).toBeDefined()
  })

  it('locks the selected group scope while accounts are loading', async () => {
    const pendingAccounts = deferred<ReturnType<typeof page<AccountHealthCandidate>>>()
    listAccounts.mockReturnValue(pendingAccounts.promise)
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="group-1"]').setValue(true)
    await wrapper.get('[data-test="load-accounts"]').trigger('click')

    for (const selector of [
      '[data-test="group-1"]',
      '[data-test="group-2"]',
      '[data-test="select-visible-groups"]',
      '[data-test="clear-groups"]',
      '[data-test="load-accounts"]',
      '[data-test="reload-groups"]'
    ]) {
      expect(wrapper.get(selector).attributes('disabled')).toBeDefined()
    }

    await wrapper.get('[data-test="select-visible-groups"]').trigger('click')
    await wrapper.get('[data-test="clear-groups"]').trigger('click')
    await wrapper.get('[data-test="reload-groups"]').trigger('click')

    expect((wrapper.get('[data-test="group-1"]').element as HTMLInputElement).checked).toBe(true)
    expect((wrapper.get('[data-test="group-2"]').element as HTMLInputElement).checked).toBe(false)
    expect(listGroups).toHaveBeenCalledTimes(1)
    expect(listAccounts).toHaveBeenCalledTimes(1)

    pendingAccounts.resolve(page([account()]))
    await flushPromises()
  })

  it('locks group scope and request controls while deletion is pending', async () => {
    const pendingDelete = deferred<{
      success: number
      failed: number
      success_ids: number[]
      failed_ids: number[]
      results: never[]
    }>()
    batchDelete.mockReturnValue(pendingDelete.promise)
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    await wrapper.get('[data-test="delete-all"]').trigger('click')
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')

    for (const selector of [
      '[data-test="group-1"]',
      '[data-test="group-2"]',
      '[data-test="select-visible-groups"]',
      '[data-test="clear-groups"]',
      '[data-test="load-accounts"]',
      '[data-test="reload-groups"]',
      '#account-health-concurrency',
      '[data-test="start-scan"]'
    ]) {
      expect(wrapper.get(selector).attributes('disabled')).toBeDefined()
    }
    expect(wrapper.get('[data-test="group-search"] input').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-test="select-visible-groups"]').trigger('click')
    await wrapper.get('[data-test="clear-groups"]').trigger('click')
    await wrapper.get('[data-test="load-accounts"]').trigger('click')
    await wrapper.get('[data-test="reload-groups"]').trigger('click')
    await wrapper.get('[data-test="start-scan"]').trigger('click')

    expect((wrapper.get('[data-test="group-1"]').element as HTMLInputElement).checked).toBe(true)
    expect((wrapper.get('[data-test="group-2"]').element as HTMLInputElement).checked).toBe(false)
    expect(listGroups).toHaveBeenCalledTimes(1)
    expect(listAccounts).toHaveBeenCalledTimes(1)
    expect(detectAccountHealth).not.toHaveBeenCalled()

    pendingDelete.resolve({ success: 1, failed: 0, success_ids: [101], failed_ids: [], results: [] })
    await flushPromises()
  })

  it('keeps failed accounts after a partial batch deletion', async () => {
    listAccounts.mockResolvedValue(page([
      account(),
      account({ id: 202, name: 'second@example.com' })
    ]))
    batchDelete.mockResolvedValue({ success: 1, failed: 1, success_ids: [101], failed_ids: [202], results: [] })
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    await wrapper.get('[data-test="delete-all"]').trigger('click')
    await wrapper.get('[data-test="confirm-delete"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="row-101"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="row-202"]').exists()).toBe(true)
    expect(showError).toHaveBeenCalled()
  })

  it('clears stale account results when the selected group scope changes', async () => {
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)
    expect(wrapper.find('[data-test="row-101"]').exists()).toBe(true)

    await wrapper.get('[data-test="group-2"]').setValue(true)
    await flushPromises()

    expect(wrapper.find('[data-test="row-101"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="start-scan"]').attributes('disabled')).toBeDefined()
  })

  it('clamps scan concurrency to ten workers', async () => {
    listAccounts.mockResolvedValue(page(Array.from({ length: 12 }, (_, index) => account({ id: index + 1, name: `account-${index + 1}` }))))
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    await wrapper.get('#account-health-concurrency').setValue(99)
    await wrapper.get('[data-test="start-scan"]').trigger('click')
    await flushPromises()

    expect(detectAccountHealth).toHaveBeenCalledTimes(12)
    expect((wrapper.get('#account-health-concurrency').element as HTMLInputElement).value).toBe('10')
  })

  it('reports account loading failures without enabling detection', async () => {
    listAccounts.mockRejectedValue(new Error('network'))
    const wrapper = mountView()
    await flushPromises()
    await selectGroupAndLoad(wrapper)

    expect(showError).toHaveBeenCalled()
    expect(wrapper.get('[data-test="start-scan"]').attributes('disabled')).toBeDefined()
  })
})
