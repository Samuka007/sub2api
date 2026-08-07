<template>
  <AppLayout>
    <TablePageLayout class="account-health-detector-layout">
      <template #actions>
        <div class="space-y-3">
          <section
            class="min-w-0 px-1 lg:hidden"
            data-test="mobile-page-heading"
            aria-labelledby="account-health-mobile-title"
          >
            <h1 id="account-health-mobile-title" class="text-lg font-semibold text-gray-900 dark:text-white">
              {{ t('admin.accountHealthDetector.title') }}
            </h1>
            <p class="mt-1 text-sm leading-5 text-gray-500 dark:text-dark-400">
              {{ t('admin.accountHealthDetector.description') }}
            </p>
          </section>

          <section
            class="border-y border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800"
            aria-labelledby="account-health-group-title"
            data-test="group-picker"
          >
            <div class="flex flex-col gap-3 px-4 py-3 lg:flex-row lg:items-end lg:justify-between">
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-baseline justify-between gap-2">
                  <h2 id="account-health-group-title" class="text-sm font-semibold text-gray-900 dark:text-white">
                    {{ t('admin.accountHealthDetector.groups.title') }}
                  </h2>
                  <span class="text-xs tabular-nums text-gray-500 dark:text-gray-400">
                    {{ t('admin.accountHealthDetector.groups.selectedCount', { count: selectedGroupIds.length }) }}
                  </span>
                </div>
                <SearchInput
                  v-model="groupSearch"
                  class="mt-2 max-w-xl"
                  :disabled="deleting"
                  :placeholder="t('admin.accountHealthDetector.groups.searchPlaceholder')"
                  :aria-label="t('admin.accountHealthDetector.groups.searchLabel')"
                  data-test="group-search"
                />
              </div>
              <div class="flex flex-wrap items-center gap-2">
                <button
                  type="button"
                  class="btn btn-secondary px-3 py-2 text-xs"
                  :disabled="groupsLoading || accountsLoading || scanning || deleting || filteredGroups.length === 0"
                  data-test="select-visible-groups"
                  @click="selectVisibleGroups"
                >
                  <Icon name="check" size="sm" />
                  <span>{{ t('admin.accountHealthDetector.actions.selectVisibleGroups') }}</span>
                </button>
                <button
                  type="button"
                  class="btn btn-secondary px-3 py-2 text-xs"
                  :disabled="groupsLoading || accountsLoading || selectedGroupIds.length === 0 || scanning || deleting"
                  data-test="clear-groups"
                  @click="clearSelectedGroups"
                >
                  <Icon name="x" size="sm" />
                  <span>{{ t('admin.accountHealthDetector.actions.clearGroups') }}</span>
                </button>
                <button
                  type="button"
                  class="btn btn-primary"
                  :disabled="groupsLoading || accountsLoading || scanning || deleting || selectedGroupIds.length === 0"
                  data-test="load-accounts"
                  @click="loadAccounts"
                >
                  <Icon name="users" size="sm" :class="{ 'animate-pulse': accountsLoading }" />
                  <span>{{ t('admin.accountHealthDetector.actions.loadAccounts') }}</span>
                </button>
                <button
                  type="button"
                  class="btn btn-secondary"
                  :disabled="groupsLoading || accountsLoading || scanning || deleting"
                  :title="t('admin.accountHealthDetector.actions.reloadGroups')"
                  data-test="reload-groups"
                  @click="loadGroups"
                >
                  <Icon name="refresh" size="sm" :class="{ 'animate-spin': groupsLoading }" />
                  <span>{{ t('admin.accountHealthDetector.actions.reloadGroups') }}</span>
                </button>
              </div>
            </div>

            <div
              v-if="filteredGroups.length > 0"
              class="grid max-h-56 grid-cols-1 overflow-y-auto border-t border-gray-200 dark:border-dark-700 md:grid-cols-2 xl:grid-cols-3"
            >
              <label
                v-for="group in filteredGroups"
                :key="group.id"
                class="grid min-h-11 cursor-pointer grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-2 border-b border-gray-100 px-4 py-2 hover:bg-gray-50 dark:border-dark-700/70 dark:hover:bg-dark-700/60 md:border-r"
              >
                <input
                  type="checkbox"
                  class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-700"
                  :checked="selectedGroupIds.includes(group.id)"
                  :disabled="groupsLoading || scanning || accountsLoading || deleting"
                  :data-test="`group-${group.id}`"
                  @change="toggleGroup(group.id, ($event.target as HTMLInputElement).checked)"
                />
                <span class="truncate text-sm text-gray-800 dark:text-gray-200" :title="group.name">
                  {{ group.name }}
                </span>
                <span class="text-xs" :class="group.status === 'active' ? 'text-emerald-600 dark:text-emerald-400' : 'text-gray-400'">
                  {{ accountStatusLabel(group.status) }}
                </span>
              </label>
            </div>
            <div v-else-if="!groupsLoading" class="border-t border-gray-200 px-4 py-6 text-sm text-gray-500 dark:border-dark-700 dark:text-gray-400">
              {{ t('admin.accountHealthDetector.groups.empty') }}
            </div>
          </section>

          <div class="flex flex-col gap-3 border-y border-gray-200 bg-white px-4 py-3 dark:border-dark-700 dark:bg-dark-800 lg:flex-row lg:items-end">
            <div class="w-full sm:w-36">
              <label class="input-label" for="account-health-concurrency">
                {{ t('admin.accountHealthDetector.controls.concurrency') }}
              </label>
              <input
                id="account-health-concurrency"
                v-model.number="concurrency"
                class="input"
                type="number"
                min="1"
                max="10"
                step="1"
                :disabled="scanning || deleting"
              />
            </div>

            <div class="flex flex-1 flex-wrap items-center gap-2">
              <button
                type="button"
                class="btn btn-primary"
                data-test="start-scan"
                :disabled="accountsLoading || scanning || deleting || accounts.length === 0"
                @click="startScan"
              >
                <Icon :name="scanning ? 'refresh' : 'play'" size="sm" :class="{ 'animate-spin': scanning }" />
                <span>{{ t('admin.accountHealthDetector.actions.scanAccounts', { count: accounts.length }) }}</span>
              </button>
              <button
                type="button"
                class="btn btn-secondary"
                data-test="stop-scan"
                :disabled="!scanning"
                @click="stopScan"
              >
                <Icon name="xCircle" size="sm" />
                <span>{{ t('admin.accountHealthDetector.actions.stop') }}</span>
              </button>
              <button
                type="button"
                class="btn btn-danger"
                data-test="delete-selected"
                :disabled="scanning || deleting || selectedAccountIds.length === 0"
                @click="requestDeleteSelected"
              >
                <Icon name="trash" size="sm" />
                <span>{{ t('admin.accountHealthDetector.actions.deleteSelected', { count: selectedAccountIds.length }) }}</span>
              </button>
              <button
                type="button"
                class="btn btn-secondary text-red-600 hover:text-red-700 dark:text-red-400"
                data-test="delete-all"
                :disabled="scanning || deleting || accounts.length === 0"
                @click="requestDeleteAll"
              >
                <Icon name="trash" size="sm" />
                <span>{{ t('admin.accountHealthDetector.actions.deleteAll', { count: accounts.length }) }}</span>
              </button>
            </div>

            <div v-if="scanTotal > 0" class="w-full lg:max-w-sm">
              <div class="mb-1 flex items-center justify-between text-xs text-gray-500 dark:text-gray-400">
                <span>{{ t('admin.accountHealthDetector.progress.label') }}</span>
                <span class="tabular-nums">{{ scanCompleted }} / {{ scanTotal }}</span>
              </div>
              <div
                class="h-2 overflow-hidden rounded bg-gray-100 dark:bg-dark-700"
                role="progressbar"
                :aria-label="t('admin.accountHealthDetector.progress.label')"
                :aria-valuenow="scanCompleted"
                aria-valuemin="0"
                :aria-valuemax="scanTotal"
              >
                <div class="h-full bg-primary-500 transition-[width] duration-200" :style="{ width: `${scanProgress}%` }"></div>
              </div>
            </div>
          </div>

          <dl class="grid grid-cols-2 divide-x divide-y divide-gray-200 overflow-hidden border-y border-gray-200 bg-white dark:divide-dark-700 dark:border-dark-700 dark:bg-dark-800 sm:grid-cols-4 xl:grid-cols-8">
            <div v-for="item in summaryItems" :key="item.key" class="min-w-0 px-3 py-2.5">
              <dt class="truncate text-xs text-gray-500 dark:text-gray-400">{{ item.label }}</dt>
              <dd class="mt-0.5 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ item.value }}</dd>
            </div>
          </dl>
        </div>
      </template>

      <template #filters>
        <div class="space-y-3">
          <div class="grid grid-cols-1 gap-3 md:grid-cols-2 xl:grid-cols-5">
            <SearchInput
              v-model="filters.search"
              :placeholder="t('admin.accountHealthDetector.filters.searchPlaceholder')"
              :aria-label="t('admin.accountHealthDetector.filters.searchLabel')"
            />
            <Select v-model="filters.localStatus" :options="localStatusOptions" :aria-label="t('admin.accountHealthDetector.filters.localStatusLabel')" />
            <Select v-model="filters.outcome" :options="outcomeOptions" :aria-label="t('admin.accountHealthDetector.filters.outcomeLabel')" />
            <Select v-model="filters.banStatus" :options="banStatusOptions" :aria-label="t('admin.accountHealthDetector.filters.banStatusLabel')" />
            <Select v-model="filters.plusStatus" :options="plusStatusOptions" :aria-label="t('admin.accountHealthDetector.filters.plusStatusLabel')" />
          </div>

          <div class="flex items-end gap-2 md:hidden" data-test="mobile-sort-controls">
            <div class="min-w-0 flex-1">
              <label class="input-label" for="account-health-mobile-sort">{{ t('admin.accountHealthDetector.filters.sortFieldLabel') }}</label>
              <Select
                id="account-health-mobile-sort"
                :model-value="sortState.key"
                :options="mobileSortOptions"
                :aria-label="t('admin.accountHealthDetector.filters.sortFieldLabel')"
                data-test="mobile-sort-field"
                @update:model-value="handleMobileSortKey"
              />
            </div>
            <button
              type="button"
              class="btn btn-secondary h-10 w-10 shrink-0 p-0"
              data-test="mobile-sort-order"
              :aria-label="mobileSortOrderLabel"
              :title="mobileSortOrderLabel"
              @click="toggleMobileSortOrder"
            >
              <Icon :name="sortState.order === 'asc' ? 'arrowUp' : 'arrowDown'" size="sm" />
            </button>
          </div>

          <div class="flex flex-wrap items-center justify-between gap-2">
            <span class="text-xs text-gray-500 dark:text-gray-400">
              {{ t('admin.accountHealthDetector.filters.visibleCount', { count: filteredRows.length }) }}
            </span>
            <div class="flex flex-wrap items-center gap-2">
              <button type="button" class="btn btn-secondary px-3 py-1.5 text-xs" :disabled="filteredRows.length === 0 || scanning || deleting" @click="selectFilteredAccounts">
                <Icon name="check" size="sm" />
                <span>{{ t('admin.accountHealthDetector.actions.selectFilteredAccounts') }}</span>
              </button>
              <button type="button" class="btn btn-secondary px-3 py-1.5 text-xs" :disabled="selectedAccountIds.length === 0 || scanning || deleting" @click="selectedAccountIds = []">
                <Icon name="x" size="sm" />
                <span>{{ t('admin.accountHealthDetector.actions.clearAccountSelection') }}</span>
              </button>
            </div>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable
          :columns="columns"
          :data="pageRows"
          :loading="accountsLoading"
          row-key="id"
          selectable
          server-side-sort
          :selected-keys="selectedAccountIds"
          :selection-disabled="scanning || deleting"
          :selection-label="selectionLabel"
          :sort-key="sortState.key"
          :sort-order="sortState.order"
          :virtualize-threshold="50"
          @sort="handleSort"
          @update:selected-keys="updateSelectedAccountIds"
        >
          <template #cell-account_name="{ row }">
            <div class="min-w-0 sm:min-w-56">
              <p class="font-medium text-gray-900 dark:text-white sm:truncate" :title="row.account_name">{{ row.account_name }}</p>
              <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">#{{ row.id }} · {{ row.account_type }}</p>
              <p v-if="row.email && row.email !== row.account_name" class="mt-0.5 break-all text-[11px] text-gray-500 dark:text-gray-400 sm:truncate" :title="row.email">{{ row.email }}</p>
              <p v-if="row.associated_email" class="mt-0.5 break-all text-[11px] text-amber-700 dark:text-amber-300 sm:truncate" :title="row.associated_email">
                {{ t('admin.accountHealthDetector.values.associatedEmail', { value: row.associated_email }) }}
              </p>
            </div>
          </template>

          <template #cell-group_name="{ row }">
            <p class="max-w-52 truncate text-sm" :title="row.group_names.join(', ')">{{ row.group_names.join(', ') }}</p>
          </template>

          <template #cell-local_status="{ value }">
            <span class="inline-flex rounded px-2 py-1 text-xs font-medium" :class="accountStatusClass(value)">{{ accountStatusLabel(value) }}</span>
          </template>

          <template #cell-outcome="{ row }">
            <div class="space-y-1" :data-test="`outcome-${row.id}`">
              <span class="inline-flex items-center gap-1.5 rounded px-2 py-1 text-xs font-medium" :class="outcomeClass(row.outcome)">
                <span v-if="row.outcome === 'checking'" class="h-2 w-2 animate-pulse rounded-full bg-current"></span>
                {{ outcomeLabel(row.outcome) }}
              </span>
              <p v-if="row.error" class="max-w-64 break-words text-[11px] text-gray-500 dark:text-gray-400">{{ row.error }}</p>
              <p v-if="row.message_date" class="max-w-64 break-words text-[11px] text-gray-500 dark:text-gray-400">
                {{ t('admin.accountHealthDetector.values.messageDate', { value: row.message_date }) }}
              </p>
            </div>
          </template>

          <template #cell-ban_status="{ value }"><span class="inline-flex rounded px-2 py-1 text-xs font-medium" :class="banStatusClass(value)">{{ banStatusLabel(value) }}</span></template>
          <template #cell-plus_status="{ row }"><span class="inline-flex rounded px-2 py-1 text-xs font-medium" :class="plusStatusClass(row.plus_status)">{{ plusStatusLabel(row.plus_status, row.plus_score) }}</span></template>
          <template #cell-plus_date="{ value }"><span class="whitespace-nowrap text-xs">{{ value || '-' }}</span></template>
          <template #cell-payment_method="{ value }"><span class="block max-w-40 truncate text-xs" :title="value || ''">{{ value || '-' }}</span></template>
          <template #cell-ban_date="{ value }"><span class="whitespace-nowrap text-xs">{{ value || '-' }}</span></template>
          <template #cell-lifespan="{ row }"><span class="whitespace-nowrap text-xs">{{ formatLifespan(row) }}</span></template>
          <template #cell-score="{ value }"><span class="whitespace-nowrap text-xs tabular-nums">{{ formatScore(value) }}</span></template>
          <template #cell-message_page_count="{ row }"><span class="whitespace-nowrap text-xs tabular-nums">{{ formatMessagePageCount(row) }}</span></template>
          <template #cell-evidence_summary="{ value }"><p class="max-w-80 break-words text-xs leading-5" :title="value || ''">{{ value || '-' }}</p></template>
          <template #cell-elapsed_ms="{ value }"><span class="whitespace-nowrap text-xs tabular-nums">{{ formatElapsed(value) }}</span></template>
          <template #cell-checked_at="{ row }"><span class="whitespace-nowrap text-xs">{{ formatCheckedAt(row.checked_at) }}</span></template>
          <template #cell-actions="{ row }">
            <button
              type="button"
              class="inline-flex h-8 w-8 items-center justify-center rounded text-red-600 hover:bg-red-50 hover:text-red-700 focus:outline-none focus:ring-2 focus:ring-red-500 dark:text-red-400 dark:hover:bg-red-950/40"
              :disabled="scanning || deleting"
              :aria-label="t('admin.accountHealthDetector.actions.deleteAccount', { name: row.account_name })"
              :title="t('admin.accountHealthDetector.actions.deleteAccount', { name: row.account_name })"
              :data-test="`delete-account-${row.id}`"
              @click="requestDeleteAccount(row)"
            >
              <Icon name="trash" size="sm" />
            </button>
          </template>

          <template #empty>
            <div class="flex flex-col items-center py-12 text-gray-500 dark:text-gray-400">
              <Icon name="shield" size="xl" class="mb-3 text-gray-400" />
              <p>{{ emptyMessage }}</p>
            </div>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="filteredRows.length > 0"
          :page="pagination.page"
          :total="filteredRows.length"
          :page-size="pagination.pageSize"
          @update:page="pagination.page = $event"
          @update:page-size="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <ConfirmDialog
      :show="deleteDialog.show"
      :title="t('admin.accountHealthDetector.deleteDialog.title')"
      :message="deleteDialogMessage"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmDelete"
      @cancel="closeDeleteDialog"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AccountHealthCandidate, AccountHealthDetectionResult } from '@/api/admin/accounts'
