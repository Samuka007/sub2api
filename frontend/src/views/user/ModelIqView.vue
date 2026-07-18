<template>
  <AppLayout>
    <section class="mx-auto max-w-[1600px] space-y-4" data-testid="model-iq-page">
      <header class="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
        <div class="min-w-0">
          <div class="flex items-center gap-3">
            <span
              class="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary-100 text-primary-600 dark:bg-primary-900/30 dark:text-primary-400"
            >
              <Icon name="brain" size="lg" />
            </span>
            <div class="min-w-0">
              <h1 class="text-2xl font-bold text-gray-900 dark:text-white">
                {{ t('modelIq.title') }}
              </h1>
              <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
                {{ t('modelIq.description') }}
              </p>
            </div>
          </div>

          <p
            v-if="response"
            class="mt-3 flex items-center gap-1.5 text-xs text-gray-500 dark:text-dark-400"
            data-testid="model-iq-updated-at"
          >
            <Icon name="clock" size="xs" />
            {{ t('modelIq.updatedAt', { time: formattedMonitoredAt }) }}
          </p>
        </div>

        <button
          type="button"
          class="btn btn-secondary h-10 shrink-0 self-start rounded-lg px-3.5 py-2"
          :disabled="loading"
          data-testid="model-iq-refresh"
          @click="refresh"
        >
          <LoadingSpinner v-if="loading" size="sm" color="secondary" />
          <Icon v-else name="refresh" size="sm" />
          {{ loading ? t('modelIq.refreshing') : t('modelIq.refresh') }}
        </button>
      </header>

      <div
        v-if="isStale"
        class="flex items-start gap-3 rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-amber-900 dark:border-amber-800/60 dark:bg-amber-950/30 dark:text-amber-200"
        data-testid="model-iq-stale"
        role="status"
      >
        <Icon name="exclamationTriangle" size="md" class="mt-0.5 shrink-0" />
        <div class="min-w-0">
          <p class="text-sm font-semibold">{{ t('modelIq.staleTitle') }}</p>
          <p class="mt-0.5 text-xs leading-5 text-amber-800 dark:text-amber-300">
            {{ staleDescription }}
          </p>
        </div>
      </div>

      <div
        v-if="isInitialLoading"
        class="overflow-hidden rounded-lg border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900"
        data-testid="model-iq-loading-skeleton"
        aria-busy="true"
      >
        <div class="flex items-center gap-2 border-b border-gray-100 px-4 py-3 text-sm text-gray-500 dark:border-dark-700 dark:text-dark-400">
          <LoadingSpinner size="sm" color="secondary" />
          {{ t('modelIq.loading') }}
        </div>
        <div class="space-y-3 p-4">
          <div
            v-for="index in 6"
            :key="index"
            class="grid animate-pulse grid-cols-[36px_minmax(140px,1.5fr)_repeat(4,minmax(72px,0.7fr))] gap-4"
          >
            <span class="h-5 rounded bg-gray-100 dark:bg-dark-800"></span>
            <span class="h-5 rounded bg-gray-100 dark:bg-dark-800"></span>
            <span class="h-5 rounded bg-gray-100 dark:bg-dark-800"></span>
            <span class="h-5 rounded bg-gray-100 dark:bg-dark-800"></span>
            <span class="h-5 rounded bg-gray-100 dark:bg-dark-800"></span>
            <span class="h-5 rounded bg-gray-100 dark:bg-dark-800"></span>
          </div>
        </div>
      </div>

      <div
        v-else-if="errorMessage && !response"
        class="rounded-lg border border-red-200 bg-white px-5 py-12 text-center dark:border-red-900/60 dark:bg-dark-900"
        data-testid="model-iq-error"
        role="alert"
      >
        <span class="mx-auto flex h-11 w-11 items-center justify-center rounded-lg bg-red-50 text-red-600 dark:bg-red-950/40 dark:text-red-400">
          <Icon name="exclamationCircle" size="lg" />
        </span>
        <h2 class="mt-4 text-base font-semibold text-gray-900 dark:text-white">
          {{ t('modelIq.errorTitle') }}
        </h2>
        <p class="mx-auto mt-1 max-w-lg text-sm text-gray-500 dark:text-dark-400">
          {{ errorMessage }}
        </p>
        <button type="button" class="btn btn-secondary mt-5 rounded-lg" @click="refresh">
          <Icon name="refresh" size="sm" />
          {{ t('modelIq.retry') }}
        </button>
      </div>

      <div
        v-else-if="rows.length === 0"
        class="rounded-lg border border-dashed border-gray-300 bg-white px-5 py-12 text-center dark:border-dark-600 dark:bg-dark-900"
        data-testid="model-iq-empty"
      >
        <span class="mx-auto flex h-11 w-11 items-center justify-center rounded-lg bg-gray-100 text-gray-500 dark:bg-dark-800 dark:text-dark-400">
          <Icon name="chart" size="lg" />
        </span>
        <h2 class="mt-4 text-base font-semibold text-gray-900 dark:text-white">
          {{ t('modelIq.emptyTitle') }}
        </h2>
        <p class="mx-auto mt-1 max-w-lg text-sm text-gray-500 dark:text-dark-400">
          {{ t('modelIq.emptyDescription') }}
        </p>
      </div>

      <template v-else>
        <div
          class="hidden overflow-x-auto rounded-lg border border-gray-200 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-900 md:block"
          data-testid="model-iq-desktop-table"
        >
          <table class="w-full min-w-[1120px] table-fixed text-sm" :aria-label="t('modelIq.tableLabel')">
            <colgroup>
              <col class="w-14" />
              <col class="w-52" />
              <col class="w-28" />
              <col class="w-24" />
              <col class="w-24" />
              <col class="w-32" />
              <col class="w-28" />
              <col class="w-28" />
              <col class="w-28" />
              <col class="w-36" />
            </colgroup>
            <thead class="bg-gray-50 text-xs font-semibold uppercase text-gray-500 dark:bg-dark-800/70 dark:text-dark-400">
              <tr>
                <th scope="col" class="px-3 py-2.5 text-center">{{ t('modelIq.columns.rank') }}</th>
                <th scope="col" class="px-3 py-2.5 text-left">{{ t('modelIq.columns.model') }}</th>
                <th scope="col" class="px-3 py-2.5 text-left">{{ t('modelIq.columns.effort') }}</th>
                <th scope="col" class="px-3 py-2.5 text-right">{{ t('modelIq.columns.score') }}</th>
                <th scope="col" class="px-3 py-2.5 text-left">{{ t('modelIq.columns.status') }}</th>
                <th scope="col" class="px-3 py-2.5 text-right">{{ t('modelIq.columns.passRate') }}</th>
                <th scope="col" class="px-3 py-2.5 text-right">{{ t('modelIq.columns.trend') }}</th>
                <th scope="col" class="px-3 py-2.5 text-right">{{ t('modelIq.columns.duration') }}</th>
                <th scope="col" class="px-3 py-2.5 text-right">{{ t('modelIq.columns.cost') }}</th>
                <th scope="col" class="px-3 py-2.5 text-right">{{ t('modelIq.columns.date') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-800">
              <tr
                v-for="row in rows"
                :key="row.key"
                class="transition-colors hover:bg-gray-50/80 dark:hover:bg-dark-800/40"
                data-testid="model-iq-row"
              >
                <td class="px-3 py-2.5 text-center">
                  <span class="inline-flex h-7 w-7 items-center justify-center rounded-full bg-gray-100 text-xs font-bold text-gray-700 dark:bg-dark-800 dark:text-gray-200">
                    {{ row.rank }}
                  </span>
                </td>
                <td class="px-3 py-2.5">
                  <p
                    class="truncate font-semibold text-gray-900 dark:text-white"
                    data-testid="model-iq-label"
                    :title="row.label"
                  >
                    {{ row.label }}
                  </p>
                  <p
                    class="mt-0.5 truncate text-xs text-gray-500 dark:text-dark-400"
                    :title="row.model"
                  >
                    {{ row.model }}
                  </p>
                </td>
                <td class="px-3 py-2.5 text-gray-700 dark:text-gray-300">
                  {{ effortLabel(row.reasoning_effort) }}
                </td>
                <td class="px-3 py-2.5 text-right text-base font-bold tabular-nums text-gray-900 dark:text-white">
                  {{ formatNumber(row.latest.score) }}
                </td>
                <td class="px-3 py-2.5">
                  <span :class="statusClass(row.latest.status)" class="inline-flex items-center gap-1.5 rounded-full px-2 py-1 text-xs font-semibold">
                    <span class="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true"></span>
                    {{ statusLabel(row.latest.status) }}
                  </span>
                </td>
                <td class="px-3 py-2.5 text-right tabular-nums text-gray-700 dark:text-gray-300">
                  <span class="font-medium">{{ formatPassRate(row) }}</span>
                  <span class="block text-xs text-gray-400 dark:text-dark-500">{{ passFraction(row) }}</span>
                </td>
                <td class="px-3 py-2.5 text-right">
                  <span :class="trendClass(row.trend)" class="inline-flex items-center justify-end gap-1 font-semibold tabular-nums">
                    <Icon v-if="row.trend !== null && row.trend > 0" name="arrowUp" size="xs" />
                    <Icon v-else-if="row.trend !== null && row.trend < 0" name="arrowDown" size="xs" />
                    {{ formatTrend(row.trend) }}
                  </span>
                </td>
                <td class="px-3 py-2.5 text-right tabular-nums text-gray-700 dark:text-gray-300">
                  {{ row.latest.wall_time_human || '--' }}
                </td>
                <td class="px-3 py-2.5 text-right font-medium tabular-nums text-gray-700 dark:text-gray-300">
                  {{ formatCost(row.latest.cost_usd) }}
                </td>
                <td class="px-3 py-2.5 text-right text-xs tabular-nums text-gray-600 dark:text-dark-300">
                  {{ formatTestDate(row.latest.date) }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div class="space-y-3 md:hidden" data-testid="model-iq-mobile-list">
          <article
            v-for="row in rows"
            :key="row.key"
            class="rounded-lg border border-gray-200 bg-white p-4 shadow-sm dark:border-dark-700 dark:bg-dark-900"
            data-testid="model-iq-card"
          >
            <div class="flex items-start justify-between gap-3">
              <div class="flex min-w-0 items-start gap-3">
                <span class="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-full bg-gray-100 text-xs font-bold text-gray-700 dark:bg-dark-800 dark:text-gray-200">
                  {{ row.rank }}
                </span>
                <div class="min-w-0">
                  <h2 class="break-words text-sm font-semibold text-gray-900 dark:text-white">{{ row.label }}</h2>
                  <p class="mt-0.5 break-all text-xs text-gray-500 dark:text-dark-400">{{ row.model }}</p>
                </div>
              </div>
              <div class="shrink-0 text-right">
                <p class="text-xl font-bold tabular-nums text-gray-900 dark:text-white">{{ formatNumber(row.latest.score) }}</p>
                <span :class="statusClass(row.latest.status)" class="mt-1 inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-semibold">
                  <span class="h-1.5 w-1.5 rounded-full bg-current" aria-hidden="true"></span>
                  {{ statusLabel(row.latest.status) }}
                </span>
              </div>
            </div>

            <dl class="mt-4 grid grid-cols-2 gap-x-4 gap-y-3 border-t border-gray-100 pt-3 text-xs dark:border-dark-800">
              <div>
                <dt class="text-gray-500 dark:text-dark-400">{{ t('modelIq.columns.effort') }}</dt>
                <dd class="mt-1 font-medium text-gray-800 dark:text-gray-200">{{ effortLabel(row.reasoning_effort) }}</dd>
              </div>
              <div class="text-right">
                <dt class="text-gray-500 dark:text-dark-400">{{ t('modelIq.columns.passRate') }}</dt>
                <dd class="mt-1 font-medium tabular-nums text-gray-800 dark:text-gray-200">{{ formatPassRate(row) }} · {{ passFraction(row) }}</dd>
              </div>
              <div>
                <dt class="text-gray-500 dark:text-dark-400">{{ t('modelIq.columns.trend') }}</dt>
                <dd :class="trendClass(row.trend)" class="mt-1 inline-flex items-center gap-1 font-semibold tabular-nums">
                  <Icon v-if="row.trend !== null && row.trend > 0" name="arrowUp" size="xs" />
                  <Icon v-else-if="row.trend !== null && row.trend < 0" name="arrowDown" size="xs" />
                  {{ formatTrend(row.trend) }}
                </dd>
              </div>
              <div class="text-right">
                <dt class="text-gray-500 dark:text-dark-400">{{ t('modelIq.columns.duration') }}</dt>
                <dd class="mt-1 font-medium tabular-nums text-gray-800 dark:text-gray-200">{{ row.latest.wall_time_human || '--' }}</dd>
              </div>
              <div>
                <dt class="text-gray-500 dark:text-dark-400">{{ t('modelIq.columns.cost') }}</dt>
                <dd class="mt-1 font-medium tabular-nums text-gray-800 dark:text-gray-200">{{ formatCost(row.latest.cost_usd) }}</dd>
              </div>
              <div class="text-right">
                <dt class="text-gray-500 dark:text-dark-400">{{ t('modelIq.columns.date') }}</dt>
                <dd class="mt-1 font-medium tabular-nums text-gray-800 dark:text-gray-200">{{ formatTestDate(row.latest.date) }}</dd>
              </div>
            </dl>
          </article>
        </div>
      </template>
    </section>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import Icon from '@/components/icons/Icon.vue'
import { getCurrentModelIq } from '@/api/modelIq'
import type { ModelIqComparison, ModelIqCurrentResponse, ModelIqDayResult } from '@/api/modelIq'

const AUTO_REFRESH_MS = 10 * 60 * 1000
interface ModelIqTableRow extends ModelIqComparison {
  key: string
  rank: number
  trend: number | null
}

interface RequestError {
  status?: number
  code?: string
  name?: string
  response?: {
    status?: number
  }
}

const { t, locale } = useI18n()
const response = ref<ModelIqCurrentResponse | null>(null)
const loading = ref(false)
const errorMessage = ref('')
const refreshFailed = ref(false)

let abortController: AbortController | null = null
let refreshTimer: ReturnType<typeof setInterval> | null = null

const isInitialLoading = computed(() => loading.value && response.value === null)

const formattedMonitoredAt = computed(() => {
  const value = response.value?.monitored_at ?? response.value?.fetched_at
  if (!value) return '--'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat(locale.value, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date)
})

const isStale = computed(() => (
  response.value !== null && (response.value.stale || refreshFailed.value)
))

const staleDescription = computed(() => (
  refreshFailed.value
    ? t('modelIq.staleRefreshFailed')
    : t('modelIq.staleSource')
))

const rows = computed<ModelIqTableRow[]>(() => {
  const comparisons = response.value?.model_iq?.comparisons ?? {}
  return Object.entries(comparisons)
    .map(([key, comparison]) => ({
      ...comparison,
      key,
      rank: 0,
      trend: calculateTrend(comparison.recent_days),
    }))
    .sort(compareRows)
    .map((row, index) => ({ ...row, rank: index + 1 }))
})

function numericValue(value: number | null | undefined, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function compareRows(a: ModelIqTableRow, b: ModelIqTableRow): number {
  const scoreDifference = numericValue(b.latest.score, Number.NEGATIVE_INFINITY)
    - numericValue(a.latest.score, Number.NEGATIVE_INFINITY)
  if (scoreDifference !== 0) return scoreDifference

  const passedDifference = numericValue(b.latest.passed, Number.NEGATIVE_INFINITY)
    - numericValue(a.latest.passed, Number.NEGATIVE_INFINITY)
  if (passedDifference !== 0) return passedDifference

  const costDifference = numericValue(a.latest.cost_usd, Number.POSITIVE_INFINITY)
    - numericValue(b.latest.cost_usd, Number.POSITIVE_INFINITY)
  if (costDifference !== 0) return costDifference

  const wallDifference = numericValue(a.latest.wall_seconds, Number.POSITIVE_INFINITY)
    - numericValue(b.latest.wall_seconds, Number.POSITIVE_INFINITY)
  if (wallDifference !== 0) return wallDifference

  return a.label.localeCompare(b.label, locale.value)
}

function benchmarkTimestamp(value: string): number | null {
  const match = /^(\d{4})-(\d{2})-(\d{2})(?:-(am|pm))?$/i.exec(value)
  if (!match) {
    const parsed = Date.parse(value)
    return Number.isFinite(parsed) ? parsed : null
  }
  const [, year, month, day, period] = match
  return Date.UTC(Number(year), Number(month) - 1, Number(day), period?.toLowerCase() === 'pm' ? 12 : 0)
}

function calculateTrend(history: ModelIqDayResult[]): number | null {
  const points = history
    .map((item, index) => ({ item, index, timestamp: benchmarkTimestamp(item.date) }))
    .filter((point) => Number.isFinite(point.item.score))
    .sort((a, b) => {
      if (a.timestamp !== null && b.timestamp !== null) return a.timestamp - b.timestamp
      return a.index - b.index
    })
    .slice(-7)

  if (points.length < 2) return null
  return points[points.length - 1].item.score - points[0].item.score
}

function requestErrorMessage(error: unknown): string {
  const requestError = typeof error === 'object' && error !== null
    ? error as RequestError
    : undefined
  const status = requestError?.status ?? requestError?.response?.status
  if (status === 401 || status === 403) return t('modelIq.errors.unauthorized')
  if (status === 429) return t('modelIq.errors.rateLimited')
  if (status === 0 || requestError?.code === 'ERR_NETWORK') return t('modelIq.errors.network')
  return t('modelIq.errors.loadFailed')
}

function isCancellation(error: unknown): boolean {
  if (!error || typeof error !== 'object') return false
  const value = error as RequestError
  return value.name === 'AbortError' || value.code === 'ERR_CANCELED'
}

async function load(): Promise<void> {
  abortController?.abort()
  const controller = new AbortController()
  abortController = controller
  loading.value = true
  if (!response.value) errorMessage.value = ''

  try {
    const data = await getCurrentModelIq({ signal: controller.signal })
    if (controller.signal.aborted || abortController !== controller) return
    response.value = data
    errorMessage.value = ''
    refreshFailed.value = false
  } catch (error: unknown) {
    if (isCancellation(error) || abortController !== controller) return
    errorMessage.value = requestErrorMessage(error)
    refreshFailed.value = response.value !== null
  } finally {
    if (abortController === controller) {
      abortController = null
      loading.value = false
    }
  }
}

function refresh(): void {
  void load()
}

function formatNumber(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '--'
  return new Intl.NumberFormat(locale.value, { maximumFractionDigits: 1 }).format(value)
}

function formatCost(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return '--'
  if (value > 0 && value < 0.0001) return '<$0.0001'
  return new Intl.NumberFormat('en-US', {
    style: 'currency',
    currency: 'USD',
    minimumFractionDigits: 2,
    maximumFractionDigits: 4,
  }).format(value)
}

function validTasks(row: ModelIqTableRow): number {
  const explicit = numericValue(row.latest.valid_tasks, Number.NaN)
  if (Number.isFinite(explicit)) return Math.max(0, explicit)
  return Math.max(0, numericValue(row.latest.tasks, 0) - numericValue(row.latest.invalid, 0))
}

function formatPassRate(row: ModelIqTableRow): string {
  const total = validTasks(row)
  if (total <= 0) return '--'
  const percentage = Math.max(0, Math.min(100, (numericValue(row.latest.passed, 0) / total) * 100))
  return `${new Intl.NumberFormat(locale.value, { maximumFractionDigits: 0 }).format(percentage)}%`
}

function passFraction(row: ModelIqTableRow): string {
  const total = validTasks(row)
  return total > 0 ? `${numericValue(row.latest.passed, 0)}/${total}` : '--'
}

function formatTrend(value: number | null): string {
  if (value === null || !Number.isFinite(value)) return '--'
  if (value === 0) return '0'
  return `${value > 0 ? '+' : ''}${new Intl.NumberFormat(locale.value, { maximumFractionDigits: 1 }).format(value)}`
}

function trendClass(value: number | null): string {
  if (value === null || value === 0) return 'text-gray-500 dark:text-dark-400'
  return value > 0
    ? 'text-emerald-600 dark:text-emerald-400'
    : 'text-red-600 dark:text-red-400'
}

function statusLabel(status: string): string {
  const normalized = status.toLowerCase()
  if (normalized === 'green') return t('modelIq.status.green')
  if (normalized === 'yellow') return t('modelIq.status.yellow')
  if (normalized === 'red') return t('modelIq.status.red')
  return t('modelIq.status.unknown')
}

function statusClass(status: string): string {
  const normalized = status.toLowerCase()
  if (normalized === 'green') return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-950/40 dark:text-emerald-400'
  if (normalized === 'yellow') return 'bg-amber-50 text-amber-700 dark:bg-amber-950/40 dark:text-amber-400'
  if (normalized === 'red') return 'bg-red-50 text-red-700 dark:bg-red-950/40 dark:text-red-400'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-800 dark:text-dark-300'
}

function effortLabel(effort: string): string {
  const normalized = effort.toLowerCase()
  if (['low', 'medium', 'high', 'xhigh', 'max'].includes(normalized)) {
    return t(`modelIq.effort.${normalized}`)
  }
  return effort || '--'
}

function formatTestDate(value: string): string {
  const match = /^(\d{4}-\d{2}-\d{2})(?:-(am|pm))?$/i.exec(value)
  if (!match) return value || '--'
  const period = match[2]?.toLowerCase()
  return period ? `${match[1]} ${t(`modelIq.period.${period}`)}` : match[1]
}

onMounted(() => {
  void load()
  refreshTimer = setInterval(() => {
    void load()
  }, AUTO_REFRESH_MS)
})

onBeforeUnmount(() => {
  abortController?.abort()
  abortController = null
  if (refreshTimer !== null) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
})
</script>
