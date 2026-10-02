<template>
  <AppLayout>
    <TablePageLayout>
      <template #actions>
        <div class="space-y-3">
          <div
            class="flex flex-col gap-3 border-y border-gray-200 bg-white px-4 py-3 dark:border-dark-700 dark:bg-dark-800 lg:flex-row lg:items-end"
          >
            <div class="flex min-h-10 items-center gap-3 lg:pb-0.5">
              <Toggle v-model="configForm.enabled" />
              <div>
                <p class="text-sm font-medium text-gray-900 dark:text-white">
                  {{ t('admin.plusQuotaAutomation.config.enabled') }}
                </p>
                <p class="text-xs text-gray-500 dark:text-gray-400">
                  {{ runtimeLabel }}
                </p>
              </div>
            </div>

            <div class="min-w-0 flex-1 lg:max-w-xs">
              <label class="input-label">{{ t('admin.plusQuotaAutomation.config.group') }}</label>
              <Select
                v-model="configForm.group_id"
                :options="groupOptions"
                :disabled="groupsLoading"
                searchable
              />
            </div>

            <div class="w-full sm:w-36">
              <label class="input-label">{{ t('admin.plusQuotaAutomation.config.intervalMinutes') }}</label>
              <input
                v-model.number="configForm.interval_minutes"
                type="number"
                min="1"
                max="1440"
                step="1"
                class="input"
              />
            </div>

            <div class="w-full sm:w-36">
              <label class="input-label">{{ t('admin.plusQuotaAutomation.config.threshold') }}</label>
              <div class="relative">
                <input
                  v-model.number="configForm.utilization_threshold"
                  type="number"
                  min="1"
                  max="100"
                  step="1"
                  class="input pr-8"
                />
                <span class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-gray-400">%</span>
              </div>
            </div>

            <div class="flex flex-wrap items-center gap-2">
              <button
                type="button"
                class="btn btn-secondary"
                :disabled="savingConfig || overviewLoading || !configValid"
                @click="saveConfig"
              >
                <Icon
                  :name="savingConfig ? 'refresh' : 'check'"
                  size="sm"
                  :class="{ 'animate-spin': savingConfig }"
                />
                <span>{{ t('common.save') }}</span>
              </button>
              <button
                type="button"
                class="btn btn-primary"
                :disabled="runSubmitting || automationRunning || overviewLoading || !canRunAutomation || deleteScopeLocked"
                @click="showRunConfirm = true"
              >
                <Icon
                  :name="runSubmitting || automationRunning ? 'refresh' : 'play'"
                  size="sm"
                  :class="{ 'animate-spin': runSubmitting || automationRunning }"
                />
                <span>{{ t('admin.plusQuotaAutomation.actions.runNow') }}</span>
              </button>
            </div>
          </div>

          <div
            class="grid grid-cols-2 divide-x divide-y divide-gray-200 overflow-hidden border-y border-gray-200 bg-white dark:divide-dark-700 dark:border-dark-700 dark:bg-dark-800 sm:grid-cols-4 xl:grid-cols-8"
          >
            <div
              v-for="item in summaryItems"
              :key="item.key"
              class="min-w-0 px-3 py-2.5"
            >
              <dt class="truncate text-xs text-gray-500 dark:text-gray-400">{{ item.label }}</dt>
              <dd class="mt-0.5 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">
                {{ item.value }}
              </dd>
            </div>
          </div>

          <div class="flex flex-wrap items-center gap-x-5 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
            <span class="inline-flex items-center gap-1.5">
              <span class="h-2 w-2 rounded-full" :class="runtimeDotClass"></span>
              {{ runtimeLabel }}
            </span>
            <span>{{ t('admin.plusQuotaAutomation.runtime.lastRun') }}: {{ lastRunText }}</span>
            <span>{{ t('admin.plusQuotaAutomation.runtime.trigger') }}: {{ triggerLabel }}</span>
            <span>{{ t('admin.plusQuotaAutomation.runtime.nextRun') }}: {{ formatTimestamp(overview?.next_run_at) }}</span>
            <span v-if="overview?.state.cooldown">
              {{ t('admin.plusQuotaAutomation.runtime.cooldown', { count: overview.state.cooldown }) }}
            </span>
            <span
              v-if="overview?.state.last_error"
              class="max-w-full truncate text-red-600 dark:text-red-400"
              :title="overview.state.last_error"
            >
              {{ overview.state.last_error }}
            </span>
            <span
              v-else-if="overview?.state.skipped_reason"
              class="max-w-full truncate"
              :title="overview.state.skipped_reason"
            >
              {{ overview.state.skipped_reason }}
            </span>
          </div>
        </div>
      </template>

      <template #filters>
        <div class="flex flex-col justify-between gap-3 sm:flex-row sm:items-center">
          <div class="flex flex-1 flex-col gap-3 sm:flex-row sm:items-center">
            <fieldset
              class="contents"
              :disabled="deleteScopeLocked"
            >
              <SearchInput
                v-model="filters.search"
                class="w-full sm:w-72"
                :class="{ 'opacity-60': deleteScopeLocked }"
                :placeholder="t('admin.plusQuotaAutomation.filters.searchPlaceholder')"
                @search="handleSearch"
              />
            </fieldset>
            <Select
              v-model="filters.status"
              class="w-full sm:w-40"
              :options="statusOptions"
              :disabled="deleteScopeLocked"
              @change="handleStatusChange"
            />
          </div>
          <div class="flex flex-wrap items-center justify-end gap-2 self-end sm:self-auto">
            <button
              type="button"
              class="btn btn-danger"
              data-test="delete-all-anomaly-accounts"
              :class="{ 'cursor-not-allowed opacity-60': !canDeleteAllAccounts }"
              :disabled="deleteAllButtonDisabled"
              :aria-disabled="!canDeleteAllAccounts"
              :aria-busy="bulkDeleteBusy"
              :title="filters.status === 'open'
                ? t('admin.plusQuotaAutomation.actions.deleteAllAccounts')
                : t('admin.plusQuotaAutomation.actions.deleteAllOpenOnly')"
              @click="requestDeleteAllAccounts"
            >
              <Icon
                :name="bulkDeleteBusy ? 'refresh' : 'trash'"
                size="sm"
                :class="{ 'animate-spin': bulkDeleteBusy }"
              />
              <span aria-live="polite">{{ deleteAllButtonLabel }}</span>
            </button>
            <button
              type="button"
              class="btn btn-secondary"
              data-test="export-account-notes"
              :disabled="exportingNotes || deleteScopeLocked"
              @click="exportAccountNotes"
            >
              <Icon
                :name="exportingNotes ? 'refresh' : 'download'"
                size="sm"
                :class="{ 'animate-spin': exportingNotes }"
              />
              <span>
                {{
                  exportingNotes
                    ? t('admin.plusQuotaAutomation.actions.exportingNotes')
                    : t('admin.plusQuotaAutomation.actions.exportNotes')
                }}
              </span>
            </button>
            <button
              type="button"
              class="btn btn-secondary"
              :disabled="overviewLoading || anomaliesLoading || deleteScopeLocked"
              :title="t('common.refresh')"
              @click="refreshAll"
            >
              <Icon
                name="refresh"
                size="md"
                :class="{ 'animate-spin': overviewLoading || anomaliesLoading }"
              />
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable
          :columns="columns"
          :data="anomalies"
          :loading="anomaliesLoading"
          row-key="account_id"
          :actions-count="2"
        >
          <template #cell-email="{ row }">
            <div class="min-w-0 sm:min-w-44">
              <p
                class="break-all font-medium text-gray-900 dark:text-white sm:truncate sm:break-normal"
                :title="row.email || ''"
              >
                {{ row.email || '-' }}
              </p>
              <p
                class="mt-0.5 text-xs text-gray-500 dark:text-gray-400 sm:truncate"
                :title="`${row.account_name || '-'} / #${row.account_id}`"
              >
                <span class="break-all sm:break-normal">{{ row.account_name || '-' }}</span>
                <span class="whitespace-nowrap"> / #{{ row.account_id }}</span>
              </p>
            </div>
          </template>

          <template #cell-group_id="{ value }">
            <span class="whitespace-nowrap">{{ groupName(value) }}</span>
          </template>

          <template #cell-stage="{ value }">
            <span class="inline-flex rounded bg-gray-100 px-2 py-1 text-xs text-gray-700 dark:bg-dark-700 dark:text-gray-300">
              {{ stageLabel(value) }}
            </span>
          </template>

          <template #cell-http_status="{ value }">
            <span class="inline-flex rounded bg-orange-100 px-2 py-1 text-xs font-medium text-orange-700 dark:bg-orange-900/40 dark:text-orange-300">
              {{ value }}
            </span>
          </template>

          <template #cell-first_detected_at="{ value }">
            <span class="whitespace-nowrap text-xs">{{ formatTimestamp(value) }}</span>
          </template>

          <template #cell-last_detected_at="{ value }">
            <span class="whitespace-nowrap text-xs">{{ formatTimestamp(value) }}</span>
          </template>

          <template #cell-count="{ value }">
            <span class="tabular-nums">{{ value }}</span>
          </template>

          <template #cell-status="{ row }">
            <div class="space-y-1">
              <span
                class="inline-flex rounded px-2 py-1 text-xs font-medium"
                :class="anomalyStatusClass(row.status)"
              >
                {{ anomalyStatusLabel(row.status) }}
              </span>
              <p v-if="row.resolved_at" class="whitespace-nowrap text-[11px] text-gray-500 dark:text-gray-400">
                {{ formatTimestamp(row.resolved_at) }}
              </p>
            </div>
          </template>

          <template #cell-last_error="{ value }">
            <span
              class="block max-w-sm truncate text-xs text-red-600 dark:text-red-400"
              :title="value || ''"
            >
              {{ value || '-' }}
            </span>
          </template>

          <template #cell-actions="{ row }">
            <div v-if="row.status === 'open'" class="flex flex-wrap items-center gap-1.5">
              <button
                type="button"
                class="btn btn-secondary px-2.5 py-1.5 text-xs"
                :disabled="deleteScopeLocked || resolvingAccountIds.has(row.account_id) || deletingAccountIds.has(row.account_id)"
                :title="t('admin.plusQuotaAutomation.actions.resolve')"
                @click="resolveRow(row)"
              >
                <Icon
                  :name="resolvingAccountIds.has(row.account_id) ? 'refresh' : 'checkCircle'"
                  size="sm"
                  :class="{ 'animate-spin': resolvingAccountIds.has(row.account_id) }"
                />
                <span>{{ t('admin.plusQuotaAutomation.actions.resolve') }}</span>
              </button>
              <button
                v-if="row.http_status === 401"
                type="button"
                class="btn btn-danger px-2.5 py-1.5 text-xs"
                data-test="delete-anomaly-account"
                :disabled="deleteScopeLocked || resolvingAccountIds.has(row.account_id) || deletingAccountIds.has(row.account_id)"
                :aria-busy="deletingAccountIds.has(row.account_id)"
                :aria-label="t(
                  deletingAccountIds.has(row.account_id)
                    ? 'admin.plusQuotaAutomation.actions.deletingAccount'
                    : 'admin.plusQuotaAutomation.actions.deleteAccount'
                )"
                :title="t(
                  deletingAccountIds.has(row.account_id)
                    ? 'admin.plusQuotaAutomation.actions.deletingAccount'
                    : 'admin.plusQuotaAutomation.actions.deleteAccount'
                )"
                @click="requestDeleteAccount(row)"
              >
                <Icon
                  :name="deletingAccountIds.has(row.account_id) ? 'refresh' : 'trash'"
                  size="sm"
                  :class="{ 'animate-spin': deletingAccountIds.has(row.account_id) }"
                />
                <span>{{ t('admin.plusQuotaAutomation.actions.deleteAccount') }}</span>
              </button>
            </div>
            <span v-else class="text-xs text-gray-400">-</span>
          </template>

          <template #empty>
            <div class="flex flex-col items-center py-12 text-gray-500 dark:text-gray-400">
              <Icon name="checkCircle" size="xl" class="mb-3 text-emerald-500" />
              <p>{{ t('admin.plusQuotaAutomation.empty') }}</p>
            </div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <fieldset
          v-if="pagination.total > 0"
          class="contents"
          data-test="anomaly-pagination-lock"
          :disabled="deleteScopeLocked"
        >
          <Pagination
            :class="{ 'opacity-60': deleteScopeLocked }"
            :page="pagination.page"
            :total="pagination.total"
            :page-size="pagination.page_size"
            @update:page="handlePageChange"
            @update:pageSize="handlePageSizeChange"
          />
        </fieldset>
      </template>
    </TablePageLayout>

    <ConfirmDialog
      :show="showRunConfirm"
      :title="t('admin.plusQuotaAutomation.runConfirm.title')"
      :message="t('admin.plusQuotaAutomation.runConfirm.message')"
      :confirm-text="t('admin.plusQuotaAutomation.actions.runNow')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmRun"
      @cancel="showRunConfirm = false"
    />
    <ConfirmDialog
      :show="showDeleteConfirm"
      :title="t('admin.plusQuotaAutomation.deleteConfirm.title')"
      :message="deleteConfirmationMessage"
      :confirm-text="t('admin.plusQuotaAutomation.actions.deleteAccount')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmDeleteAccount"
      @cancel="cancelDeleteAccount"
    />
    <ConfirmDialog
      :show="showDeleteAllConfirm"
      :title="t('admin.plusQuotaAutomation.deleteAllConfirm.title')"
      :message="deleteAllConfirmationMessage"
      :confirm-text="t('admin.plusQuotaAutomation.actions.deleteAllAccounts')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmDeleteAllAccounts"
      @cancel="cancelDeleteAllAccounts"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type {
  PlusQuotaAnomaly,
  PlusQuotaAnomalyStatus,
  PlusQuotaAnomalyStatusFilter,
  PlusQuotaAutomationOverview
} from '@/api/admin/plusQuotaAutomation'
import type { AdminGroup } from '@/types'
import type { Column } from '@/components/common/types'
import { useAppStore } from '@/stores'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { formatDateTimeToMinute } from '@/utils/format'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import SearchInput from '@/components/common/SearchInput.vue'
import Select from '@/components/common/Select.vue'
import Toggle from '@/components/common/Toggle.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const appStore = useAppStore()

