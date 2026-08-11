import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, nextTick } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'

import type {
  OneClickAccountNotesApplyResult,
  OneClickAccountNotesPreview
} from '@/api/admin/accounts'
import OneClickAccountNotesView from '../OneClickAccountNotesView.vue'

const {
  applyNotes,
  previewNotes,
  showError,
  showSuccess,
  showWarning
} = vi.hoisted(() => ({
  applyNotes: vi.fn(),
  previewNotes: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      applyOneClickAccountNotes: applyNotes,
      previewOneClickAccountNotes: previewNotes
    }
  }
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError, showSuccess, showWarning })
}))

vi.mock('@/composables/usePersistedPageSize', () => ({
  getPersistedPageSize: () => 20
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  const localizedErrorCodes = new Set([
    'ACCOUNT_NOTE_IMPORT_PLAN_TOO_LARGE',
    'ACCOUNT_NOTE_IMPORT_PREVIEW_STALE'
  ])
  const t = ((key: string, params?: Record<string, unknown>) => {
    if (params && !key.startsWith('admin.oneClickAccountNotes.errors.')) {
      return `${key}:${JSON.stringify(params)}`
    }
    return key
  }) as ((key: string, params?: Record<string, unknown>) => string) & { te?: (key: string) => boolean }
  t.te = (key: string) => {
    const prefix = 'admin.oneClickAccountNotes.errors.'
    return key.startsWith(prefix) && localizedErrorCodes.has(key.slice(prefix.length))
  }
  return {
    ...actual,
    useI18n: () => ({
      t
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

const DataTableStub = defineComponent({
  props: {
    columns: { type: Array, default: () => [] },
    data: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false }
  },
  template: `
    <section data-test="preview-table" :data-loading="String(loading)">
      <article v-for="row in data" :key="row.line_number" :data-test="'entry-' + row.line_number">
        <slot name="cell-line_number" :value="row.line_number" :row="row" />
        <slot name="cell-email" :value="row.email" :row="row" />
        <slot name="cell-status" :value="row.status" :row="row" />
        <slot name="cell-matched_accounts" :value="row.matched_accounts" :row="row" />
        <slot name="cell-will_update_accounts" :value="row.will_update_accounts" :row="row" />
        <slot name="cell-details" :row="row" />
      </article>
      <slot v-if="!loading && data.length === 0" name="empty" />
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
    <section v-if="show" data-test="apply-confirm">
      <p>{{ title }}</p>
      <p>{{ message }}</p>
      <slot />
      <button type="button" data-test="confirm-apply" @click="$emit('confirm')">confirm</button>
      <button type="button" data-test="cancel-apply" @click="$emit('cancel')">cancel</button>
    </section>
  `
})

const PaginationStub = defineComponent({
  name: 'PaginationStub',
  props: {
    page: { type: Number, required: true },
    total: { type: Number, required: true },
    pageSize: { type: Number, required: true }
  },
  emits: ['update:page', 'update:pageSize'],
  template: `
    <nav
      data-test="pagination"
      :data-page="page"
      :data-page-size="pageSize"
      :data-total="total"
    >
      <button type="button" data-test="page-1" @click="$emit('update:page', 1)">1</button>
      <button type="button" data-test="page-2" @click="$emit('update:page', 2)">2</button>
      <button type="button" data-test="page-size-10" @click="$emit('update:pageSize', 10)">10</button>
    </nav>
  `
})
const IconStub = { props: ['name'], template: '<i :data-icon="name" />' }

function previewFixture(overrides: Partial<OneClickAccountNotesPreview> = {}): OneClickAccountNotesPreview {
  return {
    preview_digest: 'preview-digest',
    total_lines: 2,
    valid_lines: 2,
    invalid_lines: 0,
    duplicate_lines: 0,
    conflict_lines: 0,
    matched_lines: 1,
    unmatched_lines: 1,
    matched_accounts: 1,
    will_update_accounts: 1,
    unchanged_accounts: 0,
    can_apply: true,
    entries: [
      {
        line_number: 1,
        email: 'a***@example.com',
        status: 'matched',
        matched_accounts: 1,
        will_update_accounts: 1
      },
      {
        line_number: 2,
        email: 'b***@example.com',
        status: 'unmatched',
        matched_accounts: 0,
        will_update_accounts: 0
      }
    ],
    ...overrides
  }
}

function paginatedPreviewFixture(
  firstLine: number,
  overrides: Partial<OneClickAccountNotesPreview> = {}
): OneClickAccountNotesPreview {
  const entries = Array.from({ length: 25 }, (_, index) => {
    const lineNumber = firstLine + index
    return {
      line_number: lineNumber,
      email: `account-${lineNumber}@example.com`,
      status: 'matched' as const,
      matched_accounts: 1,
      will_update_accounts: 1
    }
  })

  return previewFixture({
    total_lines: entries.length,
    valid_lines: entries.length,
    matched_lines: entries.length,
    unmatched_lines: 0,
    matched_accounts: entries.length,
    will_update_accounts: entries.length,
    entries,
    ...overrides
  })
}

const applyFixture: OneClickAccountNotesApplyResult = {
  matched_lines: 1,
  matched_accounts: 1,
  updated_accounts: 1,
  unchanged_accounts: 0,
  unmatched_lines: 1,
  invalid_lines: 2,
  conflict_lines: 3
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

interface MountViewOptions {
  realConfirmDialog?: boolean
  attachTo?: HTMLElement
}

const viewCleanups: Array<() => void> = []

function mountView(options: MountViewOptions = {}): VueWrapper {
  const stubs: Record<string, unknown> = {
    AppLayout: AppLayoutStub,
    TablePageLayout: TablePageLayoutStub,
    DataTable: DataTableStub,
    Pagination: PaginationStub,
    Icon: IconStub
  }
  if (!options.realConfirmDialog) stubs.ConfirmDialog = ConfirmDialogStub

  const wrapper = mount(OneClickAccountNotesView, {
    attachTo: options.attachTo,
    global: {
      stubs
    }
  })
  viewCleanups.push(() => wrapper.unmount())
  return wrapper
}

function mountAttachedViewWithRealDialog(): VueWrapper {
  const host = document.createElement('div')
  document.body.append(host)
  const wrapper = mountView({ realConfirmDialog: true, attachTo: host })
  viewCleanups.push(() => host.remove())
  return wrapper
}

function createDataTransfer(files: File[]): DataTransfer {
  if (typeof globalThis.DataTransfer === 'function') {
    const dataTransfer = new DataTransfer()
    files.forEach(file => dataTransfer.items.add(file))
    return dataTransfer
  }

  // jsdom does not implement DataTransfer. This keeps its FileList-shaped
  // contract while the test still dispatches a real bubbling drop event.
  const fileList = Object.assign([...files], {
    item: (index: number) => files[index] ?? null
  }) as unknown as FileList
  return { files: fileList, types: ['Files'] } as unknown as DataTransfer
}

async function dropFiles(wrapper: VueWrapper, files: File[]) {
  const dropEvent = new Event('drop', { bubbles: true, cancelable: true }) as DragEvent
  Object.defineProperty(dropEvent, 'dataTransfer', {
    configurable: true,
    value: createDataTransfer(files)
  })
  wrapper.get('[data-test="file-drop-zone"]').element.dispatchEvent(dropEvent)
  await nextTick()
  await vi.waitFor(() => {
    expect(wrapper.find('[data-test="file-reading"]').exists()).toBe(false)
  })
  await flushPromises()
  return dropEvent
}

async function openRealConfirmDialog(wrapper: VueWrapper): Promise<HTMLElement> {
  const applyButton = wrapper.get('[data-test="apply-notes"]')
  ;(applyButton.element as HTMLButtonElement).focus()
  await applyButton.trigger('click')
  await nextTick()
  const dialogs = document.body.querySelectorAll<HTMLElement>('[role="dialog"]')
  const dialog = dialogs.item(dialogs.length - 1)
  if (!dialog) throw new Error('Expected the real confirmation dialog to be open')
  return dialog
}

function clickRealDialogButton(dialog: HTMLElement, label: string) {
  const button = Array.from(dialog.querySelectorAll('button'))
    .find(candidate => candidate.textContent?.trim() === label)
  if (!button) throw new Error(`Expected dialog button: ${label}`)
  button.click()
}

async function chooseFiles(wrapper: VueWrapper, files: File[], waitForRead = true) {
  const input = wrapper.get('[data-test="file-input"]')
  Object.defineProperty(input.element, 'files', {
    configurable: true,
    value: files
  })
  await input.trigger('change')
  if (waitForRead) {
    await vi.waitFor(() => {
      expect(wrapper.find('[data-test="file-reading"]').exists()).toBe(false)
    })
  }
  await flushPromises()
}

async function chooseFile(wrapper: VueWrapper, file: File, waitForRead = true) {
  await chooseFiles(wrapper, [file], waitForRead)
}

function withTextReader(file: File, read: () => Promise<string>): File {
  Object.defineProperty(file, 'text', { configurable: true, value: read })
  return file
}

async function openAndConfirmApply(wrapper: VueWrapper) {
  await wrapper.get('[data-test="apply-notes"]').trigger('click')
  await wrapper.get('[data-test="confirm-apply"]').trigger('click')
}

describe('admin OneClickAccountNotesView', () => {
  beforeEach(() => {
    previewNotes.mockReset()
    applyNotes.mockReset()
    showError.mockReset()
    showSuccess.mockReset()
    showWarning.mockReset()
    previewNotes.mockResolvedValue(previewFixture())
    applyNotes.mockResolvedValue(applyFixture)
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('11111111-1111-4111-8111-111111111111')
  })

  afterEach(() => {
    viewCleanups.splice(0).forEach(cleanup => cleanup())
    document.body.querySelectorAll('[role="dialog"]').forEach(dialog => dialog.remove())
    document.body.classList.remove('modal-open')
    vi.restoreAllMocks()
  })

  it('counts a TXT file asynchronously without rendering its sensitive source lines', async () => {
    const wrapper = mountView()
    const sensitiveLine = 'account@example.com----https://mail.invalid/?token=do-not-render'
    const file = new File([sensitiveLine], 'account-notes.txt', { type: 'text/plain' })
    const textReader = vi.fn(async () => sensitiveLine)
    Object.defineProperty(file, 'text', { configurable: true, value: textReader })
    previewNotes.mockResolvedValueOnce(previewFixture({
      entries: [
        {
          line_number: 1,
          email: 'a***@example.com',
          status: 'matched',
          matched_accounts: 1,
          will_update_accounts: 1,
          reason: 'https://backend.invalid/?token=also-do-not-render'
        },
        {
          line_number: 2,
          email: 'b***@example.com',
          status: 'unmatched',
          matched_accounts: 0,
          will_update_accounts: 0
        }
      ]
    }))

    await chooseFile(wrapper, file)
    await flushPromises()

    expect(previewNotes).toHaveBeenCalledWith(
      file,
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    expect(textReader).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-test="non-empty-line-count"]').text()).toContain('1')
    expect(wrapper.get('[data-test="selected-file-name"]').text()).toBe('account-notes.txt')
    expect(wrapper.text()).toContain('a***@example.com')
    expect(wrapper.text()).not.toContain(sensitiveLine)
    expect(wrapper.text()).not.toContain('also-do-not-render')
    expect(wrapper.get('[data-test="apply-notes"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-test="file-drop-zone"]').attributes('role')).toBeUndefined()
    expect(wrapper.get('[data-test="choose-file"]').element.tagName).toBe('BUTTON')
  })

  it('paginates distinct preview entries and resets the page after page-size and file changes', async () => {
    previewNotes.mockReset()
    previewNotes
      .mockResolvedValueOnce(paginatedPreviewFixture(1))
      .mockResolvedValueOnce(paginatedPreviewFixture(101, { preview_digest: 'replacement-digest' }))
    const wrapper = mountView()
    const firstFile = new File(['first'], 'first.txt', { type: 'text/plain' })
    const replacementFile = new File(['replacement'], 'replacement.txt', { type: 'text/plain' })

    await chooseFile(wrapper, firstFile)

    const pagination = wrapper.get('[data-test="pagination"]')
    expect(pagination.attributes()).toMatchObject({
      'data-page': '1',
      'data-page-size': '20',
      'data-total': '25'
    })
    expect(wrapper.find('[data-test="entry-1"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="entry-20"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="entry-21"]').exists()).toBe(false)

    await wrapper.get('[data-test="page-2"]').trigger('click')

    expect(pagination.attributes('data-page')).toBe('2')
    expect(wrapper.find('[data-test="entry-1"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="entry-21"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="entry-25"]').exists()).toBe(true)

    await wrapper.get('[data-test="page-size-10"]').trigger('click')

    expect(pagination.attributes('data-page')).toBe('1')
    expect(pagination.attributes('data-page-size')).toBe('10')
    expect(wrapper.find('[data-test="entry-1"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="entry-10"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="entry-11"]').exists()).toBe(false)

    await wrapper.get('[data-test="page-2"]').trigger('click')
    expect(wrapper.find('[data-test="entry-1"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="entry-11"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="entry-20"]').exists()).toBe(true)

    await chooseFile(wrapper, replacementFile)

    const replacementPagination = wrapper.get('[data-test="pagination"]')
    expect(previewNotes).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-test="selected-file-name"]').text()).toBe('replacement.txt')
    expect(replacementPagination.attributes('data-page')).toBe('1')
    expect(replacementPagination.attributes('data-page-size')).toBe('10')
    expect(wrapper.find('[data-test="entry-11"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="entry-101"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="entry-110"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="entry-111"]').exists()).toBe(false)
  })

  it('opens the native file picker from an accessibly named choose button', async () => {
    const wrapper = mountView()
    const input = wrapper.get<HTMLInputElement>('[data-test="file-input"]')
    const inputClick = vi.spyOn(input.element, 'click')
    const chooseButton = wrapper.get('[data-test="choose-file"]')

    expect(chooseButton.attributes('aria-label')).toBe('admin.oneClickAccountNotes.upload.chooseFile')
    expect(chooseButton.text()).toContain('admin.oneClickAccountNotes.upload.chooseFile')

    await chooseButton.trigger('click')

    expect(inputClick).toHaveBeenCalledOnce()
  })

  it('keeps a long selected filename available while the visible label can truncate', async () => {
    const wrapper = mountView()
    const filename = `${'account-notes-'.repeat(20)}.txt`
    const file = new File(['source'], filename, { type: 'text/plain' })

    await chooseFile(wrapper, file)

    const selectedFilename = wrapper.get('[data-test="selected-file-name"]')
    expect(selectedFilename.text()).toBe(filename)
    expect(selectedFilename.attributes('title')).toBe(filename)
  })

  it('accepts a TXT file from a DataTransfer drop event and previews it', async () => {
    const wrapper = mountView()
    const file = new File(['source'], 'dropped-notes.txt', { type: 'text/plain' })

    const dropEvent = await dropFiles(wrapper, [file])
    await flushPromises()

    expect(dropEvent.defaultPrevented).toBe(true)
    expect(previewNotes).toHaveBeenCalledWith(
      file,
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    expect(wrapper.get('[data-test="selected-file-name"]').text()).toBe('dropped-notes.txt')
    expect(wrapper.get('[data-test="choose-file"]').attributes('aria-label'))
      .toBe('admin.oneClickAccountNotes.upload.replaceFile')
  })

  it('shows stable invalid and conflict reasons without allowing an impossible import to be applied', async () => {
    const wrapper = mountView()
    previewNotes.mockResolvedValueOnce(previewFixture({
      total_lines: 4,
      valid_lines: 3,
      invalid_lines: 1,
      conflict_lines: 2,
      matched_lines: 0,
      unmatched_lines: 1,
      matched_accounts: 0,
      will_update_accounts: 0,
      can_apply: false,
      entries: [
        {
          line_number: 1,
          status: 'invalid',
          matched_accounts: 0,
          will_update_accounts: 0,
          reason: 'missing_email'
        },
        {
          line_number: 2,
          email: 'c***@example.com',
          status: 'conflict',
          matched_accounts: 0,
          will_update_accounts: 0,
          reason: 'duplicate_email_conflict'
        },
        {
          line_number: 3,
          email: 'c***@example.com',
          status: 'conflict',
          matched_accounts: 0,
          will_update_accounts: 0,
          reason: 'duplicate_email_conflict'
        },
        {
          line_number: 4,
          email: 'u***@example.com',
          status: 'unmatched',
          matched_accounts: 0,
          will_update_accounts: 0,
          reason: 'https://backend.invalid/?token=unknown-reason'
        }
      ]
    }))

    await chooseFile(wrapper, new File(['source'], 'notes.txt', { type: 'text/plain' }))
    await flushPromises()

    expect(wrapper.text()).toContain('admin.oneClickAccountNotes.reasons.missingEmail')
    expect(wrapper.text()).toContain('admin.oneClickAccountNotes.reasons.duplicateEmailConflict')
    expect(wrapper.text()).not.toContain('unknown-reason')
    expect(wrapper.get('[data-test="apply-notes"]').attributes('disabled')).toBeDefined()
  })

  it('rejects invalid, empty, oversized, and multiple file selections before previewing', async () => {
    const wrapper = mountView()

    await chooseFile(wrapper, new File(['value'], 'notes.json', { type: 'application/json' }))
    await chooseFile(wrapper, new File([], 'empty.txt', { type: 'text/plain' }))
    await chooseFile(wrapper, new File([new Uint8Array(1024 * 1024 + 1)], 'large.txt', { type: 'text/plain' }))
    await chooseFiles(wrapper, [
      new File(['one'], 'one.txt', { type: 'text/plain' }),
      new File(['two'], 'two.txt', { type: 'text/plain' })
    ])

    expect(previewNotes).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('admin.oneClickAccountNotes.errors.invalidFileType')
    expect(showError).toHaveBeenCalledWith('admin.oneClickAccountNotes.errors.emptyFile')
    expect(showError).toHaveBeenCalledWith('admin.oneClickAccountNotes.errors.fileTooLarge')
    expect(showError).toHaveBeenCalledWith('admin.oneClickAccountNotes.errors.singleFileOnly')
  })

  it('allows exactly 5000 non-empty lines and rejects 5001 before previewing', async () => {
    const wrapper = mountView()
    const allowedContent = Array.from({ length: 5000 }, (_, index) => `allowed-${index}@example.test`).join('\n')
    const rejectedContent = `${allowedContent}\nrejected@example.test`
    const allowedFile = withTextReader(
      new File([allowedContent], 'allowed.txt', { type: 'text/plain' }),
      vi.fn(async () => allowedContent)
    )
    const rejectedFile = withTextReader(
      new File([rejectedContent], 'rejected.txt', { type: 'text/plain' }),
      vi.fn(async () => rejectedContent)
    )

    await chooseFile(wrapper, allowedFile)
    await flushPromises()

    expect(previewNotes).toHaveBeenCalledOnce()
    expect(previewNotes).toHaveBeenCalledWith(
      allowedFile,
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    expect(wrapper.get('[data-test="non-empty-line-count"]').text()).toContain('5000')

    await chooseFile(wrapper, rejectedFile)
    await flushPromises()

    expect(previewNotes).toHaveBeenCalledOnce()
    expect(showError).toHaveBeenCalledWith('admin.oneClickAccountNotes.errors.ACCOUNT_NOTE_IMPORT_TOO_MANY_LINES')
    expect(wrapper.find('[data-test="selected-file-name"]').exists()).toBe(false)
  })

  it('ignores an obsolete line-count result when a replacement file finishes reading first', async () => {
    const firstRead = deferred<string>()
    const secondRead = deferred<string>()
    const firstFile = withTextReader(
      new File(['first@example.test'], 'first.txt', { type: 'text/plain' }),
      () => firstRead.promise
    )
    const secondFile = withTextReader(
      new File(['second@example.test'], 'second.txt', { type: 'text/plain' }),
      () => secondRead.promise
    )
    const wrapper = mountView()

    await chooseFile(wrapper, firstFile, false)
    await chooseFile(wrapper, secondFile, false)
    secondRead.resolve('second@example.test')
    await flushPromises()

    expect(previewNotes).toHaveBeenCalledOnce()
    expect(previewNotes).toHaveBeenCalledWith(
      secondFile,
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    expect(wrapper.get('[data-test="selected-file-name"]').text()).toBe('second.txt')

    firstRead.resolve('first@example.test\nobsolete@example.test')
    await flushPromises()

    expect(previewNotes).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-test="selected-file-name"]').text()).toBe('second.txt')
    expect(wrapper.get('[data-test="non-empty-line-count"]').text()).toContain('1')
  })

  it('localizes server-side resource limit errors', async () => {
    previewNotes.mockRejectedValueOnce({ reason: 'ACCOUNT_NOTE_IMPORT_PLAN_TOO_LARGE' })
    const wrapper = mountView()

    await chooseFile(wrapper, new File(['source'], 'notes.txt', { type: 'text/plain' }))
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('admin.oneClickAccountNotes.errors.ACCOUNT_NOTE_IMPORT_PLAN_TOO_LARGE')
    expect(wrapper.get('[data-test="preview-empty-message"]').text()).toBe('admin.oneClickAccountNotes.errors.ACCOUNT_NOTE_IMPORT_PLAN_TOO_LARGE')
  })

  it('cancels an obsolete preview when the administrator replaces the file', async () => {
    const first = deferred<OneClickAccountNotesPreview>()
    const second = deferred<OneClickAccountNotesPreview>()
    previewNotes.mockReset()
    previewNotes.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const wrapper = mountView()
    const firstFile = new File(['first'], 'first.txt', { type: 'text/plain' })
    const secondFile = new File(['second'], 'second.txt', { type: 'text/plain' })

    await chooseFile(wrapper, firstFile)
    const firstSignal = previewNotes.mock.calls[0][1].signal as AbortSignal
    await chooseFile(wrapper, secondFile)

    expect(firstSignal.aborted).toBe(true)
    second.resolve(previewFixture({
      entries: [{
        line_number: 1,
        email: 's***@example.com',
        status: 'matched',
        matched_accounts: 1,
        will_update_accounts: 1
      }]
    }))
    first.resolve(previewFixture({
      entries: [{
        line_number: 1,
        email: 'obsolete@example.com',
        status: 'matched',
        matched_accounts: 1,
        will_update_accounts: 1
      }]
    }))
    await flushPromises()

    expect(wrapper.text()).toContain('s***@example.com')
    expect(wrapper.text()).not.toContain('obsolete@example.com')
  })

  it('opens and cancels confirmation without applying or discarding the preview', async () => {
    const wrapper = mountView()
    await chooseFile(wrapper, new File(['source'], 'notes.txt', { type: 'text/plain' }))
    await flushPromises()
    const summaryBefore = wrapper.get('[data-test="preview-summary"]').text()

    await wrapper.get('[data-test="apply-notes"]').trigger('click')

    expect(wrapper.find('[data-test="apply-confirm"]').exists()).toBe(true)
    expect(applyNotes).not.toHaveBeenCalled()

    await wrapper.get('[data-test="cancel-apply"]').trigger('click')

    expect(wrapper.find('[data-test="apply-confirm"]').exists()).toBe(false)
    expect(applyNotes).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="preview-summary"]').text()).toBe(summaryBefore)
    expect(wrapper.get('[data-test="apply-notes"]').attributes('disabled')).toBeUndefined()
  })

  it('starts one apply request after confirmation and ignores repeated confirmation while it is pending', async () => {
    const pending = deferred<OneClickAccountNotesApplyResult>()
    previewNotes.mockResolvedValueOnce(previewFixture({
      total_lines: 7,
      valid_lines: 5,
      invalid_lines: 2,
      conflict_lines: 3,
      unmatched_lines: 1,
      entries: []
    }))
    applyNotes.mockReset()
    applyNotes.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await chooseFile(wrapper, new File(['source'], 'notes.txt', { type: 'text/plain' }))
    await flushPromises()
    await wrapper.get('[data-test="apply-notes"]').trigger('click')
    const confirmButton = wrapper.get('[data-test="confirm-apply"]').element as HTMLButtonElement

    confirmButton.click()
    confirmButton.click()
    await nextTick()

    expect(applyNotes).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-test="apply-notes"]').attributes('disabled')).toBeDefined()

    pending.resolve(applyFixture)
    await flushPromises()

    expect(applyNotes).toHaveBeenCalledOnce()
    expect(wrapper.get('[data-test="apply-result"]').text()).toContain('admin.oneClickAccountNotes.result.success')
    expect(wrapper.get('[data-test="apply-unprocessed-summary"]').text()).toBe(
      'admin.oneClickAccountNotes.result.unprocessedSummary:{"unmatched":1,"invalid":2,"conflicts":3}'
    )
  })

  it('traps Tab inside the real confirmation dialog and restores focus after Escape', async () => {
    const wrapper = mountAttachedViewWithRealDialog()
    await chooseFile(wrapper, new File(['source'], 'notes.txt', { type: 'text/plain' }))
    await flushPromises()
    const applyButton = wrapper.get('[data-test="apply-notes"]').element as HTMLButtonElement
    const dialog = await openRealConfirmDialog(wrapper)

    const closeButton = dialog.querySelector<HTMLButtonElement>('button[aria-label="Close modal"]')
    const confirmButton = Array.from(dialog.querySelectorAll<HTMLButtonElement>('button')).find(
      button => button.textContent?.trim() === 'admin.oneClickAccountNotes.confirm.apply'
    )
    expect(closeButton).not.toBeNull()
    expect(confirmButton).toBeDefined()
    expect(document.activeElement).toBe(closeButton)

    closeButton?.dispatchEvent(new KeyboardEvent('keydown', {
      key: 'Tab',
      shiftKey: true,
      bubbles: true,
      cancelable: true
    }))
    expect(document.activeElement).toBe(confirmButton)

    confirmButton?.dispatchEvent(new KeyboardEvent('keydown', {
      key: 'Tab',
      bubbles: true,
      cancelable: true
    }))
    expect(document.activeElement).toBe(closeButton)

    document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    await nextTick()

    expect(document.activeElement).toBe(applyButton)
    expect(applyNotes).not.toHaveBeenCalled()
  })

  it('focuses the result summary after an apply succeeds through the real confirmation dialog', async () => {
    const wrapper = mountAttachedViewWithRealDialog()
    await chooseFile(wrapper, new File(['source'], 'notes.txt', { type: 'text/plain' }))
    await flushPromises()
    const dialog = await openRealConfirmDialog(wrapper)

    clickRealDialogButton(dialog, 'admin.oneClickAccountNotes.confirm.apply')
    await flushPromises()

    const result = wrapper.get('[data-test="apply-result"]')
    expect(result.attributes('tabindex')).toBe('-1')
    expect(document.activeElement).toBe(result.element)
  })

  it('refocuses the re-enabled apply button after an ordinary apply failure', async () => {
    applyNotes.mockRejectedValueOnce(new Error('network timeout'))
    const wrapper = mountAttachedViewWithRealDialog()
    await chooseFile(wrapper, new File(['source'], 'notes.txt', { type: 'text/plain' }))
    await flushPromises()
    const applyButton = wrapper.get('[data-test="apply-notes"]').element as HTMLButtonElement
    const dialog = await openRealConfirmDialog(wrapper)

    clickRealDialogButton(dialog, 'admin.oneClickAccountNotes.confirm.apply')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('network timeout')
    expect(applyButton.disabled).toBe(false)
    expect(document.activeElement).toBe(applyButton)
  })

  it('confirms the overwrite and reuses the preview operation key after an ambiguous apply failure', async () => {
    applyNotes.mockRejectedValueOnce(new Error('network timeout')).mockResolvedValueOnce(applyFixture)
    const wrapper = mountView()
    const file = new File(['source'], 'notes.txt', { type: 'text/plain' })
    await chooseFile(wrapper, file)
    await flushPromises()

    await openAndConfirmApply(wrapper)
    await flushPromises()
    expect(showError).toHaveBeenCalledWith('network timeout')
    expect(previewNotes).toHaveBeenCalledTimes(1)

    await openAndConfirmApply(wrapper)
    await flushPromises()

    expect(applyNotes).toHaveBeenCalledTimes(2)
    const firstKey = applyNotes.mock.calls[0][2]
    const retryKey = applyNotes.mock.calls[1][2]
    expect(firstKey).toBe('account-note-import-11111111-1111-4111-8111-111111111111')
    expect(retryKey).toBe(firstKey)
    expect(applyNotes).toHaveBeenLastCalledWith(
      file,
      'preview-digest',
      firstKey,
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    expect(wrapper.get('[data-test="apply-result"]').text()).toContain('admin.oneClickAccountNotes.result.success')
    expect(wrapper.get('[data-test="apply-notes"]').attributes('disabled')).toBeDefined()
  })

  it('refreshes a stale preview and applies again with the fresh digest and operation key', async () => {
    vi.mocked(globalThis.crypto.randomUUID)
      .mockReset()
      .mockReturnValueOnce('11111111-1111-4111-8111-111111111111')
      .mockReturnValueOnce('22222222-2222-4222-8222-222222222222')
    previewNotes
      .mockResolvedValueOnce(previewFixture())
      .mockResolvedValueOnce(previewFixture({ preview_digest: 'fresh-digest' }))
    applyNotes
      .mockRejectedValueOnce({ reason: 'ACCOUNT_NOTE_IMPORT_PREVIEW_STALE' })
      .mockResolvedValueOnce(applyFixture)
    const wrapper = mountAttachedViewWithRealDialog()
    const file = new File(['source'], 'notes.txt', { type: 'text/plain' })
    await chooseFile(wrapper, file)
    await flushPromises()

    const staleDialog = await openRealConfirmDialog(wrapper)
    clickRealDialogButton(staleDialog, 'admin.oneClickAccountNotes.confirm.apply')
    await flushPromises()

    expect(showWarning).toHaveBeenCalledWith('admin.oneClickAccountNotes.errors.ACCOUNT_NOTE_IMPORT_PREVIEW_STALE')
    expect(previewNotes).toHaveBeenCalledTimes(2)
    expect(applyNotes).toHaveBeenCalledTimes(1)
    expect(document.activeElement).toBe(wrapper.get('[data-test="apply-notes"]').element)

    const freshDialog = await openRealConfirmDialog(wrapper)
    clickRealDialogButton(freshDialog, 'admin.oneClickAccountNotes.confirm.apply')
    await flushPromises()

    expect(applyNotes).toHaveBeenCalledTimes(2)
    expect(applyNotes.mock.calls[0][2]).toBe('account-note-import-11111111-1111-4111-8111-111111111111')
    expect(applyNotes).toHaveBeenLastCalledWith(
      file,
      'fresh-digest',
      'account-note-import-22222222-2222-4222-8222-222222222222',
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
    expect(wrapper.get('[data-test="apply-result"]').text()).toContain('admin.oneClickAccountNotes.result.success')
  })

  it('keeps a stale summary visible but unusable when automatic re-preview fails, then retries safely', async () => {
    vi.mocked(globalThis.crypto.randomUUID)
      .mockReset()
      .mockReturnValueOnce('11111111-1111-4111-8111-111111111111')
      .mockReturnValueOnce('22222222-2222-4222-8222-222222222222')
    previewNotes
      .mockResolvedValueOnce(paginatedPreviewFixture(1))
      .mockRejectedValueOnce(new Error('refresh failed'))
      .mockResolvedValueOnce(paginatedPreviewFixture(101, { preview_digest: 'fresh-digest' }))
    applyNotes
      .mockRejectedValueOnce({ reason: 'ACCOUNT_NOTE_IMPORT_PREVIEW_STALE' })
      .mockResolvedValueOnce(applyFixture)
    const wrapper = mountAttachedViewWithRealDialog()
    const file = new File(['source'], 'notes.txt', { type: 'text/plain' })
    await chooseFile(wrapper, file)
    await flushPromises()
    const staleSummary = wrapper.get('[data-test="preview-summary"]').text()
    const pagination = wrapper.get('[data-test="pagination"]')
    await wrapper.get('[data-test="page-2"]').trigger('click')
    expect(wrapper.find('[data-test="entry-1"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="entry-21"]').exists()).toBe(true)
    const dialog = await openRealConfirmDialog(wrapper)

    clickRealDialogButton(dialog, 'admin.oneClickAccountNotes.confirm.apply')
    await flushPromises()

    const retryButton = wrapper.get('[data-test="retry-preview"]')
    expect(wrapper.get('[data-test="preview-summary"]').text()).toBe(staleSummary)
    expect(wrapper.get('[data-test="preview-error"]').text()).toBe('refresh failed')
    expect(wrapper.get('[data-test="apply-notes"]').attributes('disabled')).toBeDefined()
    expect(document.activeElement).toBe(retryButton.element)
    expect(applyNotes).toHaveBeenCalledOnce()
    expect(applyNotes.mock.calls[0][1]).toBe('preview-digest')
    expect(pagination.attributes('data-page')).toBe('1')
    expect(wrapper.find('[data-test="entry-1"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="entry-21"]').exists()).toBe(false)

    await wrapper.get('[data-test="page-2"]').trigger('click')
    expect(wrapper.find('[data-test="entry-21"]').exists()).toBe(true)
    await retryButton.trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-test="preview-error"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="apply-notes"]').attributes('disabled')).toBeUndefined()
    expect(document.activeElement).toBe(wrapper.get('[data-test="apply-notes"]').element)
    expect(pagination.attributes('data-page')).toBe('1')
    expect(wrapper.find('[data-test="entry-21"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="entry-101"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="entry-121"]').exists()).toBe(false)

    const retryDialog = await openRealConfirmDialog(wrapper)
    clickRealDialogButton(retryDialog, 'admin.oneClickAccountNotes.confirm.apply')
    await flushPromises()

    expect(applyNotes).toHaveBeenCalledTimes(2)
    expect(applyNotes).toHaveBeenLastCalledWith(
      file,
      'fresh-digest',
      'account-note-import-22222222-2222-4222-8222-222222222222',
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    )
  })

  it('does not refresh the preview for an unrelated HTTP 409', async () => {
    applyNotes.mockRejectedValueOnce({
      status: 409,
      reason: 'IDEMPOTENCY_KEY_REUSED',
      message: 'operation conflict'
    })
    const wrapper = mountView()
    await chooseFile(wrapper, new File(['source'], 'notes.txt', { type: 'text/plain' }))
    await flushPromises()

    await openAndConfirmApply(wrapper)
    await flushPromises()

    expect(previewNotes).toHaveBeenCalledTimes(1)
    expect(showWarning).not.toHaveBeenCalled()
    expect(showError).toHaveBeenCalledWith('operation conflict')
  })

  it('aborts a pending preview when the page unmounts', async () => {
    const pending = deferred<OneClickAccountNotesPreview>()
    previewNotes.mockReset()
    previewNotes.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await chooseFile(wrapper, new File(['source'], 'notes.txt', { type: 'text/plain' }))
    const signal = previewNotes.mock.calls[0][1].signal as AbortSignal

    wrapper.unmount()

    expect(signal.aborted).toBe(true)
  })
})