import type {
  GroupAccountHealthCandidate,
  GroupAccountHealthEvidence,
  GroupAccountHealthFormatIssue,
  GroupAccountHealthDetectionStatus,
  GroupAccountHealthLifespanStatus
} from '@/api/admin/groups'
import type { Column } from '@/components/common/types'
import { useAppStore } from '@/stores'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { isCancelledScanError, runConcurrentScan } from '@/features/account-health-detector/scanner'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import SearchInput from '@/components/common/SearchInput.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

type DetectionOutcome =
  | 'idle'
  | 'queued'
  | 'checking'
  | GroupAccountHealthDetectionStatus
  | 'stopped'
  | 'failed'
type BanStatus = 'unknown' | 'not_banned' | 'banned' | 'reactivated' | 'warning' | 'mismatch'
type PlusStatus = 'unknown' | 'not_detected' | 'detected' | 'banned'

interface ScanResult {
  outcome: DetectionOutcome
  detection?: AccountHealthDetectionResult
  checkedAt?: string
  error?: string
}

interface AccountHealthRow {
  id: number
  account: AccountHealthCandidate
  account_name: string
  account_type: string
  group_name: string
  group_names: string[]
  email: string
  associated_email: string
  local_status: string
  outcome: DetectionOutcome
  ban_status: BanStatus
  plus_status: PlusStatus
  plus_score: number | null
  message_date: string
  plus_date: string
  payment_method: string
  ban_date: string
  lifespan: number | null
  lifespan_status: GroupAccountHealthLifespanStatus
  score: number | null
  messages_scanned: number | null
  pages_scanned: number | null
  message_page_count: string
  evidence_summary: string
  elapsed_ms: number | null
  checked_at: string
  error: string
}