const BULK_DELETE_CONCURRENCY = 4
type BulkDeleteOutcome = 'deleted' | 'stale' | 'failed'

const overview = ref<PlusQuotaAutomationOverview | null>(null)
const groups = ref<AdminGroup[]>([])
const anomalies = ref<PlusQuotaAnomaly[]>([])
const overviewLoading = ref(false)
const groupsLoading = ref(false)
const anomaliesLoading = ref(false)
const exportingNotes = ref(false)
const savingConfig = ref(false)
const runSubmitting = ref(false)
const showRunConfirm = ref(false)
const showDeleteConfirm = ref(false)
const deleteCandidate = ref<PlusQuotaAnomaly | null>(null)
const preparingDeleteAll = ref(false)
const deletingAllAccounts = ref(false)
const showDeleteAllConfirm = ref(false)
const deleteAllCandidates = ref<number[]>([])
const deleteAllProgress = reactive({ completed: 0, total: 0 })
const resolvingAccountIds = reactive(new Set<number>())
const deletingAccountIds = reactive(new Set<number>())

const configForm = reactive({
  enabled: false,
  group_id: 0,
  interval_minutes: 60,
  utilization_threshold: 100
})

const filters = reactive<{
  status: PlusQuotaAnomalyStatusFilter
  search: string
}>({
  status: 'open',
  search: ''
})

