<template>
  <section class="dashboard-panel relative min-w-0 border border-[#dfe5e3] bg-white dark:border-[#273439] dark:bg-[#121c20]">
    <div class="flex flex-col gap-4 border-b border-[#e7ecea] px-5 py-4 lg:flex-row lg:items-center lg:justify-between dark:border-[#28353a]">
      <div>
        <h2 class="text-sm font-semibold text-[#1b2523] dark:text-[#eff5f3]">{{ t('dashboard.usageAnalysis') }}</h2>
        <p class="mt-1 text-xs text-[#7d8986] dark:text-[#80918e]">{{ t('dashboard.usageAnalysisDescription') }}</p>
      </div>

      <div class="flex flex-wrap items-center gap-2">
        <DateRangePicker
          :start-date="startDate"
          :end-date="endDate"
          @update:startDate="$emit('update:startDate', $event)"
          @update:endDate="$emit('update:endDate', $event)"
          @change="$emit('dateRangeChange', $event)"
        />
        <div class="w-24">
          <Select
            :model-value="granularity"
            :options="granularityOptions"
            @update:model-value="$emit('update:granularity', $event)"
            @change="$emit('granularityChange')"
          />
        </div>
        <button
          type="button"
          class="dashboard-icon-button"
          :disabled="loading"
          :title="t('common.refresh')"
          :aria-label="t('common.refresh')"
          @click="$emit('refresh')"
        >
          <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
        </button>
      </div>
    </div>

    <div class="grid min-h-[318px] grid-cols-1 xl:grid-cols-[minmax(0,1fr)_230px]">
      <div class="relative min-w-0 px-4 py-5 sm:px-5">
        <div v-if="loading" class="absolute inset-0 z-10 flex items-center justify-center bg-white/75 dark:bg-[#121c20]/75">
          <LoadingSpinner size="md" />
        </div>
        <div v-if="chartData" class="h-[270px]">
          <Line :data="chartData" :options="lineOptions" />
        </div>
        <div v-else class="flex h-[270px] flex-col items-center justify-center text-center">
          <span class="mb-3 flex h-10 w-10 items-center justify-center rounded-md bg-[#f0f4f3] text-[#91a09d] dark:bg-[#1b292d] dark:text-[#71837f]">
            <Icon name="chart" size="md" />
          </span>
          <p class="text-sm font-medium text-[#52605d] dark:text-[#aab7b4]">{{ t('dashboard.noDataAvailable') }}</p>
        </div>
      </div>

      <aside class="border-t border-[#e7ecea] px-5 py-5 xl:border-l xl:border-t-0 dark:border-[#28353a]">
        <div class="mb-4 flex items-center justify-between gap-3">
          <h3 class="text-xs font-semibold text-[#35413f] dark:text-[#cbd5d2]">{{ t('dashboard.topModels') }}</h3>
          <span class="text-[11px] text-[#8c9895]">{{ models.length }}</span>
        </div>
        <div v-if="topModels.length" class="space-y-4">
          <div v-for="(model, index) in topModels" :key="model.model" class="min-w-0">
            <div class="flex items-center gap-2">
              <span class="flex h-5 w-5 flex-none items-center justify-center rounded bg-[#eef3f2] text-[10px] font-semibold text-[#697774] dark:bg-[#1c292d] dark:text-[#94a39f]">
                {{ index + 1 }}
              </span>
              <span class="min-w-0 flex-1 truncate text-xs font-medium text-[#35413f] dark:text-[#dce5e2]" :title="model.model">
                {{ model.model }}
              </span>
            </div>
            <div class="mt-1.5 flex items-center justify-between pl-7 text-[11px] text-[#899592] dark:text-[#788985]">
              <span>{{ formatNumber(model.requests) }} {{ t('dashboard.requests') }}</span>
              <span>{{ formatTokens(model.total_tokens) }}</span>
            </div>
          </div>
        </div>
        <p v-else class="py-10 text-center text-xs text-[#8a9693]">{{ t('dashboard.noDataAvailable') }}</p>
      </aside>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Tooltip,
  Legend,
  Filler
} from 'chart.js'
import { Line } from 'vue-chartjs'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import type { TrendDataPoint, ModelStat } from '@/types'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend, Filler)

