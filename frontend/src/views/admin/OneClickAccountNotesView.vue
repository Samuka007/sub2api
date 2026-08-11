<template>
  <AppLayout>
    <TablePageLayout class="one-click-account-notes-layout">
      <template #actions>
        <div class="space-y-3">
          <section class="min-w-0 px-1 lg:hidden" aria-labelledby="one-click-notes-mobile-title">
            <h1 id="one-click-notes-mobile-title" class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t('admin.oneClickAccountNotes.title') }}
            </h1>
            <p class="mt-1 text-sm leading-5 text-gray-500 dark:text-dark-400">
              {{ t('admin.oneClickAccountNotes.description') }}
            </p>
          </section>

          <section
            class="border-y border-gray-200 bg-white px-4 py-4 dark:border-dark-700 dark:bg-dark-800"
            :aria-busy="readingFile || previewing || applying"
            aria-labelledby="one-click-notes-upload-title"
          >
            <div class="flex flex-col gap-4 xl:flex-row xl:items-end">
              <div class="min-w-0 flex-1">
                <div class="mb-3">
                  <h2 id="one-click-notes-upload-title" class="text-sm font-semibold text-gray-900 dark:text-white">
                    {{ t('admin.oneClickAccountNotes.upload.title') }}
                  </h2>
                  <p class="mt-1 text-xs leading-5 text-gray-500 dark:text-gray-400">
                    {{ t('admin.oneClickAccountNotes.upload.description') }}
                  </p>
                </div>

                <div
                  class="flex min-h-28 w-full flex-col items-center justify-center gap-3 border border-dashed px-4 py-5 text-center transition-colors sm:flex-row sm:justify-between sm:text-left"
                  :class="[
                    dragging
                      ? 'border-primary-500 bg-primary-50 dark:bg-primary-900/20'
                      : 'border-gray-300 bg-gray-50 dark:border-dark-600 dark:bg-dark-900/40',
                    applying ? 'opacity-60' : 'hover:border-primary-400'
                  ]"
                  data-test="file-drop-zone"
                  @dragenter.prevent="handleDragEnter"
                  @dragover.prevent
                  @dragleave.prevent="handleDragLeave"
                  @drop.prevent="handleDrop"
                >
                  <div class="flex w-full min-w-0 items-center gap-3">
                    <span class="flex h-10 w-10 shrink-0 items-center justify-center rounded bg-white text-primary-600 shadow-sm dark:bg-dark-800 dark:text-primary-400">
                      <Icon :name="selectedFile ? 'document' : 'upload'" size="lg" />
                    </span>
                    <div class="min-w-0">
                      <p
                        v-if="selectedFile"
                        class="truncate text-sm font-medium text-gray-900 dark:text-white"
                        :title="selectedFile.name"
                        data-test="selected-file-name"
                      >
                        {{ selectedFile.name }}
                      </p>
                      <p v-else class="text-sm font-medium text-gray-800 dark:text-gray-200">
                        {{ t('admin.oneClickAccountNotes.upload.dropOrChoose') }}
                      </p>
                      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
                        <template v-if="selectedFile">{{ formatFileSize(selectedFile.size) }} · </template>
                        <span v-if="readingFile" data-test="file-reading">
                          {{ t('admin.oneClickAccountNotes.upload.reading') }}
                        </span>
                        <span v-else-if="selectedFile && nonEmptyLineCount !== null" data-test="non-empty-line-count">
                          {{ t('admin.oneClickAccountNotes.upload.lineCount', { count: nonEmptyLineCount }) }}
                        </span>
                        <template v-else>{{ t('admin.oneClickAccountNotes.upload.formatHint') }}</template>
                      </p>
                    </div>
                  </div>

                  <div class="flex shrink-0 items-center gap-2">
                    <button
                      v-if="selectedFile"
                      type="button"
                      class="btn btn-secondary h-9 w-9 p-0"
                      :disabled="applying"
                      :aria-label="t('admin.oneClickAccountNotes.upload.removeFile')"
                      :title="t('admin.oneClickAccountNotes.upload.removeFile')"
                      data-test="remove-file"
                      @click.stop="clearSelection"
                    >
                      <Icon name="x" size="sm" />
                    </button>
                    <button
                      type="button"
                      class="btn btn-secondary"
                      :disabled="applying"
                      :aria-label="t(selectedFile ? 'admin.oneClickAccountNotes.upload.replaceFile' : 'admin.oneClickAccountNotes.upload.chooseFile')"
                      data-test="choose-file"
                      @click.stop="openFilePicker"
                    >
                      <Icon name="upload" size="sm" />
                      <span>{{ t(selectedFile ? 'admin.oneClickAccountNotes.upload.replaceFile' : 'admin.oneClickAccountNotes.upload.chooseFile') }}</span>
                    </button>
                  </div>
                </div>

                <input
                  ref="fileInput"
                  type="file"
                  class="hidden"
                  accept=".txt,text/plain"
                  data-test="file-input"
                  @change="handleFileChange"
                />
              </div>

              <div class="flex shrink-0 flex-wrap items-center gap-2 xl:pb-0.5">
                <button
                  v-if="selectedFile && previewError"
                  ref="retryPreviewButton"
                  type="button"
                  class="btn btn-secondary"
                  :disabled="readingFile || previewing || applying"
                  data-test="retry-preview"
                  @click="retryPreview"
                >
                  <Icon name="refresh" size="sm" :class="{ 'animate-spin': previewing }" />
                  <span>{{ t('admin.oneClickAccountNotes.actions.retry') }}</span>
                </button>
                <button
                  ref="applyButton"
                  type="button"
                  class="btn btn-primary"
                  :disabled="!canOpenConfirmation"
                  :aria-busy="applying"
                  data-test="apply-notes"
                  @click="showApplyConfirm = true"
                >
                  <Icon :name="applying ? 'refresh' : 'check'" size="sm" :class="{ 'animate-spin': applying }" />
                  <span>{{ t(applying ? 'admin.oneClickAccountNotes.actions.applying' : 'admin.oneClickAccountNotes.actions.apply') }}</span>
                </button>
              </div>
            </div>

            <p class="mt-3 flex items-start gap-2 text-xs leading-5 text-amber-700 dark:text-amber-300">
              <Icon name="lock" size="sm" class="mt-0.5 shrink-0" />
              <span>{{ t('admin.oneClickAccountNotes.preview.privacy') }}</span>
            </p>
          </section>

          <section
            v-if="applyResult"
            ref="applyResultSummary"
            class="border-y border-emerald-200 bg-emerald-50 px-4 py-3 text-sm text-emerald-800 focus:outline-none focus:ring-2 focus:ring-inset focus:ring-emerald-500 dark:border-emerald-800 dark:bg-emerald-900/20 dark:text-emerald-200"
            aria-live="polite"
            tabindex="-1"
            data-test="apply-result"
          >
            <div class="flex items-start gap-2">
              <Icon name="checkCircle" size="md" class="mt-0.5 shrink-0" />
              <div>
                <p class="font-medium">{{ t('admin.oneClickAccountNotes.result.title') }}</p>
                <p class="mt-0.5">
                  {{ applyResult.updated_accounts > 0
                    ? t('admin.oneClickAccountNotes.result.success', { count: applyResult.updated_accounts })
                    : t('admin.oneClickAccountNotes.result.noChanges') }}
                </p>
                <p class="mt-1 text-xs" data-test="apply-unprocessed-summary">
                  {{ t('admin.oneClickAccountNotes.result.unprocessedSummary', {
                    unmatched: applyResult.unmatched_lines,
                    invalid: applyResult.invalid_lines,
                    conflicts: applyResult.conflict_lines
                  }) }}
                </p>
              </div>
            </div>
          </section>

          <dl
            v-if="preview"
            ref="previewSummary"
            class="grid grid-cols-2 divide-x divide-y divide-gray-200 overflow-hidden border-y border-gray-200 bg-white focus:outline-none focus:ring-2 focus:ring-inset focus:ring-primary-500 dark:divide-dark-700 dark:border-dark-700 dark:bg-dark-800 sm:grid-cols-3 xl:grid-cols-9"
            tabindex="-1"
            data-test="preview-summary"
          >
            <div v-for="item in summaryItems" :key="item.key" class="min-w-0 px-3 py-2.5">
              <dt class="truncate text-xs text-gray-500 dark:text-gray-400" :title="item.label">{{ item.label }}</dt>
              <dd class="mt-0.5 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ item.value }}</dd>
            </div>
          </dl>

          <section
            v-if="preview && previewError"
            class="border-y border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-900/20 dark:text-red-300"
            role="alert"
            data-test="preview-error"
          >
            {{ previewError }}
          </section>
        </div>
      </template>

      <template #filters>
        <div class="flex flex-col justify-between gap-2 sm:flex-row sm:items-center">
          <div>
            <h2 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ t('admin.oneClickAccountNotes.preview.title') }}
            </h2>
            <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.oneClickAccountNotes.preview.description') }}
            </p>
          </div>
          <span v-if="previewing" class="inline-flex items-center gap-2 text-xs text-primary-600 dark:text-primary-400" role="status">
            <Icon name="refresh" size="sm" class="animate-spin" />
            {{ t('admin.oneClickAccountNotes.preview.loading') }}
          </span>
        </div>
      </template>

      <template #table>
        <DataTable
          :columns="columns"
          :data="pageEntries"
          :loading="previewing"
          row-key="line_number"
          :virtualize-threshold="50"
        >
          <template #cell-line_number="{ value }">
            <span class="tabular-nums text-gray-500 dark:text-gray-400">#{{ value }}</span>
          </template>
          <template #cell-email="{ value }">
            <span class="font-medium text-gray-900 dark:text-white">{{ value || '-' }}</span>
          </template>
          <template #cell-status="{ row }">
            <span class="inline-flex rounded px-2 py-1 text-xs font-medium" :class="statusClass(row.status)">
              {{ statusLabel(row.status) }}
            </span>
          </template>
          <template #cell-matched_accounts="{ value }">
            <span class="tabular-nums">{{ value }}</span>
          </template>
          <template #cell-will_update_accounts="{ value }">
            <span class="tabular-nums">{{ value }}</span>
          </template>
          <template #cell-details="{ row }">
            <span class="block max-w-72 text-xs text-gray-500 dark:text-gray-400">
              {{ entryDetails(row) }}
            </span>
          </template>

          <template #empty>
            <div class="flex flex-col items-center py-12 text-center text-gray-500 dark:text-gray-400">
              <Icon :name="previewError ? 'exclamationTriangle' : 'document'" size="xl" class="mb-3 text-gray-400" />
              <p :class="{ 'text-red-600 dark:text-red-400': previewError }" data-test="preview-empty-message">
                {{ previewError || t('admin.oneClickAccountNotes.preview.empty') }}
              </p>
            </div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="preview && preview.entries.length > 0"
          :page="pagination.page"
          :total="preview.entries.length"
          :page-size="pagination.pageSize"
          @update:page="pagination.page = $event"
          @update:page-size="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <ConfirmDialog
      :show="showApplyConfirm"
      :title="t('admin.oneClickAccountNotes.confirm.title')"
      :message="t('admin.oneClickAccountNotes.confirm.message', { count: preview?.will_update_accounts ?? 0 })"
      :confirm-text="t('admin.oneClickAccountNotes.confirm.apply')"
      :cancel-text="t('admin.oneClickAccountNotes.confirm.cancel')"
      trap-focus
      @confirm="confirmApply"
      @cancel="showApplyConfirm = false"
    >
      <p class="text-xs leading-5 text-amber-700 dark:text-amber-300">
        {{ t('admin.oneClickAccountNotes.confirm.warning') }}
      </p>
    </ConfirmDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, nextTick, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type {
  OneClickAccountNotesApplyResult,
  OneClickAccountNotesEntry,
  OneClickAccountNotesEntryStatus,
  OneClickAccountNotesPreview
} from '@/api/admin/accounts'
import type { Column } from '@/components/common/types'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { useAppStore } from '@/stores'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'