const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0
})

const columns = computed<Column[]>(() => [
  { key: 'email', label: t('admin.plusQuotaAutomation.columns.email') },
  { key: 'group_id', label: t('admin.plusQuotaAutomation.columns.group') },
  { key: 'stage', label: t('admin.plusQuotaAutomation.columns.stage') },
  { key: 'http_status', label: t('admin.plusQuotaAutomation.columns.httpStatus') },
  { key: 'first_detected_at', label: t('admin.plusQuotaAutomation.columns.firstDetected') },
  { key: 'last_detected_at', label: t('admin.plusQuotaAutomation.columns.lastDetected') },
  { key: 'count', label: t('admin.plusQuotaAutomation.columns.count') },
  { key: 'status', label: t('admin.plusQuotaAutomation.columns.status') },
  { key: 'last_error', label: t('admin.plusQuotaAutomation.columns.lastError') },
  { key: 'actions', label: t('admin.plusQuotaAutomation.columns.actions') }
])

const statusOptions = computed(() => [
  { value: 'open', label: t('admin.plusQuotaAutomation.status.open') },
  { value: 'resolved', label: t('admin.plusQuotaAutomation.status.resolved') },
  { value: 'all', label: t('admin.plusQuotaAutomation.status.all') }
])

const groupOptions = computed(() => [
  {
    value: 0,
    label: groupsLoading.value
      ? t('common.loading')
      : t('admin.plusQuotaAutomation.config.selectGroup'),
    disabled: true
  },
  ...groups.value.map((group) => ({
    value: group.id,
    label: group.status === 'inactive'
      ? `${group.name} (${t('common.inactive')})`
      : group.name,
    disabled: group.status === 'inactive'
  }))
])