type AccountHealthSortKey = Exclude<keyof AccountHealthRow, 'account' | 'group_names'>

const maxSelectedGroups = 50
const maxBatchDeleteAccounts = 500
const sortableColumnKeys = new Set<AccountHealthSortKey>([
  'account_name', 'account_type', 'group_name', 'email', 'local_status', 'outcome', 'ban_status',
  'plus_status', 'plus_date', 'payment_method', 'ban_date', 'lifespan', 'score',
  'message_page_count', 'evidence_summary', 'elapsed_ms', 'checked_at'
])

const { t, locale } = useI18n()
const appStore = useAppStore()
const sortCollator = computed(() => new Intl.Collator(locale.value, { numeric: true, sensitivity: 'base' }))

const groups = ref<GroupAccountHealthCandidate[]>([])
const groupsLoading = ref(false)
const groupSearch = ref('')
const selectedGroupIds = ref<number[]>([])
const loadedGroupKey = ref('')
const accounts = ref<AccountHealthCandidate[]>([])
const accountsLoading = ref(false)
const selectedAccountIds = ref<number[]>([])
const deleting = ref(false)
const concurrency = ref(3)
const scanning = ref(false)
const scanTotal = ref(0)
const scanCompleted = ref(0)
const scanResults = reactive(new Map<number, ScanResult>())
const filters = reactive({ search: '', localStatus: 'all', outcome: 'all', banStatus: 'all', plusStatus: 'all' })
const pagination = reactive({ page: 1, pageSize: getPersistedPageSize() })
const sortState = reactive<{ key: AccountHealthSortKey; order: 'asc' | 'desc' }>({ key: 'account_name', order: 'asc' })
const deleteDialog = reactive({ show: false, ids: [] as number[], label: '' })

