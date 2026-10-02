<template>
  <AppLayout>
    <div class="space-y-6 pb-12">
      <header class="flex flex-col gap-4 rounded-2xl border border-gray-200 bg-white p-5 shadow-sm dark:border-dark-700 dark:bg-dark-800 sm:flex-row sm:items-start sm:justify-between">
        <div class="min-w-0">
          <RouterLink
            to="/admin/ops"
            class="mb-3 inline-flex items-center gap-1 text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
          >
            <span aria-hidden="true">←</span>
            {{ t('admin.ops.hermes.backToOps') }}
          </RouterLink>
          <h1 class="text-xl font-semibold text-gray-900 dark:text-white sm:text-2xl">
            {{ t('admin.ops.hermes.title') }}
          </h1>
          <p class="mt-1 max-w-3xl text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.ops.hermes.description') }}
          </p>
        </div>

        <div class="flex flex-wrap items-center gap-3 sm:justify-end">
          <label class="inline-flex min-h-10 cursor-pointer items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
            <input
              v-model="autoRefreshEnabled"
              type="checkbox"
              class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-500 dark:bg-dark-700"
              :aria-label="t('admin.ops.hermes.autoRefresh')"
            />
            <span>{{ t('admin.ops.hermes.autoRefresh') }}</span>
          </label>
          <button
            type="button"
            class="btn btn-secondary inline-flex min-h-10 items-center gap-2"
            :disabled="loading || refreshing"
            data-testid="hermes-refresh"
            @click="loadStatus()"
          >
            <span aria-hidden="true" :class="['text-base leading-none', { 'animate-spin': loading || refreshing }]">↻</span>
            <span>{{ loading || refreshing ? t('admin.ops.hermes.refreshing') : t('admin.ops.hermes.refresh') }}</span>
          </button>
        </div>
      </header>

      <div
        v-if="errorMessage && status"
        class="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300"
        role="alert"
        data-testid="hermes-refresh-error"
      >
        <span>{{ errorMessage }}</span>
        <button type="button" class="font-medium underline underline-offset-2" @click="loadStatus()">
          {{ t('admin.ops.hermes.retry') }}
        </button>
      </div>

      <div
        v-if="loading"
        class="flex min-h-64 flex-col items-center justify-center rounded-2xl border border-gray-200 bg-white p-8 text-center dark:border-dark-700 dark:bg-dark-800"
        role="status"
        aria-live="polite"
        data-testid="hermes-loading"
      >
        <span class="mb-4 h-9 w-9 animate-spin rounded-full border-2 border-primary-200 border-t-primary-600 dark:border-primary-900 dark:border-t-primary-400" aria-hidden="true"></span>
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('admin.ops.hermes.loading') }}</p>
      </div>

      <div
        v-else-if="!status"
        class="flex min-h-64 flex-col items-center justify-center rounded-2xl border border-gray-200 bg-white p-8 text-center dark:border-dark-700 dark:bg-dark-800"
        data-testid="hermes-empty"
      >
        <div class="mb-4 flex h-14 w-14 items-center justify-center rounded-2xl bg-gray-100 text-2xl dark:bg-dark-700" aria-hidden="true">◌</div>
        <p class="text-base font-medium text-gray-900 dark:text-white">{{ t('admin.ops.hermes.noSnapshot') }}</p>
        <p class="mt-2 max-w-md text-sm text-gray-500 dark:text-gray-400">{{ errorMessage || t('admin.ops.hermes.sinceStartup') }}</p>
        <button type="button" class="btn btn-primary mt-5" @click="loadStatus()">{{ t('admin.ops.hermes.retry') }}</button>
      </div>

      <template v-else>
        <section
          class="rounded-2xl border p-5 shadow-sm"
          :class="statusPanelClass"
          aria-live="polite"
          data-testid="hermes-health-panel"
        >
          <div class="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
            <div class="flex items-start gap-3">
              <span class="mt-1 flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-full bg-white/70 text-xl dark:bg-dark-900/40" aria-hidden="true">✦</span>
              <div>
                <p class="text-xs font-semibold uppercase tracking-wide opacity-75">{{ t('admin.ops.hermes.statusLabel') }}</p>
                <h2 class="mt-1 text-2xl font-semibold">{{ statusLabel }}</h2>
                <p class="mt-1 max-w-2xl text-sm opacity-85">{{ statusHint }}</p>
              </div>
            </div>
            <span
              class="inline-flex w-fit items-center rounded-full border px-3 py-1 text-sm font-semibold"
              :class="statusBadgeClass"
              data-testid="hermes-status-badge"
            >
              {{ statusLabel }}
            </span>
          </div>
          <p v-if="lastFetchedAt" class="mt-4 text-xs opacity-75" data-testid="hermes-last-fetched">
            {{ t('admin.ops.hermes.lastFetched', { time: formatTimestamp(lastFetchedAt) }) }}
          </p>
        </section>

        <section class="grid grid-cols-1 gap-6 lg:grid-cols-2" :aria-label="t('admin.ops.hermes.runtimeSection')">
          <article class="card p-5" data-testid="hermes-runtime-card">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.ops.hermes.runtimeTitle') }}</h2>
            <dl class="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div>
                <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.lifecycle') }}</dt>
                <dd class="mt-1 font-medium text-gray-900 dark:text-gray-100">{{ displayValue(status.lifecycle_state) }}</dd>
              </div>
              <div>
                <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.lease') }}</dt>
                <dd class="mt-1 font-medium" :class="status.lease_healthy ? 'text-emerald-600 dark:text-emerald-400' : 'text-amber-600 dark:text-amber-400'">
                  {{ status.lease_held ? t('admin.ops.hermes.leaseHeld') : t('admin.ops.hermes.leaseNotHeld') }}
                  <span class="text-xs font-normal opacity-80">· {{ status.lease_healthy ? t('admin.ops.hermes.leaseHealthy') : t('admin.ops.hermes.leaseUnhealthy') }}</span>
                </dd>
              </div>
              <div>
                <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.enabled') }}</dt>
                <dd class="mt-1 font-medium text-gray-900 dark:text-gray-100">{{ status.enabled ? t('admin.ops.hermes.enabled') : t('admin.ops.hermes.disabled') }}</dd>
              </div>
              <div>
                <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.startedAt') }}</dt>
                <dd class="mt-1 break-words font-medium text-gray-900 dark:text-gray-100">{{ formatTimestamp(status.started_at) }}</dd>
              </div>
              <div>
                <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.observedAt') }}</dt>
                <dd class="mt-1 break-words font-medium text-gray-900 dark:text-gray-100">{{ formatTimestamp(status.observed_at) }}</dd>
              </div>
              <div>
                <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.nextRunAt') }}</dt>
                <dd class="mt-1 break-words font-medium text-gray-900 dark:text-gray-100">{{ formatTimestamp(status.next_run_at) }}</dd>
              </div>
            </dl>
          </article>

          <article class="card p-5" data-testid="hermes-lease-card">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.ops.hermes.leaseTimeline') }}</h2>
            <dl class="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div>
                <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.lastLeaseAcquiredAt') }}</dt>
                <dd class="mt-1 break-words font-medium text-gray-900 dark:text-gray-100">{{ formatTimestamp(status.last_lease_acquired_at) }}</dd>
              </div>
              <div>
                <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.lastLeaseLostAt') }}</dt>
                <dd class="mt-1 break-words font-medium text-gray-900 dark:text-gray-100">{{ formatTimestamp(status.last_lease_lost_at) }}</dd>
              </div>
              <div class="sm:col-span-2">
                <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.lastReacquiredAt') }}</dt>
                <dd class="mt-1 break-words font-medium text-gray-900 dark:text-gray-100">{{ formatTimestamp(status.last_reacquired_at) }}</dd>
              </div>
            </dl>
          </article>
        </section>

        <article class="card p-5" data-testid="hermes-config-card">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.ops.hermes.configuration') }}</h2>
          <dl class="mt-4 grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-5">
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.interval') }}</dt>
              <dd class="mt-1 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatSeconds(status.config?.interval_seconds) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.batchSize') }}</dt>
              <dd class="mt-1 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatCount(status.config?.batch_size) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.concurrency') }}</dt>
              <dd class="mt-1 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatCount(status.config?.concurrency) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.timeout') }}</dt>
              <dd class="mt-1 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatSeconds(status.config?.timeout_seconds) }}</dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.jitter') }}</dt>
              <dd class="mt-1 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ formatSeconds(status.config?.jitter_seconds) }}</dd>
            </div>
          </dl>
        </article>

        <section class="grid grid-cols-1 gap-6 lg:grid-cols-2" :aria-label="t('admin.ops.hermes.runsSection')">
          <article class="card p-5" data-testid="hermes-current-run-card">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.ops.hermes.currentRun') }}</h2>
            <div v-if="status.current_run" class="mt-4 space-y-3">
              <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <div>
                  <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.trigger') }}</p>
                  <p class="mt-1 font-medium text-gray-900 dark:text-gray-100">{{ displayValue(status.current_run.trigger) }}</p>
                </div>
                <div>
                  <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.runStartedAt') }}</p>
                  <p class="mt-1 break-words font-medium text-gray-900 dark:text-gray-100">{{ formatTimestamp(status.current_run.started_at) }}</p>
                </div>
              </div>
              <p class="rounded-lg bg-primary-50 px-3 py-2 text-sm text-primary-700 dark:bg-primary-500/10 dark:text-primary-300">{{ t('admin.ops.hermes.currentRunHint') }}</p>
            </div>
            <p v-else class="mt-4 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.noCurrentRun') }}</p>
          </article>

          <article class="card p-5" data-testid="hermes-last-run-card">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.ops.hermes.lastRun') }}</h2>
            <div v-if="status.last_run" class="mt-4 space-y-4">
              <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
                <div>
                  <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.trigger') }}</p>
                  <p class="mt-1 font-medium text-gray-900 dark:text-gray-100">{{ displayValue(status.last_run.trigger) }}</p>
                </div>
                <div>
                  <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.runStartedAt') }}</p>
                  <p class="mt-1 break-words font-medium text-gray-900 dark:text-gray-100">{{ formatTimestamp(status.last_run.started_at) }}</p>
                </div>
                <div>
                  <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.runCompletedAt') }}</p>
                  <p class="mt-1 break-words font-medium text-gray-900 dark:text-gray-100">{{ formatTimestamp(status.last_run.completed_at) }}</p>
                </div>
              </div>
              <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
                <div v-for="item in lastRunMetrics" :key="item.key" class="rounded-lg bg-gray-50 px-3 py-2 dark:bg-dark-700/60">
                  <p class="truncate text-xs text-gray-500 dark:text-gray-400">{{ item.label }}</p>
                  <p class="mt-1 text-lg font-semibold tabular-nums text-gray-900 dark:text-white">{{ item.value }}</p>
                </div>
              </div>
              <p v-if="status.last_run.last_error" class="break-words rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300">
                <span class="font-medium">{{ t('admin.ops.hermes.lastError') }}:</span>
                {{ status.last_run.last_error }}
              </p>
            </div>
            <p v-else class="mt-4 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.ops.hermes.noLastRun') }}</p>
          </article>
        </section>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { opsAPI, type OpsHermesStatusResponse } from '@/api/admin/ops'