const groupNames = computed(() => new Map(groups.value.map((group) => [group.id, group.name])))
const selectedGroup = computed(() => groups.value.find((group) => group.id === configForm.group_id))
const deleteConfirmationMessage = computed(() => {
  const row = deleteCandidate.value
  if (!row) return ''
  return t('admin.plusQuotaAutomation.deleteConfirm.message', {
    name: row.account_name || '-',
    email: row.email || '-',
    id: row.account_id
  })
})
const automationRunning = computed(() => overview.value?.state.running === true)
const bulkDeleteBusy = computed(() => preparingDeleteAll.value || deletingAllAccounts.value)
const deleteScopeLocked = computed(() => bulkDeleteBusy.value || showDeleteAllConfirm.value)
const canDeleteAllAccounts = computed(() =>
  filters.status === 'open'
  && pagination.total > 0
  && !anomaliesLoading.value
  && !exportingNotes.value
  && !automationRunning.value
  && !runSubmitting.value
  && !deleteScopeLocked.value
  && resolvingAccountIds.size === 0
  && deletingAccountIds.size === 0
)
const deleteAllButtonDisabled = computed(() =>
  filters.status !== 'open' || pagination.total <= 0
)
const deleteAllButtonLabel = computed(() => {
  if (preparingDeleteAll.value) {
    return t('admin.plusQuotaAutomation.actions.preparingDeleteAllAccounts')
  }
  if (deletingAllAccounts.value) {
    return t('admin.plusQuotaAutomation.actions.deletingAllAccounts', {
      completed: deleteAllProgress.completed,
      total: deleteAllProgress.total
    })
  }
  return t('admin.plusQuotaAutomation.actions.deleteAllAccounts')
})
const deleteAllConfirmationMessage = computed(() =>
  t('admin.plusQuotaAutomation.deleteAllConfirm.message', {
    count: deleteAllCandidates.value.length
  })
)