let groupsController: AbortController | null = null
let accountsController: AbortController | null = null
let scanController: AbortController | null = null

const selectedGroupKey = computed(() => [...selectedGroupIds.value].sort((a, b) => a - b).join(','))
const filteredGroups = computed(() => {
  const search = groupSearch.value.trim().toLocaleLowerCase()
  if (!search) return groups.value
  return groups.value.filter((group) => `${group.name} ${group.id}`.toLocaleLowerCase().includes(search))
})

const columns = computed<Column[]>(() => [
  { key: 'account_name', label: t('admin.accountHealthDetector.columns.account'), sortable: true },
  { key: 'group_name', label: t('admin.accountHealthDetector.columns.groups'), sortable: true },
  { key: 'local_status', label: t('admin.accountHealthDetector.columns.accountStatus'), sortable: true },
  { key: 'outcome', label: t('admin.accountHealthDetector.columns.outcome'), sortable: true },
  { key: 'ban_status', label: t('admin.accountHealthDetector.columns.banStatus'), sortable: true },
  { key: 'plus_status', label: t('admin.accountHealthDetector.columns.plusStatus'), sortable: true },
  { key: 'plus_date', label: t('admin.accountHealthDetector.columns.plusDate'), sortable: true },
  { key: 'payment_method', label: t('admin.accountHealthDetector.columns.paymentMethod'), sortable: true },
  { key: 'ban_date', label: t('admin.accountHealthDetector.columns.banDate'), sortable: true },
  { key: 'lifespan', label: t('admin.accountHealthDetector.columns.lifespan'), sortable: true },
  { key: 'score', label: t('admin.accountHealthDetector.columns.score'), sortable: true },
  { key: 'message_page_count', label: t('admin.accountHealthDetector.columns.messagePageCount'), sortable: true },
  { key: 'evidence_summary', label: t('admin.accountHealthDetector.columns.evidence'), sortable: true },
  { key: 'elapsed_ms', label: t('admin.accountHealthDetector.columns.elapsed'), sortable: true },
  { key: 'checked_at', label: t('admin.accountHealthDetector.columns.checkedAt'), sortable: true },
  { key: 'actions', label: t('common.actions') }
])
const mobileSortOptions = computed(() => columns.value
  .filter((column) => column.sortable)
  .map((column) => ({ value: column.key, label: column.label })))