import { formatDateTime, formatNumber } from '@/utils/format'

type HermesTone = 'healthy' | 'warning' | 'error' | 'disabled' | 'unknown'

const { t } = useI18n()
const status = ref<OpsHermesStatusResponse | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const errorMessage = ref('')
const lastFetchedAt = ref<Date | null>(null)
const autoRefreshEnabled = ref(false)
const requestInFlight = ref(false)
let refreshTimer: ReturnType<typeof setInterval> | null = null

const tone = computed<HermesTone>(() => {
  const snapshot = status.value
  if (!snapshot) return 'unknown'
  const normalizedStatus = String(snapshot.status || '').toLowerCase()
  if (!snapshot.enabled || normalizedStatus === 'disabled') return 'disabled'
  if (normalizedStatus === 'error') return 'error'
  if (normalizedStatus === 'unknown') return 'unknown'
  if (normalizedStatus === 'warning') return 'warning'
  // Only a positive runtime status combined with the backend health bit is
  // allowed to render green. A stale/ambiguous status stays amber.
  if (snapshot.healthy && (normalizedStatus === 'running' || normalizedStatus === 'healthy')) return 'healthy'
  if (!snapshot.lease_held || !snapshot.lease_healthy) return 'warning'
  return 'warning'
})

const statusLabel = computed(() => t(`admin.ops.hermes.status.${tone.value}`))