const configValid = computed(() =>
  Number.isInteger(configForm.group_id)
  && configForm.group_id > 0
  && (!configForm.enabled || selectedGroup.value?.status === 'active')
  && Number.isFinite(configForm.interval_minutes)
  && configForm.interval_minutes >= 1
  && configForm.interval_minutes <= 1440
  && Number.isFinite(configForm.utilization_threshold)
  && configForm.utilization_threshold >= 1
  && configForm.utilization_threshold <= 100
)

const canRunAutomation = computed(() => {
  const configuredGroupId = overview.value?.config.group_id ?? 0
  return configuredGroupId > 0
    && groups.value.some((group) => group.id === configuredGroupId && group.status === 'active')
})

const runtimeLabel = computed(() => {
  if (automationRunning.value) return t('admin.plusQuotaAutomation.runtime.running')
  if (overview.value?.config.enabled) return t('admin.plusQuotaAutomation.runtime.waiting')
  return t('admin.plusQuotaAutomation.runtime.disabled')
})

const runtimeDotClass = computed(() => {
  if (automationRunning.value) return 'bg-blue-500 animate-pulse'
  if (overview.value?.config.enabled) return 'bg-emerald-500'
  return 'bg-gray-400'
})

const triggerLabel = computed(() => {
  const trigger = overview.value?.state.trigger?.trim()
  if (!trigger) return '-'
  if (trigger === 'manual') return t('admin.plusQuotaAutomation.trigger.manual')
  if (trigger === 'scheduled' || trigger === 'schedule') {
    return t('admin.plusQuotaAutomation.trigger.scheduled')
  }
  return trigger
})

const lastRunText = computed(() => {
  const state = overview.value?.state
  if (!state) return '-'
  return formatTimestamp(state.running ? state.started_at : state.completed_at || state.started_at)
})

const summaryItems = computed(() => {
  const state = overview.value?.state
  return [
    { key: 'scanned', label: t('admin.plusQuotaAutomation.summary.scanned'), value: state?.scanned ?? 0 },
    { key: 'eligible', label: t('admin.plusQuotaAutomation.summary.eligible'), value: state?.eligible ?? 0 },
    { key: 'atLimit', label: t('admin.plusQuotaAutomation.summary.atLimit'), value: state?.at_limit ?? 0 },
    { key: 'reset', label: t('admin.plusQuotaAutomation.summary.reset'), value: state?.reset_count ?? 0 },
    { key: 'unauthorized', label: t('admin.plusQuotaAutomation.summary.unauthorized'), value: state?.unauthorized ?? 0 },
    { key: 'noCredits', label: t('admin.plusQuotaAutomation.summary.noCredits'), value: state?.no_credits ?? 0 },
    { key: 'failed', label: t('admin.plusQuotaAutomation.summary.failed'), value: state?.failed ?? 0 },
    { key: 'skipped', label: t('admin.plusQuotaAutomation.summary.skipped'), value: state?.skipped ?? 0 }
  ]
})

let anomaliesAbortController: AbortController | null = null
let notesExportAbortController: AbortController | null = null
let deleteAllAbortController: AbortController | null = null
let overviewPollTimer: number | null = null
let overviewRequestSequence = 0
let foregroundOverviewRequests = 0
let configFormInitialized = false
let isUnmounted = false
let anomalyReloadPending = false

function apiErrorStatus(error: unknown): number | undefined {
  const candidate = error as { status?: number; response?: { status?: number } }
  return candidate?.status ?? candidate?.response?.status
}

function apiErrorMessage(error: unknown, fallback: string): string {
  const candidate = error as {
    message?: string
    reason?: string
    response?: { data?: { message?: string; detail?: string } }
  }
  return (
    candidate?.message
    || candidate?.reason
    || candidate?.response?.data?.message
    || candidate?.response?.data?.detail
    || fallback
  )
}

function syncConfigForm(config: PlusQuotaAutomationOverview['config']) {
  configForm.enabled = config.enabled
  configForm.group_id = config.group_id
  configForm.interval_minutes = Math.max(1, Math.round(config.interval_seconds / 60))
  configForm.utilization_threshold = config.utilization_threshold
}

function stopOverviewPolling() {
  if (overviewPollTimer !== null) {
    window.clearTimeout(overviewPollTimer)
    overviewPollTimer = null
  }
}

function scheduleOverviewPoll() {
  stopOverviewPolling()
  if (isUnmounted || !automationRunning.value) return
  overviewPollTimer = window.setTimeout(async () => {
    const wasRunning = automationRunning.value
    await loadOverview(false, true)
    if (wasRunning && !automationRunning.value) {
      await loadAnomalies()
    }
  }, 3000)
}