const MAX_FILE_SIZE = 1024 * 1024
const MAX_NON_EMPTY_LINES = 5000

const { t } = useI18n()
const appStore = useAppStore()
const fileInput = ref<HTMLInputElement | null>(null)
const retryPreviewButton = ref<HTMLButtonElement | null>(null)
const applyButton = ref<HTMLButtonElement | null>(null)
const applyResultSummary = ref<HTMLElement | null>(null)
const previewSummary = ref<HTMLElement | null>(null)
const selectedFile = ref<File | null>(null)
const preview = ref<OneClickAccountNotesPreview | null>(null)
const previewIsCurrent = ref(false)
const applyResult = ref<OneClickAccountNotesApplyResult | null>(null)
const previewError = ref('')
const readingFile = ref(false)
const nonEmptyLineCount = ref<number | null>(null)
const previewing = ref(false)
const applying = ref(false)
const dragging = ref(false)
const showApplyConfirm = ref(false)
const pagination = reactive({ page: 1, pageSize: getPersistedPageSize() })

let previewController: AbortController | null = null
let applyController: AbortController | null = null
let dragDepth = 0
let applyIdempotencyKey = ''
let selectionVersion = 0

const columns = computed<Column[]>(() => [
  { key: 'line_number', label: t('admin.oneClickAccountNotes.columns.line') },
  { key: 'email', label: t('admin.oneClickAccountNotes.columns.email') },
  { key: 'status', label: t('admin.oneClickAccountNotes.columns.status') },
  { key: 'matched_accounts', label: t('admin.oneClickAccountNotes.columns.matchedAccounts') },
  { key: 'will_update_accounts', label: t('admin.oneClickAccountNotes.columns.changes') },
  { key: 'details', label: t('admin.oneClickAccountNotes.columns.details') }
])