const statusHint = computed(() => {
  switch (tone.value) {
    case 'healthy':
      return t('admin.ops.hermes.healthyHint')
    case 'disabled':
      return t('admin.ops.hermes.disabledHint')
    case 'error':
      return t('admin.ops.hermes.errorHint')
    case 'warning':
      return t('admin.ops.hermes.warningHint')
    default:
      return t('admin.ops.hermes.noSnapshot')
  }
})

const statusPanelClass = computed(() => {
  const classes: Record<HermesTone, string> = {
    healthy: 'border-emerald-200 bg-emerald-50 text-emerald-900 dark:border-emerald-500/30 dark:bg-emerald-500/10 dark:text-emerald-200',
    warning: 'border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200',
    error: 'border-red-200 bg-red-50 text-red-900 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-200',
    disabled: 'border-gray-200 bg-gray-50 text-gray-800 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-200',
    unknown: 'border-gray-200 bg-gray-50 text-gray-800 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-200'
  }
  return classes[tone.value]
})

const statusBadgeClass = computed(() => {
  const classes: Record<HermesTone, string> = {
    healthy: 'border-emerald-300 bg-white/70 text-emerald-700 dark:border-emerald-400/40 dark:bg-dark-900/30 dark:text-emerald-300',
    warning: 'border-amber-300 bg-white/70 text-amber-700 dark:border-amber-400/40 dark:bg-dark-900/30 dark:text-amber-300',
    error: 'border-red-300 bg-white/70 text-red-700 dark:border-red-400/40 dark:bg-dark-900/30 dark:text-red-300',
    disabled: 'border-gray-300 bg-white/70 text-gray-700 dark:border-dark-500 dark:bg-dark-900/30 dark:text-gray-300',
    unknown: 'border-gray-300 bg-white/70 text-gray-700 dark:border-dark-500 dark:bg-dark-900/30 dark:text-gray-300'
  }
  return classes[tone.value]
})