async function loadOverview(syncForm = false, silent = false) {
  const requestSequence = ++overviewRequestSequence
  if (!silent) {
    foregroundOverviewRequests += 1
    overviewLoading.value = true
  }
  try {
    const result = await adminAPI.plusQuotaAutomation.getAutomation()
    if (isUnmounted || requestSequence !== overviewRequestSequence) return
    overview.value = result
    if (syncForm || !configFormInitialized) {
      syncConfigForm(result.config)
      configFormInitialized = true
    }
    scheduleOverviewPoll()
  } catch (error) {
    if (isUnmounted || requestSequence !== overviewRequestSequence) return
    if (!silent) {
      appStore.showError(
        apiErrorMessage(error, t('admin.plusQuotaAutomation.messages.loadOverviewFailed'))
      )
    }
    scheduleOverviewPoll()
  } finally {
    if (!silent) {
      foregroundOverviewRequests = Math.max(0, foregroundOverviewRequests - 1)
      overviewLoading.value = foregroundOverviewRequests > 0
    }
  }
}

async function loadGroups() {
  if (isUnmounted) return
  groupsLoading.value = true
  try {
    const availableGroups = await adminAPI.groups.getAllIncludingInactive('openai')
    if (isUnmounted) return
    const configuredGroupId = configForm.group_id
    groups.value = availableGroups.filter((group) =>
      group.status === 'active' || group.id === configuredGroupId
    )
  } catch (error) {
    if (isUnmounted) return
    appStore.showError(
      apiErrorMessage(error, t('admin.plusQuotaAutomation.messages.loadGroupsFailed'))
    )
  } finally {
    if (!isUnmounted) groupsLoading.value = false
  }
}

async function loadAnomalies() {
  anomaliesAbortController?.abort()
  const controller = new AbortController()
  anomaliesAbortController = controller
  anomaliesLoading.value = true
  const requestedPage = pagination.page
  const requestedPageSize = pagination.page_size
  try {
    const result = await adminAPI.plusQuotaAutomation.listAnomalies(
      {
        page: requestedPage,
        page_size: requestedPageSize,
        status: filters.status,
        search: filters.search.trim() || undefined
      },
      { signal: controller.signal }
    )
    if (controller.signal.aborted || anomaliesAbortController !== controller) return
    const resultPageSize = result.page_size || requestedPageSize
    const resultTotal = result.total || 0
    const lastPage = Math.max(1, Math.ceil(resultTotal / resultPageSize))
    if (requestedPage > lastPage) {
      pagination.page = lastPage
      await loadAnomalies()
      return
    }
    anomalies.value = result.items || []
    pagination.total = resultTotal
    pagination.page = result.page || 1
    pagination.page_size = resultPageSize
  } catch (error) {
    if (controller.signal.aborted) return
    appStore.showError(
      apiErrorMessage(error, t('admin.plusQuotaAutomation.messages.loadAnomaliesFailed'))
    )
  } finally {
    if (anomaliesAbortController === controller) {
      anomaliesLoading.value = false
      anomaliesAbortController = null
    }
  }
}

function accountNotesExportTimestamp(): string {
  return new Date().toISOString().replace(/\D/g, '').slice(0, 14)
}

async function exportAccountNotes() {
  if (exportingNotes.value || deleteScopeLocked.value) return

  const controller = new AbortController()
  notesExportAbortController = controller
  exportingNotes.value = true
  try {
    const result = await adminAPI.plusQuotaAutomation.exportAnomalyNotes({
      signal: controller.signal
    })
    if (controller.signal.aborted || isUnmounted) return

    if (!result.blob) {
      appStore.showWarning(t('admin.plusQuotaAutomation.messages.noAccountNotesToExport'))
      return
    }

    const url = window.URL.createObjectURL(result.blob)
    const link = document.createElement('a')
    link.href = url
    link.download = result.filename
      || `sub2api-plus-anomaly-account-notes-${accountNotesExportTimestamp()}.txt`
    try {
      link.click()
    } finally {
      window.URL.revokeObjectURL(url)
    }
    appStore.showSuccess(
      t('admin.plusQuotaAutomation.messages.accountNotesExported', { count: result.count })
    )
  } catch (error) {
    if (controller.signal.aborted || isUnmounted) return
    appStore.showError(
      apiErrorMessage(error, t('admin.plusQuotaAutomation.messages.exportAccountNotesFailed'))
    )
  } finally {
    if (notesExportAbortController === controller) {
      notesExportAbortController = null
      exportingNotes.value = false
    }
  }
}

async function saveConfig() {
  if (!configValid.value || savingConfig.value) return
  savingConfig.value = true
  try {
    await adminAPI.plusQuotaAutomation.updateAutomation({
      enabled: configForm.enabled,
      group_id: configForm.group_id,
      interval_seconds: Math.round(configForm.interval_minutes * 60),
      utilization_threshold: configForm.utilization_threshold
    })
    await loadOverview(true, true)
    appStore.showSuccess(t('admin.plusQuotaAutomation.messages.configSaved'))
  } catch (error) {
    appStore.showError(
      apiErrorMessage(error, t('admin.plusQuotaAutomation.messages.saveConfigFailed'))
    )
  } finally {
    savingConfig.value = false
  }
}