const summaryItems = computed(() => {
  const value = preview.value
  if (!value) return []
  return [
    { key: 'total', label: t('admin.oneClickAccountNotes.summary.totalLines'), value: value.total_lines },
    { key: 'valid', label: t('admin.oneClickAccountNotes.summary.validLines'), value: value.valid_lines },
    { key: 'matched', label: t('admin.oneClickAccountNotes.summary.matchedAccounts'), value: value.matched_accounts },
    { key: 'updates', label: t('admin.oneClickAccountNotes.summary.willUpdateAccounts'), value: value.will_update_accounts },
    { key: 'unchanged', label: t('admin.oneClickAccountNotes.summary.unchangedAccounts'), value: value.unchanged_accounts },
    { key: 'unmatched', label: t('admin.oneClickAccountNotes.summary.unmatchedLines'), value: value.unmatched_lines },
    { key: 'invalid', label: t('admin.oneClickAccountNotes.summary.invalidLines'), value: value.invalid_lines },
    { key: 'duplicates', label: t('admin.oneClickAccountNotes.summary.duplicateLines'), value: value.duplicate_lines },
    { key: 'conflicts', label: t('admin.oneClickAccountNotes.summary.conflictLines'), value: value.conflict_lines }
  ]
})

const pageEntries = computed(() => {
  const entries = preview.value?.entries ?? []
  const start = (pagination.page - 1) * pagination.pageSize
  return entries.slice(start, start + pagination.pageSize)
})