const lastRunMetrics = computed(() => {
  const run = status.value?.last_run
  if (!run) return []
  return [
    { key: 'listed', label: t('admin.ops.hermes.listed'), value: formatCount(run.listed) },
    { key: 'checked', label: t('admin.ops.hermes.checked'), value: formatCount(run.checked) },
    { key: 'recovered', label: t('admin.ops.hermes.recovered'), value: formatCount(run.recovered) },
    { key: 'exhausted', label: t('admin.ops.hermes.exhausted'), value: formatCount(run.exhausted) },
    { key: 'skipped', label: t('admin.ops.hermes.skipped'), value: formatCount(run.skipped) },
    { key: 'unknown', label: t('admin.ops.hermes.unknown'), value: formatCount(run.unknown) },
    { key: 'cas_misses', label: t('admin.ops.hermes.casMisses'), value: formatCount(run.cas_misses) },
    { key: 'errors', label: t('admin.ops.hermes.errors'), value: formatCount(run.errors) },
    { key: 'duration', label: t('admin.ops.hermes.duration'), value: formatDuration(run.duration_ms) }
  ]
})

function formatTimestamp(value: string | Date | null | undefined): string {
  if (!value) return t('admin.ops.hermes.notAvailable')
  const formatted = formatDateTime(value)
  return formatted || t('admin.ops.hermes.notAvailable')
}

function formatCount(value: number | null | undefined): string {
  return typeof value === 'number' && Number.isFinite(value) ? formatNumber(value) : t('admin.ops.hermes.notAvailable')
}

function formatSeconds(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return t('admin.ops.hermes.notAvailable')
  return t('admin.ops.hermes.seconds', { value: formatNumber(value) })
}

function formatDuration(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value)) return t('admin.ops.hermes.notAvailable')
  return t('admin.ops.hermes.milliseconds', { value: formatNumber(value) })
}

function displayValue(value: string | null | undefined): string {
  const text = String(value ?? '').trim()
  return text || t('admin.ops.hermes.notAvailable')
}

async function loadStatus(): Promise<void> {
  // The initial request intentionally starts with `loading=true`; this guard
  // also prevents a slow first request from being duplicated by auto-refresh.
  if (requestInFlight.value) return
  requestInFlight.value = true
  const hasSnapshot = Boolean(status.value)
  if (hasSnapshot) refreshing.value = true
  else loading.value = true
  errorMessage.value = ''

  try {
    status.value = await opsAPI.getHermesStatus()
    lastFetchedAt.value = new Date()
  } catch (error) {
    // Keep the previous snapshot visible on a refresh failure and avoid
    // rendering raw backend error text, which may contain sensitive details.
    console.warn('[HermesStatus] failed to load status', error)
    errorMessage.value = t('admin.ops.hermes.loadFailed')
  } finally {
    loading.value = false
    refreshing.value = false
    requestInFlight.value = false
  }
}

function stopAutoRefresh(): void {
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = null
  }
}

function startAutoRefresh(): void {
  stopAutoRefresh()
  if (!autoRefreshEnabled.value) return
  refreshTimer = setInterval(() => {
    void loadStatus()
  }, 15_000)
}

watch(autoRefreshEnabled, startAutoRefresh)

onMounted(() => {
  void loadStatus()
})

onUnmounted(stopAutoRefresh)
</script>