const props = defineProps<{
  loading: boolean
  startDate: string
  endDate: string
  granularity: string
  trend: TrendDataPoint[]
  models: ModelStat[]
}>()

defineEmits([
  'update:startDate',
  'update:endDate',
  'update:granularity',
  'dateRangeChange',
  'granularityChange',
  'refresh'
])

const { t } = useI18n()
const granularityOptions = computed(() => [
  { value: 'day', label: t('dashboard.day') },
  { value: 'hour', label: t('dashboard.hour') }
])

const topModels = computed(() => [...(props.models || [])]
  .sort((a, b) => b.total_tokens - a.total_tokens)
  .slice(0, 5))

const chartData = computed(() => {
  if (!props.trend?.length) return null
  return {
    labels: props.trend.map(point => point.date),
    datasets: [
      {
        label: t('dashboard.input'),
        data: props.trend.map(point => point.input_tokens),
        borderColor: '#248d78',
        backgroundColor: 'rgba(36, 141, 120, 0.10)',
        borderWidth: 2,
        pointRadius: 0,
        pointHoverRadius: 4,
        fill: true,
        tension: 0.32
      },
      {
        label: t('dashboard.output'),
        data: props.trend.map(point => point.output_tokens),
        borderColor: '#4f79c7',
        backgroundColor: 'transparent',
        borderWidth: 2,
        pointRadius: 0,
        pointHoverRadius: 4,
        fill: false,
        tension: 0.32
      },
      {
        label: t('dashboard.cache'),
        data: props.trend.map(point => point.cache_read_tokens + point.cache_creation_tokens),
        borderColor: '#bd744a',
        backgroundColor: 'transparent',
        borderWidth: 1.5,
        borderDash: [5, 5],
        pointRadius: 0,
        pointHoverRadius: 4,
        fill: false,
        tension: 0.32
      }
    ]
  }
})

const lineOptions = computed(() => {
  const isDark = document.documentElement.classList.contains('dark')
  const textColor = isDark ? '#8fa09d' : '#7d8986'
  const gridColor = isDark ? 'rgba(73, 91, 96, 0.35)' : 'rgba(214, 222, 219, 0.65)'
  return {
    responsive: true,
    maintainAspectRatio: false,
    interaction: { intersect: false, mode: 'index' as const },
    plugins: {
      legend: {
        position: 'top' as const,
        align: 'end' as const,
        labels: {
          color: textColor,
          usePointStyle: true,
          pointStyle: 'circle',
          boxWidth: 6,
          boxHeight: 6,
          padding: 18,
          font: { size: 11 }
        }
      },
      tooltip: {
        callbacks: {
          label: (context: any) => `${context.dataset.label}: ${formatTokens(Number(context.raw))}`
        }
      }
    },
    scales: {
      x: {
        grid: { display: false },
        border: { display: false },
        ticks: { color: textColor, font: { size: 10 }, maxRotation: 0, autoSkip: true }
      },
      y: {
        beginAtZero: true,
        grid: { color: gridColor },
        border: { display: false },
        ticks: {
          color: textColor,
          font: { size: 10 },
          callback: (value: string | number) => formatTokens(Number(value))
        }
      }
    }
  }
})

const formatNumber = (value: number) => value.toLocaleString()

function formatTokens(value: number) {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return value.toLocaleString()
}
</script>

<style scoped>
.dashboard-panel {
  border-radius: 8px;
  box-shadow: 0 1px 2px rgb(18 31 28 / 4%);
}

.dashboard-icon-button {
  display: inline-flex;
  width: 36px;
  height: 36px;
  align-items: center;
  justify-content: center;
  border: 1px solid #dbe2e0;
  border-radius: 6px;
  color: #62706d;
  background: #fff;
  transition: background-color 150ms ease, color 150ms ease;
}

.dashboard-icon-button:hover:not(:disabled) {
  color: #167d69;
  background: #f2f7f5;
}

.dashboard-icon-button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.dark .dashboard-icon-button {
  border-color: #304045;
  color: #97a7a3;
  background: #172327;
}

@media (prefers-reduced-motion: reduce) {
  .dashboard-icon-button {
    transition: none;
  }
}
</style>