const canOpenConfirmation = computed(() => Boolean(
  selectedFile.value
  && previewIsCurrent.value
  && preview.value?.can_apply
  && preview.value.preview_digest
  && preview.value.will_update_accounts > 0
  && !readingFile.value
  && !previewing.value
  && !applying.value
  && !applyResult.value
))

function openFilePicker() {
  if (applying.value) return
  if (fileInput.value) fileInput.value.value = ''
  fileInput.value?.click()
}

function handleFileChange(event: Event) {
  const files = (event.target as HTMLInputElement).files
  if (files?.length) selectFiles(files)
}

function handleDragEnter() {
  if (applying.value) return
  dragDepth += 1
  dragging.value = true
}

function handleDragLeave() {
  dragDepth = Math.max(0, dragDepth - 1)
  dragging.value = dragDepth > 0
}

function handleDrop(event: DragEvent) {
  dragDepth = 0
  dragging.value = false
  if (applying.value || !event.dataTransfer?.files.length) return
  selectFiles(event.dataTransfer.files)
}

function selectFiles(files: FileList) {
  if (files.length !== 1) {
    clearSelection()
    appStore.showError(t('admin.oneClickAccountNotes.errors.singleFileOnly'))
    return
  }
  selectFile(files[0])
}

function selectFile(file: File) {
  clearSelection()
  if (!file.name.toLocaleLowerCase().endsWith('.txt')) {
    appStore.showError(t('admin.oneClickAccountNotes.errors.invalidFileType'))
    return
  }
  if (file.size === 0) {
    appStore.showError(t('admin.oneClickAccountNotes.errors.emptyFile'))
    return
  }
  if (file.size > MAX_FILE_SIZE) {
    appStore.showError(t('admin.oneClickAccountNotes.errors.fileTooLarge'))
    return
  }

  selectedFile.value = file
  const version = selectionVersion
  void validateAndPreviewFile(file, version)
}