const mobileSortOrderLabel = computed(() => t(
  sortState.order === 'asc'
    ? 'admin.accountHealthDetector.filters.sortAscending'
    : 'admin.accountHealthDetector.filters.sortDescending'
))

function normalizeOutcome(result: AccountHealthDetectionResult): DetectionOutcome {
  return result.status
}

function resolveBanStatus(outcome: DetectionOutcome): BanStatus {
  if (outcome === 'deactivated') return 'banned'
  if (outcome === 'reactivated') return 'reactivated'
  if (outcome === 'warning') return 'warning'
  if (outcome === 'mismatch') return 'mismatch'
  if (outcome === 'no_evidence') return 'not_banned'
  return 'unknown'
}

function resolvePlusStatus(result: ScanResult): PlusStatus {
  if (!result.detection) return 'unknown'
  if (['fetch_error', 'parse_error', 'format_error', 'failed', 'stopped'].includes(result.outcome)) return 'unknown'
  if (!result.detection.plus_detected) return 'not_detected'
  return result.outcome === 'deactivated' ? 'banned' : 'detected'
}

function formatIssueMessage(issue: GroupAccountHealthFormatIssue | undefined): string {
  return issue ? t(`admin.accountHealthDetector.formatIssue.${issue}`) : ''
}

function evidenceLabel(evidence: GroupAccountHealthEvidence): string {
  return t(`admin.accountHealthDetector.evidence.${evidence}`)
}

function resultEvidence(result: ScanResult): string {
  const detection = result.detection
  if (!detection) return ''
  if (detection.format_issue) return formatIssueMessage(detection.format_issue)
  return (detection.evidence ?? []).map(evidenceLabel).join(' · ')
}

const healthRows = computed<AccountHealthRow[]>(() => accounts.value.map((account) => {
  const result = scanResults.get(account.id) || { outcome: 'idle' as const }
  const detection = result.detection
  const accountEmail = detection?.account_email?.trim() || ''
  const associatedEmail = detection?.associated_email?.trim() || ''
  const groupNames = account.group_names?.length > 0 ? account.group_names : [account.group_name]
  return {
    id: account.id,
    account,
    account_name: account.name,
    account_type: account.type,
    group_name: groupNames.join(', '),
    group_names: groupNames,
    email: accountEmail,
    associated_email: associatedEmail && associatedEmail !== accountEmail ? associatedEmail : '',
    local_status: account.status,
    outcome: result.outcome,
    ban_status: resolveBanStatus(result.outcome),
    plus_status: resolvePlusStatus(result),
    plus_score: detection?.plus_score ?? null,
    message_date: detection?.message_date || '',
    plus_date: detection?.plus_date || '',
    payment_method: detection?.payment_method || '',
    ban_date: detection?.ban_date || '',
    lifespan: detection?.lifespan_seconds ?? null,
    lifespan_status: detection?.lifespan_status || 'unavailable',
    score: detection?.score ?? null,
    messages_scanned: detection?.messages_scanned ?? null,
    pages_scanned: detection?.pages_scanned ?? null,
    message_page_count: detection ? `${detection.messages_scanned}/${detection.pages_scanned}` : '',
    evidence_summary: resultEvidence(result),
    elapsed_ms: detection?.elapsed_ms ?? null,
    checked_at: detection?.checked_at || result.checkedAt || '',
    error: result.error || formatIssueMessage(detection?.format_issue)
  }
}))

const filteredRows = computed(() => {
  const search = filters.search.trim().toLocaleLowerCase()
  return healthRows.value.filter((row) => {
    if (search) {
      const haystack = [row.account_name, row.account_type, row.group_name, row.email, row.associated_email, row.evidence_summary, String(row.id)]
        .join(' ').toLocaleLowerCase()
      if (!haystack.includes(search)) return false
    }
    if (filters.localStatus !== 'all' && row.local_status !== filters.localStatus) return false
    if (filters.outcome !== 'all' && row.outcome !== filters.outcome) return false
    if (filters.banStatus !== 'all' && row.ban_status !== filters.banStatus) return false
    if (filters.plusStatus !== 'all' && row.plus_status !== filters.plusStatus) return false
    return true
  })
})

const sortedRows = computed(() => filteredRows.value
  .map((row, index) => ({ row, index }))
  .sort((left, right) => {
    const leftValue = accountHealthSortValue(left.row, sortState.key)
    const rightValue = accountHealthSortValue(right.row, sortState.key)
    const leftEmpty = leftValue == null || leftValue === ''
    const rightEmpty = rightValue == null || rightValue === ''
    if (leftEmpty || rightEmpty) {
      if (leftEmpty && rightEmpty) return left.index - right.index
      return leftEmpty ? 1 : -1
    }
    const compared = sortCollator.value.compare(String(leftValue), String(rightValue))
    if (compared !== 0) return sortState.order === 'asc' ? compared : -compared
    return left.index - right.index
  })
  .map(({ row }) => row))

function accountHealthSortValue(row: AccountHealthRow, key: AccountHealthSortKey): unknown {
  if (key === 'message_page_count') return row.messages_scanned
  if (key === 'plus_status') {
    const score = row.plus_score == null || !Number.isFinite(row.plus_score) ? -1 : row.plus_score
    return `${row.plus_status}:${score}`
  }
  return row[key]
}

const pageRows = computed(() => {
  const start = (pagination.page - 1) * pagination.pageSize
  return sortedRows.value.slice(start, start + pagination.pageSize)
})

