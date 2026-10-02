<template>
  <section class="quota-panel border border-[#dfe5e3] bg-white dark:border-[#273439] dark:bg-[#121c20]">
    <div class="border-b border-[#e7ecea] px-5 py-4 dark:border-[#28353a]">
      <div class="flex items-center justify-between gap-3">
        <div>
          <h2 class="text-sm font-semibold text-[#1b2523] dark:text-[#eff5f3]">{{ t('dashboard.platformQuotaOverview') }}</h2>
          <p class="mt-1 text-xs text-[#7d8986] dark:text-[#80918e]">{{ t('dashboard.platformQuotaDescription') }}</p>
        </div>
        <span class="flex h-8 w-8 flex-none items-center justify-center rounded-md bg-[#edf7f4] text-[#238b76] dark:bg-[#17322d] dark:text-[#62d1b8]">
          <Icon name="server" size="sm" :stroke-width="1.8" />
        </span>
      </div>
    </div>

    <div v-if="platformRows.length" class="divide-y divide-[#edf0ef] dark:divide-[#263337]">
      <div v-for="row in platformRows" :key="row.platform" class="px-5 py-4">
        <div class="flex items-center justify-between gap-3">
          <div class="flex min-w-0 items-center gap-2">
            <span class="platform-dot" :class="platformTone(row.platform)"></span>
            <span class="truncate text-xs font-semibold text-[#35413f] dark:text-[#dce5e2]">{{ platformLabel(row.platform) }}</span>
          </div>
          <span class="flex-none text-[11px] font-medium text-[#6f7d79] dark:text-[#899995]">
            ${{ formatUsd(row.todayCost) }} {{ t('dashboard.todayShort') }}
          </span>
        </div>

        <template v-if="row.window">
          <div class="mt-3 flex items-center justify-between gap-2 text-[11px]">
            <span class="text-[#82908c] dark:text-[#7f918d]">{{ row.window.label }}</span>
            <span v-if="row.window.limit === 0" class="font-medium text-[#c55243]">{{ t('dashboard.platformQuota.disabled') }}</span>
            <span v-else class="font-medium text-[#53615e] dark:text-[#aab7b4]">
              ${{ formatUsd(row.window.usage) }} / ${{ formatUsd(row.window.limit) }}
            </span>
          </div>
          <div class="mt-2 h-1.5 overflow-hidden rounded-full bg-[#edf1f0] dark:bg-[#263438]">
            <div
              class="h-full rounded-full"
              :class="progressTone(row.window.percent)"
              :style="{ width: `${row.window.limit === 0 ? 100 : row.window.percent}%` }"
            ></div>
          </div>
          <p v-if="row.window.resetsAt" class="mt-1.5 truncate text-[10px] text-[#99a39f]" :title="row.window.resetsAt">
            {{ t('dashboard.platformQuota.resetsAt', { time: formatResetTime(row.window.resetsAt) }) }}
          </p>
        </template>
        <div v-else class="mt-3 flex items-center justify-between text-[11px]">
          <span class="text-[#899592]">{{ t('dashboard.platformQuota.title') }}</span>
          <span class="font-medium text-[#3d8a77] dark:text-[#66cbb4]">{{ t('dashboard.platformQuota.noLimit') }}</span>
        </div>
      </div>
    </div>

    <div v-else class="flex min-h-[238px] flex-col items-center justify-center px-5 text-center">
      <Icon name="server" size="lg" class="text-[#bdc7c4] dark:text-[#536561]" />
      <p class="mt-3 text-sm font-medium text-[#66736f] dark:text-[#a5b2af]">{{ t('dashboard.platformBreakdownEmpty') }}</p>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { UserDashboardStats as UserStatsType } from '@/api/usage'
import type { PlatformQuotaItem } from '@/types'

interface QuotaWindowDisplay {
  label: string
  usage: number
  limit: number
  percent: number
  resetsAt?: string | null
}

const props = defineProps<{
  stats: UserStatsType
  platformQuotas?: PlatformQuotaItem[] | null
}>()

const { t } = useI18n()

const platformRows = computed(() => {
  const statMap = new Map((props.stats.by_platform || []).map(item => [item.platform, item]))
  const quotaMap = new Map<string, PlatformQuotaItem>((props.platformQuotas || []).map(item => [item.platform, item]))
  const platforms = Array.from(new Set([...statMap.keys(), ...quotaMap.keys()]))

  return platforms
    .map(platform => {
      const stat = statMap.get(platform)
      const quota = quotaMap.get(platform)
      return {
        platform,
        todayCost: stat?.today_actual_cost || 0,
        totalCost: stat?.total_actual_cost || 0,
        window: getPrimaryWindow(quota)
      }
    })
    .sort((a, b) => b.totalCost - a.totalCost)
})

function getPrimaryWindow(quota?: PlatformQuotaItem): QuotaWindowDisplay | null {
  if (!quota) return null
  const windows = [
    {
      label: t('dashboard.platformQuota.daily'),
      limit: quota.daily_limit_usd,
      usage: quota.daily_usage_usd,
      resetsAt: quota.daily_window_resets_at
    },
    {
      label: t('dashboard.platformQuota.weekly'),
      limit: quota.weekly_limit_usd,
      usage: quota.weekly_usage_usd,
      resetsAt: quota.weekly_window_resets_at
    },
    {
      label: t('dashboard.platformQuota.monthly'),
      limit: quota.monthly_limit_usd,
      usage: quota.monthly_usage_usd,
      resetsAt: quota.monthly_window_resets_at
    }
  ]
  const active = windows.find(window => window.limit != null)
  if (!active || active.limit == null) return null
  return {
    label: active.label,
    limit: active.limit,
    usage: active.usage || 0,
    percent: active.limit > 0 ? Math.min(100, Math.max(0, (active.usage / active.limit) * 100)) : 100,
    resetsAt: active.resetsAt
  }
}

const PLATFORM_LABELS: Record<string, string> = {
  anthropic: 'Claude',
  openai: 'OpenAI',
  gemini: 'Gemini',
  antigravity: 'Antigravity',
  grok: 'Grok'
}

const platformLabel = (platform: string) => PLATFORM_LABELS[platform] || platform

function platformTone(platform: string) {
  const tones: Record<string, string> = {
    anthropic: 'dot-coral',
    openai: 'dot-emerald',
    gemini: 'dot-blue',
    antigravity: 'dot-violet',
    grok: 'dot-gray'
  }
  return tones[platform] || 'dot-gray'
}

function progressTone(percent: number) {
  if (percent >= 95) return 'bg-[#c65345]'
  if (percent >= 75) return 'bg-[#c58739]'
  return 'bg-[#2f9b83]'
}

function formatUsd(value: number) {
  return new Intl.NumberFormat('en-US', {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2
  }).format(Number.isFinite(value) ? value : 0)
}

function formatResetTime(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString(undefined, {
    month: 'numeric',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false
  })
}
</script>

<style scoped>
.quota-panel {
  border-radius: 8px;
  box-shadow: 0 1px 2px rgb(18 31 28 / 4%);
}

.platform-dot {
  width: 7px;
  height: 7px;
  flex: 0 0 7px;
  border-radius: 999px;
}

.dot-emerald { background: #2f9b83; }
.dot-blue { background: #4f79c7; }
.dot-coral { background: #c56e55; }
.dot-violet { background: #7962b7; }
.dot-gray { background: #8b9895; }
</style>