function clearSelection() {
  selectionVersion += 1
  previewController?.abort()
  applyController?.abort()
  previewController = null
  applyController = null
  previewing.value = false
  applying.value = false
  selectedFile.value = null
  readingFile.value = false
  nonEmptyLineCount.value = null
  preview.value = null
  previewIsCurrent.value = false
  applyResult.value = null
  applyIdempotencyKey = ''
  previewError.value = ''
  showApplyConfirm.value = false
  pagination.page = 1
  dragDepth = 0
  dragging.value = false
  if (fileInput.value) fileInput.value.value = ''
}

async function validateAndPreviewFile(file: File, version: number) {
  readingFile.value = true

  try {
    const content = await readFileText(file)
    if (!isCurrentSelection(file, version)) return

    const lineCount = countNonEmptyLines(content)
    if (lineCount === 0) {
      clearSelection()
      appStore.showError(t('admin.oneClickAccountNotes.errors.emptyFile'))
      return
    }
    if (lineCount > MAX_NON_EMPTY_LINES) {
      clearSelection()
      appStore.showError(t('admin.oneClickAccountNotes.errors.ACCOUNT_NOTE_IMPORT_TOO_MANY_LINES'))
      return
    }

    nonEmptyLineCount.value = lineCount
  } catch {
    if (!isCurrentSelection(file, version)) return
    clearSelection()
    appStore.showError(t('admin.oneClickAccountNotes.errors.readFailed'))
    return
  } finally {
    if (isCurrentSelection(file, version)) readingFile.value = false
  }

  if (isCurrentSelection(file, version)) void runPreview(file)
}

function isCurrentSelection(file: File, version: number): boolean {
  return selectionVersion === version && selectedFile.value === file
}

function countNonEmptyLines(content: string): number {
  let count = 0
  for (const line of content.split(/\r\n|\n|\r/)) {
    if (line.trim() === '') continue
    count += 1
    if (count > MAX_NON_EMPTY_LINES) break
  }
  return count
}

function readFileText(file: File): Promise<string> {
  if (typeof file.text === 'function') return file.text()

  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(typeof reader.result === 'string' ? reader.result : '')
    reader.onerror = () => reject(reader.error ?? new Error('file read failed'))
    reader.onabort = () => reject(new Error('file read aborted'))
    reader.readAsText(file)
  })
}

interface RunPreviewOptions {
  preservePreview?: boolean
  restoreWorkflowFocus?: boolean
}

async function runPreview(file: File, options: RunPreviewOptions = {}) {
  previewController?.abort()
  const controller = new AbortController()
  previewController = controller
  previewing.value = true
  previewError.value = ''
  previewIsCurrent.value = false
  if (!options.preservePreview) preview.value = null
  applyResult.value = null
  applyIdempotencyKey = ''
  pagination.page = 1
  let succeeded = false

  try {
    const result = await adminAPI.accounts.previewOneClickAccountNotes(file, { signal: controller.signal })
    if (controller.signal.aborted || selectedFile.value !== file) return
    preview.value = result
    previewIsCurrent.value = true
    applyIdempotencyKey = createApplyIdempotencyKey()
    succeeded = true
    appStore.showSuccess(t('admin.oneClickAccountNotes.messages.previewReady', { count: result.matched_accounts }))
  } catch (error) {
    if (controller.signal.aborted) return
    previewError.value = oneClickAccountNotesErrorMessage(error, t('admin.oneClickAccountNotes.errors.previewFailed'))
    appStore.showError(previewError.value)
  } finally {
    if (previewController === controller) {
      previewController = null
      previewing.value = false
    }
  }

  if (options.restoreWorkflowFocus && !controller.signal.aborted && selectedFile.value === file) {
    await nextTick()
    if (succeeded) {
      const button = applyButton.value
      if (canOpenConfirmation.value && button && !button.disabled) {
        button.focus()
      } else {
        previewSummary.value?.focus()
      }
    } else {
      retryPreviewButton.value?.focus()
    }
  }
}

function retryPreview() {
  const file = selectedFile.value
  if (!file || previewing.value || applying.value) return
  void runPreview(file, {
    preservePreview: Boolean(preview.value),
    restoreWorkflowFocus: true
  })
}