const localStatusOptions = computed(() => [
  { value: 'all', label: t('admin.accountHealthDetector.filters.allLocalStatuses') },
  { value: 'active', label: t('admin.accountHealthDetector.localStatus.active') },
  { value: 'inactive', label: t('admin.accountHealthDetector.localStatus.inactive') },
  { value: 'error', label: t('admin.accountHealthDetector.localStatus.error') }
])
const detectionOutcomes: DetectionOutcome[] = [
  'idle', 'queued', 'checking', 'deactivated', 'warning', 'reactivated', 'no_evidence',
  'mismatch', 'fetch_error', 'parse_error', 'format_error', 'stopped', 'failed'
]
const outcomeOptions = computed(() => [
  { value: 'all', label: t('admin.accountHealthDetector.filters.allOutcomes') },
  ...detectionOutcomes.map((value) => ({ value, label: outcomeLabel(value) }))
])
const banStatuses: BanStatus[] = ['unknown', 'not_banned', 'banned', 'reactivated', 'warning', 'mismatch']
const banStatusOptions = computed(() => [
  { value: 'all', label: t('admin.accountHealthDetector.filters.allBanStatuses') },
  ...banStatuses.map((value) => ({ value, label: banStatusLabel(value) }))
])
const plusStatuses: PlusStatus[] = ['unknown', 'not_detected', 'detected', 'banned']
const plusStatusOptions = computed(() => [
  { value: 'all', label: t('admin.accountHealthDetector.filters.allPlusStatuses') },
  ...plusStatuses.map((value) => ({ value, label: plusStatusLabel(value) }))
])

const completedResults = computed(() => Array.from(scanResults.values()).filter((result) =>
  !['idle', 'queued', 'checking', 'stopped'].includes(result.outcome)))
const summaryItems = computed(() => {
  const results = Array.from(scanResults.values())
  const completed = completedResults.value
  return [
    { key: 'groups', label: t('admin.accountHealthDetector.summary.selectedGroups'), value: selectedGroupIds.value.length },
    { key: 'loaded', label: t('admin.accountHealthDetector.summary.loadedAccounts'), value: accounts.value.length },
    { key: 'selected', label: t('admin.accountHealthDetector.summary.selectedAccounts'), value: selectedAccountIds.value.length },
    { key: 'checked', label: t('admin.accountHealthDetector.summary.checked'), value: completed.length },
    { key: 'plus', label: t('admin.accountHealthDetector.summary.plus'), value: completed.filter((result) => resolvePlusStatus(result) === 'detected').length },
    { key: 'banned', label: t('admin.accountHealthDetector.summary.banned'), value: completed.filter((result) => result.outcome === 'deactivated').length },
    { key: 'format', label: t('admin.accountHealthDetector.summary.formatErrors'), value: completed.filter((result) => result.outcome === 'format_error').length },
    { key: 'failed', label: t('admin.accountHealthDetector.summary.failed'), value: completed.filter((result) => ['fetch_error', 'parse_error', 'failed'].includes(result.outcome)).length + results.filter((result) => result.outcome === 'stopped').length }
  ]
})
const scanProgress = computed(() => scanTotal.value <= 0 ? 0 : Math.min(100, Math.round((scanCompleted.value / scanTotal.value) * 100)))
const emptyMessage = computed(() => {
  if (selectedGroupIds.value.length === 0) return t('admin.accountHealthDetector.empty.selectGroups')
  if (!loadedGroupKey.value) return t('admin.accountHealthDetector.empty.loadAccounts')
  return t('admin.accountHealthDetector.empty.noAccounts')
})
const deleteDialogMessage = computed(() => deleteDialog.ids.length === 1 && deleteDialog.label
  ? t('admin.accountHealthDetector.deleteDialog.single', { name: deleteDialog.label })
  : t('admin.accountHealthDetector.deleteDialog.batch', { count: deleteDialog.ids.length }))

watch(
  () => [filters.search, filters.localStatus, filters.outcome, filters.banStatus, filters.plusStatus],
  () => { pagination.page = 1 }
)
watch(() => filteredRows.value.length, (total) => {
  const lastPage = Math.max(1, Math.ceil(total / pagination.pageSize))
  if (pagination.page > lastPage) pagination.page = lastPage
})
watch(selectedGroupKey, (next) => {
  if (loadedGroupKey.value && loadedGroupKey.value !== next) clearLoadedAccounts()
})

async function loadGroups() {
  if (accountsLoading.value || deleting.value) return
  groupsController?.abort()
  const controller = new AbortController()
  groupsController = controller
  groupsLoading.value = true
  try {
    const loaded: GroupAccountHealthCandidate[] = []
    let page = 1
    let pages = 1
    do {
      const response = await adminAPI.groups.listAccountHealthCandidates(page, 1000, { signal: controller.signal })
      loaded.push(...response.items.filter((group) => group.platform === 'openai'))
      pages = Math.max(1, response.pages || 1)
      page += 1
    } while (page <= pages && !controller.signal.aborted)
    if (controller.signal.aborted) return
    groups.value = loaded
    const validIDs = new Set(loaded.map((group) => group.id))
    selectedGroupIds.value = selectedGroupIds.value.filter((id) => validIDs.has(id))
  } catch (error) {
    if (!isCancelledScanError(error, controller.signal)) appStore.showError(t('admin.accountHealthDetector.messages.loadGroupsFailed'))
  } finally {
    if (groupsController === controller) {
      groupsController = null
      groupsLoading.value = false
    }
  }
}

