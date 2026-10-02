<template>
  <section class="usage-panel overflow-hidden border border-[#dfe5e3] bg-white dark:border-[#273439] dark:bg-[#121c20]">
    <div class="flex items-center justify-between gap-4 border-b border-[#e7ecea] px-5 py-4 dark:border-[#28353a]">
      <div>
        <h2 class="text-sm font-semibold text-[#1b2523] dark:text-[#eff5f3]">{{ t('dashboard.recentUsage') }}</h2>
        <p class="mt-1 text-xs text-[#7d8986] dark:text-[#80918e]">{{ t('dashboard.recentUsageDescription') }}</p>
      </div>
      <router-link to="/usage" class="inline-flex flex-none items-center gap-1.5 text-xs font-medium text-[#228b75] hover:text-[#176b5b] dark:text-[#63cdb6]">
        {{ t('dashboard.viewAllUsage') }}
        <Icon name="arrowRight" size="xs" />
      </router-link>
    </div>

    <div v-if="loading" class="divide-y divide-[#edf0ef] px-5 dark:divide-[#263337]">
      <div v-for="index in 5" :key="index" class="grid grid-cols-[1.1fr_1.7fr_.8fr_.8fr] gap-5 py-4">
        <span class="skeleton-line w-3/4"></span>
        <span class="skeleton-line w-4/5"></span>
        <span class="skeleton-line w-2/3"></span>
        <span class="skeleton-line ml-auto w-3/4"></span>
      </div>
    </div>

    <div v-else-if="data.length === 0" class="py-4">
      <EmptyState :title="t('dashboard.noUsageRecords')" :description="t('dashboard.startUsingApi')" />
    </div>

    <template v-else>
      <div class="hidden overflow-x-auto md:block">
        <table class="w-full min-w-[760px] table-fixed">
          <thead>
            <tr class="border-b border-[#edf0ef] bg-[#fafcfb] text-left text-[11px] font-medium text-[#7c8986] dark:border-[#263337] dark:bg-[#10191c] dark:text-[#82928f]">
              <th class="w-[18%] px-5 py-3">{{ t('dashboard.time') }}</th>
              <th class="w-[29%] px-4 py-3">{{ t('dashboard.model') }}</th>
              <th class="w-[13%] px-4 py-3">{{ t('dashboard.requestMode') }}</th>
              <th class="w-[14%] px-4 py-3 text-right">{{ t('dashboard.tokens') }}</th>
              <th class="w-[12%] px-4 py-3 text-right">{{ t('dashboard.duration') }}</th>
              <th class="w-[14%] px-5 py-3 text-right">{{ t('dashboard.actual') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-[#edf0ef] dark:divide-[#263337]">
            <tr v-for="log in data" :key="log.id" class="text-xs transition-colors hover:bg-[#fafcfb] dark:hover:bg-[#162125]">
              <td class="whitespace-nowrap px-5 py-3.5 text-[#71807c] dark:text-[#899995]">{{ formatDateTime(log.created_at) }}</td>
              <td class="px-4 py-3.5">
                <p class="truncate font-medium text-[#2d3936] dark:text-[#dce5e2]" :title="log.model">{{ log.model }}</p>
                <p v-if="log.inbound_endpoint" class="mt-0.5 truncate text-[10px] text-[#929d9a]" :title="log.inbound_endpoint">{{ log.inbound_endpoint }}</p>
              </td>
              <td class="px-4 py-3.5">
                <span class="request-badge" :class="log.stream ? 'request-stream' : 'request-sync'">
                  {{ log.stream ? t('dashboard.streamRequest') : t('dashboard.syncRequest') }}
                </span>
              </td>
              <td class="px-4 py-3.5 text-right font-medium text-[#4c5a57] dark:text-[#aab7b4]">{{ formatTokens(log.input_tokens + log.output_tokens) }}</td>
              <td class="px-4 py-3.5 text-right text-[#71807c] dark:text-[#899995]">{{ formatDuration(log.duration_ms) }}</td>
              <td class="px-5 py-3.5 text-right font-mono font-semibold text-[#2a806d] dark:text-[#64cbb5]">${{ formatCost(log.actual_cost) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="divide-y divide-[#edf0ef] md:hidden dark:divide-[#263337]">
        <div v-for="log in data" :key="log.id" class="px-4 py-4">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <p class="truncate text-sm font-medium text-[#2d3936] dark:text-[#dce5e2]">{{ log.model }}</p>
              <p class="mt-1 text-[11px] text-[#899592]">{{ formatDateTime(log.created_at) }}</p>
            </div>
            <span class="font-mono text-xs font-semibold text-[#2a806d] dark:text-[#64cbb5]">${{ formatCost(log.actual_cost) }}</span>
          </div>
          <div class="mt-3 flex items-center justify-between gap-3 text-[11px] text-[#71807c] dark:text-[#899995]">
            <span class="request-badge" :class="log.stream ? 'request-stream' : 'request-sync'">{{ log.stream ? t('dashboard.streamRequest') : t('dashboard.syncRequest') }}</span>
            <span>{{ formatTokens(log.input_tokens + log.output_tokens) }} Token</span>
            <span>{{ formatDuration(log.duration_ms) }}</span>
          </div>
        </div>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import EmptyState from '@/components/common/EmptyState.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatDateTime } from '@/utils/format'
import type { UsageLog } from '@/types'

defineProps<{
  data: UsageLog[]
  loading: boolean
}>()

const { t } = useI18n()
const formatCost = (value: number) => value.toFixed(4)

function formatTokens(value: number) {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`
  return value.toLocaleString()
}

function formatDuration(value: number | null) {
  if (value == null) return '-'
  return value >= 1000 ? `${(value / 1000).toFixed(2)}s` : `${value.toFixed(0)}ms`
}
</script>

<style scoped>
.usage-panel {
  border-radius: 8px;
  box-shadow: 0 1px 2px rgb(18 31 28 / 4%);
}

.skeleton-line {
  display: block;
  height: 12px;
  border-radius: 4px;
  background: linear-gradient(90deg, #edf1f0 25%, #f6f8f7 50%, #edf1f0 75%);
  background-size: 200% 100%;
  animation: dashboard-shimmer 1.4s linear infinite;
}

.dark .skeleton-line {
  background: linear-gradient(90deg, #1e2b2f 25%, #29383c 50%, #1e2b2f 75%);
  background-size: 200% 100%;
}

.request-badge {
  display: inline-flex;
  align-items: center;
  border-radius: 4px;
  padding: 0.2rem 0.4rem;
  font-size: 10px;
  font-weight: 600;
}

.request-stream {
  background: #e9f7f3;
  color: #247e6b;
}

.request-sync {
  background: #eef2f6;
  color: #62717f;
}

.dark .request-stream { background: #18352f; color: #73d4be; }
.dark .request-sync { background: #243138; color: #a1b2ba; }

@keyframes dashboard-shimmer {
  from { background-position: 200% 0; }
  to { background-position: -200% 0; }
}

@media (prefers-reduced-motion: reduce) {
  .skeleton-line {
    animation: none;
  }
}
</style>
