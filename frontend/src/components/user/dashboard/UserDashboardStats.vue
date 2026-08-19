<template>
  <section class="user-metric-panel overflow-hidden border border-[#dfe5e3] bg-white dark:border-[#273439] dark:bg-[#121c20]">
    <div class="grid grid-cols-1 divide-y divide-[#e7ecea] sm:grid-cols-2 sm:divide-x sm:divide-y-0 xl:grid-cols-4 dark:divide-[#28353a]">
      <article v-for="metric in primaryMetrics" :key="metric.label" class="min-w-0 px-5 py-5 lg:px-6">
        <div class="flex items-start justify-between gap-4">
          <div class="min-w-0">
            <p class="text-xs font-medium text-[#697674] dark:text-[#8fa09d]">{{ metric.label }}</p>
            <p class="mt-2 truncate text-2xl font-semibold text-[#17211f] dark:text-[#f1f6f4]" :title="metric.value">
              {{ metric.value }}
            </p>
            <p class="mt-1 truncate text-xs text-[#8a9693] dark:text-[#758783]" :title="metric.detail">
              {{ metric.detail }}
            </p>
          </div>
          <span class="metric-icon" :class="metric.tone">
            <Icon :name="metric.icon" size="sm" :stroke-width="1.8" />
          </span>
        </div>
      </article>
    </div>

    <div class="grid grid-cols-2 border-t border-[#e7ecea] bg-[#fafcfb] lg:grid-cols-4 dark:border-[#28353a] dark:bg-[#10191c]">
      <div v-for="metric in secondaryMetrics" :key="metric.label" class="min-w-0 border-r border-[#e7ecea] px-5 py-3 last:border-r-0 dark:border-[#28353a]">
        <div class="flex items-center gap-2 text-xs text-[#778481] dark:text-[#82928f]">
          <Icon :name="metric.icon" size="xs" :stroke-width="1.8" />
          <span class="truncate">{{ metric.label }}</span>
        </div>
        <p class="mt-1 truncate text-sm font-semibold text-[#26312f] dark:text-[#dce5e2]" :title="metric.value">
          {{ metric.value }}
        </p>
        <p v-if="metric.detail" class="mt-0.5 truncate text-xs text-[#8a9693] dark:text-[#758783]" :title="metric.detail">
          {{ metric.detail }}
        </p>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { UserDashboardStats as UserStatsType } from '@/api/usage'

const props = defineProps<{
  stats: UserStatsType
  balance: number
  isSimple: boolean
}>()

const { t } = useI18n()

const primaryMetrics = computed(() => {
  const metrics = [
    {
      label: t('dashboard.balance'),
      value: `$${formatBalance(props.balance)}`,
      detail: t('dashboard.availableBalanceHint'),
      icon: 'dollar' as const,
      tone: 'tone-emerald'
    },
    {
      label: t('dashboard.apiKeys'),
      value: formatNumber(props.stats.total_api_keys || 0),
      detail: t('dashboard.activeKeysHint', { count: props.stats.active_api_keys || 0 }),
      icon: 'key' as const,
      tone: 'tone-blue'
    },
    {
      label: t('dashboard.todayRequests'),
      value: formatNumber(props.stats.today_requests || 0),
      detail: t('dashboard.totalRequestsHint', { count: formatNumber(props.stats.total_requests || 0) }),
      icon: 'chartBar' as const,
      tone: 'tone-coral'
    },
    {
      label: t('dashboard.todayCost'),
      value: `$${formatCost(props.stats.today_actual_cost || 0)}`,
      detail: t('dashboard.standardCostHint', { amount: formatCost(props.stats.today_cost || 0) }),
      icon: 'creditCard' as const,
      tone: 'tone-violet'
    }
  ]

  return props.isSimple ? metrics.slice(1) : metrics
})

const secondaryMetrics = computed(() => [
  {
    label: t('dashboard.todayTokens'),
    value: formatTokens(props.stats.today_tokens || 0),
    detail: `${t('dashboard.input')}: ${formatTokens(props.stats.today_input_tokens || 0)} / ${t('dashboard.output')}: ${formatTokens(props.stats.today_output_tokens || 0)} / ${t('dashboard.cache')}: ${formatTokens((props.stats.today_cache_creation_tokens || 0) + (props.stats.today_cache_read_tokens || 0))}`,
    icon: 'cube' as const
  },
  {
    label: t('dashboard.totalTokens'),
    value: formatTokens(props.stats.total_tokens || 0),
    detail: `${t('dashboard.input')}: ${formatTokens(props.stats.total_input_tokens || 0)} / ${t('dashboard.output')}: ${formatTokens(props.stats.total_output_tokens || 0)} / ${t('dashboard.cache')}: ${formatTokens((props.stats.total_cache_creation_tokens || 0) + (props.stats.total_cache_read_tokens || 0))}`,
    icon: 'database' as const
  },
  {
    label: t('dashboard.performance'),
    value: `${formatTokens(props.stats.rpm || 0)} RPM / ${formatTokens(props.stats.tpm || 0)} TPM`,
    detail: '',
    icon: 'bolt' as const
  },
  {
    label: t('dashboard.avgResponse'),
    value: formatDuration(props.stats.average_duration_ms || 0),
    detail: '',
    icon: 'clock' as const
  }
])

const formatBalance = (value: number) => new Intl.NumberFormat('en-US', {
  minimumFractionDigits: 2,
  maximumFractionDigits: 2
}).format(Number.isFinite(value) ? value : 0)

const formatNumber = (value: number) => value.toLocaleString()
const formatCost = (value: number) => value.toFixed(4)

function formatTokens(value: number) {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return value.toLocaleString()
}

function formatDuration(ms: number) {
  return ms >= 1000 ? `${(ms / 1000).toFixed(2)}s` : `${ms.toFixed(0)}ms`
}
</script>

<style scoped>
.user-metric-panel {
  border-radius: 8px;
  box-shadow: 0 1px 2px rgb(18 31 28 / 4%);
}

.metric-icon {
  display: inline-flex;
  width: 34px;
  height: 34px;
  flex: 0 0 34px;
  align-items: center;
  justify-content: center;
  border-radius: 7px;
}

.tone-emerald { background: #e8f8f3; color: #13866f; }
.tone-blue { background: #edf3ff; color: #3569c8; }
.tone-coral { background: #fff0ec; color: #c85d46; }
.tone-violet { background: #f3efff; color: #7157bd; }

.dark .tone-emerald { background: #16352f; color: #65d6bd; }
.dark .tone-blue { background: #1d2d45; color: #8eb2f2; }
.dark .tone-coral { background: #3a2723; color: #ee9985; }
.dark .tone-violet { background: #2c2740; color: #b9a4f4; }

@media (max-width: 639px) {
  .user-metric-panel > div:first-child > article:last-child {
    border-bottom: 0;
  }
}
</style>