async function loadAccounts() {
  if (groupsLoading.value || deleting.value) return
  if (selectedGroupIds.value.length === 0) {
    appStore.showWarning(t('admin.accountHealthDetector.messages.noGroupSelection'))
    return
  }
  accountsController?.abort()
  const controller = new AbortController()
  accountsController = controller
  accountsLoading.value = true
  try {
    const loaded: AccountHealthCandidate[] = []
    let page = 1
    let pages = 1
    do {
      const response = await adminAPI.accounts.listAccountHealthCandidates(
        page,
        1000,
        selectedGroupIds.value,
        { signal: controller.signal }
      )
      loaded.push(...response.items.filter((account) => account.platform === 'openai'))
      pages = Math.max(1, response.pages || 1)
      page += 1
    } while (page <= pages && !controller.signal.aborted)
    if (controller.signal.aborted) return
    accounts.value = loaded
    loadedGroupKey.value = selectedGroupKey.value
    selectedAccountIds.value = []
    resetScanResults()
    pagination.page = 1
  } catch (error) {
    if (!isCancelledScanError(error, controller.signal)) appStore.showError(t('admin.accountHealthDetector.messages.loadAccountsFailed'))
  } finally {
    if (accountsController === controller) {
      accountsController = null
      accountsLoading.value = false
    }
  }
}

function toggleGroup(groupID: number, checked: boolean) {
  if (groupsLoading.value || accountsLoading.value || deleting.value) return
  if (checked) {
    if (selectedGroupIds.value.includes(groupID)) return
    if (selectedGroupIds.value.length >= maxSelectedGroups) {
      appStore.showWarning(t('admin.accountHealthDetector.messages.groupLimit', { count: maxSelectedGroups }))
      return
    }
    selectedGroupIds.value = [...selectedGroupIds.value, groupID]
    return
  }
  selectedGroupIds.value = selectedGroupIds.value.filter((id) => id !== groupID)
}

function selectVisibleGroups() {
  if (groupsLoading.value || accountsLoading.value || deleting.value) return
  const selected = new Set(selectedGroupIds.value)
  for (const group of filteredGroups.value) {
    if (selected.size >= maxSelectedGroups) break
    selected.add(group.id)
  }
  selectedGroupIds.value = Array.from(selected)
  if (filteredGroups.value.some((group) => !selected.has(group.id))) {
    appStore.showWarning(t('admin.accountHealthDetector.messages.groupLimit', { count: maxSelectedGroups }))
  }
}

function clearSelectedGroups() {
  if (groupsLoading.value || accountsLoading.value || deleting.value) return
  selectedGroupIds.value = []
}

function clearLoadedAccounts() {
  accountsController?.abort()
  accounts.value = []
  loadedGroupKey.value = ''
  selectedAccountIds.value = []
  resetScanResults()
}

function resetScanResults() {
  scanResults.clear()
  scanTotal.value = 0
  scanCompleted.value = 0
}

async function startScan() {
  if (scanning.value || deleting.value) return
  const targets = [...accounts.value]
  if (targets.length === 0) {
    appStore.showWarning(t('admin.accountHealthDetector.messages.noAccounts'))
    return
  }
  const controller = new AbortController()
  scanController = controller
  scanning.value = true
  scanTotal.value = targets.length
  scanCompleted.value = 0
  const workerCount = Math.max(1, Math.min(10, Math.floor(Number(concurrency.value)) || 1))
  concurrency.value = workerCount
  for (const account of targets) scanResults.set(account.id, { outcome: 'queued' })
  try {
    await runConcurrentScan(targets, {
      concurrency: workerCount,
      signal: controller.signal,
      scan: (account, signal) => adminAPI.accounts.detectAccountHealth(account.id, account.group_id, { signal }),
      onStart: (account) => scanResults.set(account.id, { outcome: 'checking' }),
      onSuccess: (account, detection) => {
        scanResults.set(account.id, {
          outcome: normalizeOutcome(detection),
          detection,
          checkedAt: detection.checked_at || new Date().toISOString()
        })
        scanCompleted.value += 1
      },
      onError: (account, error) => {
        const stopped = isCancelledScanError(error, controller.signal)
        scanResults.set(account.id, {
          outcome: stopped ? 'stopped' : 'failed',
          checkedAt: new Date().toISOString(),
          error: stopped ? '' : t('admin.accountHealthDetector.reasons.requestFailed')
        })
        if (!stopped) scanCompleted.value += 1
      }
    })
  } finally {
    for (const account of targets) {
      const current = scanResults.get(account.id)
      if (!current || current.outcome === 'queued') scanResults.set(account.id, { outcome: 'stopped' })
      else if (current.outcome === 'checking') scanResults.set(account.id, { outcome: 'stopped', checkedAt: new Date().toISOString() })
    }
    scanCompleted.value = targets.filter((account) => {
      const outcome = scanResults.get(account.id)?.outcome
      return outcome != null && !['idle', 'queued', 'checking', 'stopped'].includes(outcome)
    }).length
    if (scanController === controller) scanController = null
    scanning.value = false
  }
}

function stopScan() {
  scanController?.abort()
}

function selectFilteredAccounts() {
  selectedAccountIds.value = filteredRows.value.map((row) => row.id)
}

function updateSelectedAccountIds(keys: Array<string | number>) {
  if (scanning.value || deleting.value) return
  const available = new Set(accounts.value.map((account) => account.id))
  selectedAccountIds.value = keys.map(Number).filter((id) => Number.isFinite(id) && available.has(id))
}

function selectionLabel(row: AccountHealthRow): string {
  return t('admin.accountHealthDetector.selectionLabel', { name: row.account_name })
}

function requestDeleteAccount(row: AccountHealthRow) {
  openDeleteDialog([row.id], row.account_name)
}

function requestDeleteSelected() {
  openDeleteDialog([...selectedAccountIds.value], '')
}

function requestDeleteAll() {
  openDeleteDialog(accounts.value.map((account) => account.id), '')
}

function openDeleteDialog(ids: number[], label: string) {
  if (ids.length === 0 || scanning.value || deleting.value) return
  deleteDialog.ids = Array.from(new Set(ids))
  deleteDialog.label = label
  deleteDialog.show = true
}