async function confirmApply() {
  showApplyConfirm.value = false
  const file = selectedFile.value
  const currentPreview = preview.value
  if (
    !file
    || !previewIsCurrent.value
    || !currentPreview?.can_apply
    || !currentPreview.preview_digest
    || !applyIdempotencyKey
    || applying.value
  ) return

  const controller = new AbortController()
  applyController = controller
  applying.value = true
  let outcome: 'none' | 'success' | 'failure' | 'stale' = 'none'
  try {
    const result = await adminAPI.accounts.applyOneClickAccountNotes(
      file,
      currentPreview.preview_digest,
      applyIdempotencyKey,
      { signal: controller.signal }
    )
    if (controller.signal.aborted || selectedFile.value !== file) return
    applyResult.value = result
    outcome = 'success'
    appStore.showSuccess(t('admin.oneClickAccountNotes.messages.applySuccess', { count: result.updated_accounts }))
  } catch (error) {
    if (controller.signal.aborted) return
    if (extractApiErrorCode(error) === 'ACCOUNT_NOTE_IMPORT_PREVIEW_STALE' && selectedFile.value === file) {
      appStore.showWarning(oneClickAccountNotesErrorMessage(error, t('admin.oneClickAccountNotes.errors.applyFailed')))
      outcome = 'stale'
    } else {
      outcome = 'failure'
      appStore.showError(oneClickAccountNotesErrorMessage(error, t('admin.oneClickAccountNotes.errors.applyFailed')))
    }
  } finally {
    if (applyController === controller) {
      applyController = null
      applying.value = false
    }
  }

  if (controller.signal.aborted || selectedFile.value !== file) return
  if (outcome === 'stale') {
    await runPreview(file, { preservePreview: true, restoreWorkflowFocus: true })
    return
  }

  await nextTick()
  if (outcome === 'success') {
    applyResultSummary.value?.focus()
  } else if (outcome === 'failure') {
    applyButton.value?.focus()
  }
}

function createApplyIdempotencyKey(): string {
  const requestID = globalThis.crypto?.randomUUID?.()
    ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`
  return `account-note-import-${requestID}`
}

function oneClickAccountNotesErrorMessage(error: unknown, fallback: string): string {
  return extractI18nErrorMessage(error, t, 'admin.oneClickAccountNotes.errors', fallback)
}

function statusLabel(status: OneClickAccountNotesEntryStatus): string {
  return t(`admin.oneClickAccountNotes.statuses.${status}`)
}

function statusClass(status: OneClickAccountNotesEntryStatus): string {
  if (status === 'matched') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300'
  if (status === 'unmatched') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  if (status === 'duplicate') return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
}

function entryDetails(entry: OneClickAccountNotesEntry): string {
  if (entry.reason === 'missing_email') {
    return t('admin.oneClickAccountNotes.reasons.missingEmail')
  }
  if (entry.reason === 'duplicate_email_conflict') {
    return t('admin.oneClickAccountNotes.reasons.duplicateEmailConflict')
  }
  if (entry.duplicate_of_line != null) {
    return t('admin.oneClickAccountNotes.details.duplicateOfLine', { line: entry.duplicate_of_line })
  }
  if (entry.reason === 'duplicate_identical_line') {
    return t('admin.oneClickAccountNotes.reasons.duplicateIdenticalLine')
  }
  if (entry.matched_accounts > 1) {
    return t('admin.oneClickAccountNotes.details.matchedAccounts', { count: entry.matched_accounts })
  }
  if (entry.matched_accounts > 0 && entry.will_update_accounts === 0) {
    return t('admin.oneClickAccountNotes.details.unchangedAccounts', { count: entry.matched_accounts })
  }
  return '-'
}

function formatFileSize(size: number): string {
  if (size < 1024) return `${size} B`
  return `${(size / 1024).toFixed(size < 10240 ? 1 : 0)} KB`
}

function handlePageSizeChange(pageSize: number) {
  pagination.pageSize = pageSize
  pagination.page = 1
}

onUnmounted(() => {
  selectionVersion += 1
  previewController?.abort()
  applyController?.abort()
})
</script>

<style scoped>
@media (min-width: 1024px) {
  .one-click-account-notes-layout :deep(.layout-section-scrollable) {
    min-height: 20rem;
  }
}
</style>