async function confirmRun() {
  showRunConfirm.value = false
  if (
    runSubmitting.value
    || automationRunning.value
    || !canRunAutomation.value
    || deleteScopeLocked.value
  ) return
  runSubmitting.value = true
  try {
    overview.value = await adminAPI.plusQuotaAutomation.runAutomation()
    appStore.showSuccess(
      automationRunning.value
        ? t('admin.plusQuotaAutomation.messages.runStarted')
        : t('admin.plusQuotaAutomation.messages.runCompleted')
    )
    scheduleOverviewPoll()
    if (!automationRunning.value) await loadAnomalies()
  } catch (error) {
    if (apiErrorStatus(error) === 409) {
      appStore.showError(t('admin.plusQuotaAutomation.messages.runBusy'))
    } else {
      appStore.showError(
        apiErrorMessage(error, t('admin.plusQuotaAutomation.messages.runFailed'))
      )
    }
  } finally {
    runSubmitting.value = false
  }
}

async function resolveRow(row: PlusQuotaAnomaly) {
  if (
    deleteScopeLocked.value
    || resolvingAccountIds.has(row.account_id)
    || deletingAccountIds.has(row.account_id)
  ) return
  resolvingAccountIds.add(row.account_id)
  try {
    await adminAPI.plusQuotaAutomation.resolveAnomaly(row.account_id)
    appStore.showSuccess(t('admin.plusQuotaAutomation.messages.resolved'))
    await loadAnomalies()
  } catch (error) {
    appStore.showError(
      apiErrorMessage(error, t('admin.plusQuotaAutomation.messages.resolveFailed'))
    )
  } finally {
    resolvingAccountIds.delete(row.account_id)
  }
}

function requestDeleteAccount(row: PlusQuotaAnomaly) {
  if (
    row.status !== 'open'
    || row.http_status !== 401
    || deleteScopeLocked.value
    || resolvingAccountIds.has(row.account_id)
    || deletingAccountIds.has(row.account_id)
  ) {
    return
  }
  deleteCandidate.value = row
  showDeleteConfirm.value = true
}

function cancelDeleteAccount() {
  showDeleteConfirm.value = false
  deleteCandidate.value = null
}

async function confirmDeleteAccount() {
  const candidate = deleteCandidate.value
  showDeleteConfirm.value = false
  deleteCandidate.value = null
  if (
    !candidate
    || deleteScopeLocked.value
    || resolvingAccountIds.has(candidate.account_id)
    || deletingAccountIds.has(candidate.account_id)
  ) {
    return
  }

  deletingAccountIds.add(candidate.account_id)
  try {
    await adminAPI.plusQuotaAutomation.deleteAnomalyAccount(candidate.account_id)
    appStore.showSuccess(
      t('admin.plusQuotaAutomation.messages.accountDeleted', {
        name: candidate.account_name || candidate.email || `#${candidate.account_id}`
      })
    )
    await Promise.all([
      loadAnomalies(),
      loadOverview(false)
    ])
  } catch (error) {
    if (apiErrorStatus(error) === 409) {
      appStore.showWarning(
        t('admin.plusQuotaAutomation.messages.deleteAccountNoLongerEligible')
      )
    } else {
      appStore.showError(
        apiErrorMessage(error, t('admin.plusQuotaAutomation.messages.deleteAccountFailed'))
      )
    }
    await loadAnomalies()
  } finally {
    deletingAccountIds.delete(candidate.account_id)
  }
}

async function requestDeleteAllAccounts() {
  if (!canDeleteAllAccounts.value) return

  const controller = new AbortController()
  deleteAllAbortController = controller
  preparingDeleteAll.value = true
  try {
    const snapshot = await adminAPI.plusQuotaAutomation.getAnomalyDeletionCandidates(
      filters.search,
      { signal: controller.signal }
    )
    if (controller.signal.aborted || isUnmounted) return
    const candidates = [...new Set((snapshot.account_ids || []).filter((accountID) =>
      Number.isSafeInteger(accountID) && accountID > 0
    ))].sort((left, right) => left - right)
    if (candidates.length === 0) {
      appStore.showWarning(
        t('admin.plusQuotaAutomation.messages.noAccountsToDelete')
      )
      await loadAnomalies()
      return
    }
    deleteAllCandidates.value = candidates
    showDeleteAllConfirm.value = true
  } catch (error) {
    if (controller.signal.aborted || isUnmounted) return
    appStore.showError(
      apiErrorMessage(error, t('admin.plusQuotaAutomation.messages.prepareDeleteAllFailed'))
    )
  } finally {
    if (deleteAllAbortController === controller) {
      deleteAllAbortController = null
      preparingDeleteAll.value = false
      applyPendingAnomalyReload()
    }
  }
}

function cancelDeleteAllAccounts() {
  showDeleteAllConfirm.value = false
  deleteAllCandidates.value = []
  applyPendingAnomalyReload()
}