function closeDeleteDialog() {
  if (deleting.value) return
  deleteDialog.show = false
  deleteDialog.ids = []
  deleteDialog.label = ''
}

async function confirmDelete() {
  const ids = [...deleteDialog.ids]
  if (ids.length === 0 || deleting.value) return
  deleteDialog.show = false
  deleting.value = true
  const deleted = new Set<number>()
  let failed = 0
  try {
    for (let offset = 0; offset < ids.length; offset += maxBatchDeleteAccounts) {
      const batch = ids.slice(offset, offset + maxBatchDeleteAccounts)
      try {
        const result = await adminAPI.accounts.batchDelete(batch)
        for (const id of result.success_ids ?? []) deleted.add(id)
        failed += result.failed
      } catch {
        failed += batch.length
      }
    }
    if (deleted.size > 0) {
      accounts.value = accounts.value.filter((account) => !deleted.has(account.id))
      selectedAccountIds.value = selectedAccountIds.value.filter((id) => !deleted.has(id))
      for (const id of deleted) scanResults.delete(id)
      appStore.showSuccess(t('admin.accountHealthDetector.messages.deleteSuccess', { count: deleted.size }))
    }
    if (failed > 0) {
      if (deleted.size > 0) {
        appStore.showError(t('admin.accountHealthDetector.messages.deletePartial', { success: deleted.size, failed }))
      } else {
        appStore.showError(t('admin.accountHealthDetector.messages.deleteFailed'))
      }
    }
  } finally {
    deleting.value = false
    deleteDialog.ids = []
    deleteDialog.label = ''
  }
}

function handlePageSizeChange(pageSize: number) {
  pagination.pageSize = pageSize
  pagination.page = 1
}

function handleSort(key: string, order: 'asc' | 'desc') {
  const sortKey = key as AccountHealthSortKey
  if (!sortableColumnKeys.has(sortKey)) return
  sortState.key = sortKey
  sortState.order = order
  pagination.page = 1
}

function handleMobileSortKey(value: string | number | boolean | null) {
  if (typeof value === 'string') handleSort(value, sortState.order)
}

function toggleMobileSortOrder() {
  handleSort(sortState.key, sortState.order === 'asc' ? 'desc' : 'asc')
}

function outcomeLabel(outcome: DetectionOutcome): string {
  return t(`admin.accountHealthDetector.outcome.${outcome}`)
}

function outcomeClass(outcome: DetectionOutcome): string {
  if (['deactivated', 'format_error', 'parse_error', 'failed'].includes(outcome)) return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
  if (['warning', 'mismatch', 'fetch_error'].includes(outcome)) return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
  if (outcome === 'reactivated') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
  if (['queued', 'checking'].includes(outcome)) return 'bg-blue-100 text-blue-700 dark:bg-blue-900/40 dark:text-blue-300'
  return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300'
}

function accountStatusLabel(status: string): string {
  const normalized = status === 'active' ? 'active' : status === 'error' ? 'error' : 'inactive'
  return t(`admin.accountHealthDetector.localStatus.${normalized}`)
}

function accountStatusClass(status: string): string {
  if (status === 'active') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
  if (status === 'error') return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
  return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300'
}

function banStatusLabel(status: BanStatus): string {
  return t(`admin.accountHealthDetector.banStatus.${status}`)
}

function banStatusClass(status: BanStatus): string {
  if (status === 'banned') return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
  if (status === 'reactivated') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
  if (['warning', 'mismatch'].includes(status)) return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
  return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300'
}

function plusStatusLabel(status: PlusStatus, score: number | null = null): string {
  if (status === 'detected' && score != null && Number.isFinite(score)) return t('admin.accountHealthDetector.plusStatus.detectedWithScore', { score })
  return t(`admin.accountHealthDetector.plusStatus.${status}`)
}

function plusStatusClass(status: PlusStatus): string {
  if (status === 'banned') return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
  if (status === 'detected') return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300'
  return 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-300'
}

function formatScore(value: number | null): string {
  return value == null || !Number.isFinite(value) ? '-' : t('admin.accountHealthDetector.values.score', { value })
}

function formatLifespan(row: AccountHealthRow): string {
  if (row.lifespan_status === 'ban_date_unknown') return t('admin.accountHealthDetector.lifespan.banDateUnknown')
  if (row.lifespan_status === 'invalid_date') return t('admin.accountHealthDetector.lifespan.invalidDate')
  if (row.lifespan == null || !Number.isFinite(row.lifespan)) return '-'
  const seconds = Math.max(0, Math.floor(row.lifespan))
  const days = Math.floor(seconds / 86_400)
  const hours = Math.floor((seconds % 86_400) / 3_600)
  const key = row.lifespan_status === 'active' ? 'active' : 'ended'
  return t(`admin.accountHealthDetector.lifespan.${key}`, { days, hours })
}

function formatMessagePageCount(row: AccountHealthRow): string {
  return row.messages_scanned == null || row.pages_scanned == null ? '-' : `${row.messages_scanned}/${row.pages_scanned}`
}

function formatElapsed(value: number | null): string {
  if (value == null || !Number.isFinite(value)) return '-'
  return value < 1000 ? `${Math.round(value)} ms` : `${(value / 1000).toFixed(2)} s`
}

function formatCheckedAt(value: string): string {
  if (!value) return '-'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString(locale.value)
}

onMounted(() => { void loadGroups() })
onUnmounted(() => {
  groupsController?.abort()
  accountsController?.abort()
  scanController?.abort()
})
</script>

<style scoped>
@media (min-width: 1024px) {
  .account-health-detector-layout {
    height: auto !important;
    min-height: calc(100vh - 64px - 4rem);
  }

  .account-health-detector-layout :deep(.layout-section-scrollable) {
    flex: none;
    height: clamp(20rem, 45vh, 36rem);
    min-height: 20rem;
  }
}
</style>
