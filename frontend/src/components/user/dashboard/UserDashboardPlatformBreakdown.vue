<template>
  <section
    class="platform-breakdown border border-[#dfe5e3] bg-white dark:border-[#273439] dark:bg-[#121c20]"
    :aria-labelledby="headingId"
  >
    <div class="flex items-center justify-between gap-4 border-b border-[#e7ecea] px-5 py-4 dark:border-[#28353a]">
      <h2 :id="headingId" class="text-sm font-semibold text-[#1b2523] dark:text-[#eff5f3]">
        {{ t('dashboard.platformBreakdown') }}
      </h2>
      <span class="text-[11px] font-medium text-[#87928f] dark:text-[#7f918d]">
        {{ t('dashboard.platformCount', { count: platformRows.length }) }}
      </span>
    </div>

    <div class="grid grid-cols-1 gap-2 p-3 sm:grid-cols-2 xl:grid-cols-4">
      <article
        v-for="row in platformRows"
        :key="row.key"
        class="platform-card min-w-0 border border-[#e3e8e6] bg-[#fcfdfd] px-4 py-3.5 dark:border-[#2a383d] dark:bg-[#10191c]"
        :data-testid="`platform-card-${row.key}`"
      >
        <div class="flex items-start justify-between gap-4">
          <div class="flex min-w-0 items-center gap-2">
            <span class="platform-mark" :class="row.tone" aria-hidden="true"></span>
            <h3 class="truncate text-sm font-semibold text-[#26312f] dark:text-[#e5ecea]">
              {{ row.label }}
            </h3>
          </div>
          <div class="min-w-0 text-right">
            <p class="text-[10px] text-[#929d99] dark:text-[#728480]">
              {{ t('dashboard.platformTotalCost') }}
            </p>
            <p class="mt-0.5 truncate text-sm font-semibold tabular-nums text-[#7b56d8] dark:text-[#b49af1]">
              ${{ formatCost(row.totalActualCost) }}
            </p>
          </div>
        </div>

        <dl class="mt-3 space-y-1.5 text-xs">
          <div class="flex items-center justify-between gap-4">
            <dt class="text-[#7f8b88] dark:text-[#81938f]">{{ t('dashboard.todayCost') }}</dt>
            <dd class="font-medium tabular-nums text-[#3c4845] dark:text-[#cbd6d3]">
              ${{ formatCost(row.todayActualCost) }}
            </dd>
          </div>
          <div class="flex items-center justify-between gap-4">
            <dt class="text-[#7f8b88] dark:text-[#81938f]">{{ t('dashboard.requests') }}</dt>
            <dd class="font-medium tabular-nums text-[#3c4845] dark:text-[#cbd6d3]">
              {{ formatCount(row.totalRequests) }}
            </dd>
          </div>
          <div class="flex items-center justify-between gap-4">
            <dt class="text-[#7f8b88] dark:text-[#81938f]">{{ t('dashboard.tokens') }}</dt>
            <dd class="font-medium tabular-nums text-[#3c4845] dark:text-[#cbd6d3]">
              {{ formatTokens(row.totalTokens) }}
            </dd>
          </div>
        </dl>
      </article>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { PlatformDashboardStats } from '@/api/usage'

type PlatformKey = 'anthropic' | 'openai' | 'gemini' | 'antigravity' | 'grok'

interface PlatformConfig {
  key: PlatformKey
  label: string
  tone: string
}

interface PlatformTotals {
  totalRequests: number
  totalTokens: number
  totalActualCost: number
  todayActualCost: number
}

const props = defineProps<{
  platforms?: PlatformDashboardStats[] | null
}>()

const { t } = useI18n()
const headingId = 'dashboard-platform-breakdown-title'

const PLATFORM_CONFIG: PlatformConfig[] = [
  { key: 'anthropic', label: 'Claude', tone: 'mark-claude' },
  { key: 'openai', label: 'OpenAI', tone: 'mark-openai' },
  { key: 'gemini', label: 'Gemini', tone: 'mark-gemini' },
  { key: 'antigravity', label: 'Antigravity', tone: 'mark-antigravity' },
  { key: 'grok', label: 'grok', tone: 'mark-grok' }
]

const PLATFORM_ALIASES: Record<string, PlatformKey> = {
  anthropic: 'anthropic',
  claude: 'anthropic',
  openai: 'openai',
  gemini: 'gemini',
  antigravity: 'antigravity',
  grok: 'grok',
  xai: 'grok'
}

const platformRows = computed(() => {
  const totals = new Map<PlatformKey, PlatformTotals>(
    PLATFORM_CONFIG.map(({ key }) => [key, emptyTotals()])
  )

  for (const platform of props.platforms || []) {
    const key = PLATFORM_ALIASES[platform.platform?.trim().toLowerCase()]
    if (!key) continue

    const current = totals.get(key) || emptyTotals()
    current.totalRequests += finiteNumber(platform.total_requests)
    current.totalTokens += finiteNumber(platform.total_tokens)
    current.totalActualCost += finiteNumber(platform.total_actual_cost)
    current.todayActualCost += finiteNumber(platform.today_actual_cost)
    totals.set(key, current)
  }

  return PLATFORM_CONFIG.map(config => ({
    ...config,
    ...(totals.get(config.key) || emptyTotals())
  }))
})

function emptyTotals(): PlatformTotals {
  return {
    totalRequests: 0,
    totalTokens: 0,
    totalActualCost: 0,
    todayActualCost: 0
  }
}

function finiteNumber(value: number | null | undefined) {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function formatCost(value: number) {
  return value.toFixed(4)
}

function formatCount(value: number) {
  return Math.round(value).toLocaleString()
}

function formatTokens(value: number) {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return Math.round(value).toLocaleString()
}
</script>

<style scoped>
.platform-breakdown {
  border-radius: 8px;
  box-shadow: 0 1px 2px rgb(18 31 28 / 4%);
}

.platform-card {
  border-radius: 6px;
  transition: border-color 150ms ease, background-color 150ms ease, transform 150ms ease;
}

.platform-card:hover {
  border-color: #cbd6d2;
  background: #fff;
  transform: translateY(-1px);
}

.dark .platform-card:hover {
  border-color: #3b4b50;
  background: #142024;
}

.platform-mark {
  width: 7px;
  height: 7px;
  flex: 0 0 7px;
  border-radius: 999px;
}

.mark-claude { background: #c56e55; }
.mark-openai { background: #2f9b83; }
.mark-gemini { background: #4f79c7; }
.mark-antigravity { background: #7962b7; }
.mark-grok { background: #586865; }

@media (prefers-reduced-motion: reduce) {
  .platform-card {
    transition: none;
  }

  .platform-card:hover {
    transform: none;
  }
}
</style>