async function mapWithConcurrency<T, R>(
  items: T[],
  concurrency: number,
  worker: (item: T) => Promise<R>
): Promise<R[]> {
  const results = new Array<R>(items.length)
  let nextIndex = 0
  const workers = Array.from(
    { length: Math.min(concurrency, items.length) },
    async () => {
      while (nextIndex < items.length) {
        const index = nextIndex
        nextIndex += 1
        results[index] = await worker(items[index])
      }
    }
  )
  await Promise.all(workers)
  return results
}

async function deleteAllCandidate(accountID: number): Promise<BulkDeleteOutcome> {
  try {
    await adminAPI.plusQuotaAutomation.deleteAnomalyAccount(accountID)
    return 'deleted'
  } catch (error) {
    return apiErrorStatus(error) === 409 ? 'stale' : 'failed'
  } finally {
    deleteAllProgress.completed += 1
  }
}

async function confirmDeleteAllAccounts() {
  const candidates = [...deleteAllCandidates.value]
  showDeleteAllConfirm.value = false
  deleteAllCandidates.value = []
  if (candidates.length === 0 || deletingAllAccounts.value) return

  deletingAllAccounts.value = true
  deleteAllProgress.completed = 0
  deleteAllProgress.total = candidates.length
  for (const accountID of candidates) {
    deletingAccountIds.add(accountID)
  }

  try {
    const outcomes = await mapWithConcurrency(
      candidates,
      BULK_DELETE_CONCURRENCY,
      deleteAllCandidate
    )
    const deleted = outcomes.filter((outcome) => outcome === 'deleted').length
    const stale = outcomes.filter((outcome) => outcome === 'stale').length
    const failed = outcomes.length - deleted - stale

    anomalyReloadPending = false
    await Promise.all([
      loadAnomalies(),
      loadOverview(false)
    ])
    if (isUnmounted) return

    if (failed > 0) {
      appStore.showError(
        t('admin.plusQuotaAutomation.messages.deleteAllSummary', { deleted, stale, failed })
      )
    } else if (stale > 0) {
      appStore.showWarning(
        t('admin.plusQuotaAutomation.messages.deleteAllSummary', { deleted, stale, failed })
      )
    } else {
      appStore.showSuccess(
        t('admin.plusQuotaAutomation.messages.accountsDeleted', { count: deleted })
      )
    }
  } finally {
    for (const accountID of candidates) {
      deletingAccountIds.delete(accountID)
    }
    deletingAllAccounts.value = false
    deleteAllProgress.completed = 0
    deleteAllProgress.total = 0
    applyPendingAnomalyReload()
  }
}

function applyPendingAnomalyReload() {
  if (!anomalyReloadPending || deleteScopeLocked.value) return
  anomalyReloadPending = false
  pagination.page = 1
  void loadAnomalies()
}

function handleSearch() {
  if (deleteScopeLocked.value) {
    anomalyReloadPending = true
    return
  }
  pagination.page = 1
  void loadAnomalies()
}

function handleStatusChange() {
  if (deleteScopeLocked.value) return
  pagination.page = 1
  void loadAnomalies()
}

function handlePageChange(page: number) {
  if (deleteScopeLocked.value) return
  pagination.page = page
  void loadAnomalies()
}

function handlePageSizeChange(pageSize: number) {
  if (deleteScopeLocked.value) return
  pagination.page_size = pageSize
  pagination.page = 1
  void loadAnomalies()
}

function refreshAll() {
  if (deleteScopeLocked.value) return
  void loadAnomalies()
  void (async () => {
    await loadOverview(false)
    if (!isUnmounted) await loadGroups()
  })()
}

function formatTimestamp(value?: string | null): string {
  return value ? formatDateTimeToMinute(value) : '-'
}

function groupName(groupId: number): string {
  return groupNames.value.get(groupId) || `#${groupId}`
}

function stageLabel(stage: string): string {
  const normalized = stage?.trim().toLowerCase()
  const knownStages: Record<string, string> = {
    query: 'query',
    quota_query: 'query',
    credits: 'credits',
    credit_query: 'credits',
    reset: 'reset',
    quota_reset: 'reset'
  }
  const key = knownStages[normalized]
  return key ? t(`admin.plusQuotaAutomation.stage.${key}`) : stage || '-'
}

function anomalyStatusLabel(status: PlusQuotaAnomalyStatus): string {
  return t(`admin.plusQuotaAutomation.status.${status}`)
}

function anomalyStatusClass(status: PlusQuotaAnomalyStatus): string {
  return status === 'resolved'
    ? 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
    : 'bg-orange-100 text-orange-700 dark:bg-orange-900/40 dark:text-orange-300'
}

onMounted(() => {
  void loadAnomalies()
  void (async () => {
    await loadOverview(true)
    if (!isUnmounted) await loadGroups()
  })()
})

onUnmounted(() => {
  isUnmounted = true
  overviewRequestSequence += 1
  stopOverviewPolling()
  anomaliesAbortController?.abort()
  notesExportAbortController?.abort()
  deleteAllAbortController?.abort()
})
</script>
