<template>
  <AppLayout>
    <div class="mx-auto max-w-[1560px] space-y-6">
      <header class="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <p class="text-[11px] font-semibold uppercase text-[#238b76] dark:text-[#63cdb6]">{{ t('dashboard.developerWorkspace') }}</p>
          <h1 class="mt-1.5 text-2xl font-semibold text-[#17211f] dark:text-[#f0f5f3]">
            {{ t('dashboard.greeting', { name: displayName }) }}
          </h1>
          <p class="mt-1.5 text-sm text-[#71807c] dark:text-[#899995]">{{ t('dashboard.workspaceSummary') }}</p>
        </div>
        <router-link to="/keys" class="create-key-button">
          <Icon name="plus" size="sm" :stroke-width="2" />
          <span>{{ t('dashboard.createApiKey') }}</span>
        </router-link>
      </header>

      <div v-if="initialLoading" class="space-y-6" aria-live="polite" :aria-label="t('common.loading')">
        <div class="dashboard-skeleton h-[176px]"></div>
        <div class="grid grid-cols-1 gap-6 xl:grid-cols-[minmax(0,1.75fr)_minmax(280px,.75fr)]">
          <div class="dashboard-skeleton h-[390px]"></div>
          <div class="dashboard-skeleton h-[390px]"></div>
        </div>
        <div class="dashboard-skeleton h-[260px]"></div>
      </div>

      <div v-else-if="errorMessage && !stats" class="error-panel border border-[#ead3ce] bg-white px-6 py-10 text-center dark:border-[#523832] dark:bg-[#121c20]">
        <span class="mx-auto flex h-10 w-10 items-center justify-center rounded-md bg-[#fff0ec] text-[#bf5846] dark:bg-[#3a2723] dark:text-[#ed9a87]">
          <Icon name="exclamationTriangle" size="md" />
        </span>
        <h2 class="mt-4 text-sm font-semibold text-[#293532] dark:text-[#e0e8e5]">{{ t('dashboard.loadFailed') }}</h2>
        <p class="mt-1 text-xs text-[#7b8884] dark:text-[#849490]">{{ errorMessage }}</p>
        <button type="button" class="retry-button mt-5" @click="refreshAll">
          <Icon name="refresh" size="xs" />
          {{ t('dashboard.retry') }}
        </button>
      </div>

      <template v-else-if="stats">
        <div v-if="errorMessage" class="flex items-center justify-between gap-4 rounded-md border border-[#ead3ce] bg-[#fff8f6] px-4 py-3 text-xs text-[#9b4f42] dark:border-[#523832] dark:bg-[#2a201e] dark:text-[#e9a494]">
          <span>{{ errorMessage }}</span>
          <button type="button" class="font-semibold" @click="refreshAll">{{ t('dashboard.retry') }}</button>
        </div>

        <UserDashboardStats
          :stats="stats"
          :balance="user?.balance || 0"
          :is-simple="authStore.isSimpleMode"
        />

        <UserDashboardPlatformBreakdown :platforms="stats.by_platform" />

        <div
          class="grid grid-cols-1 gap-6"
          :class="authStore.isSimpleMode ? '' : 'xl:grid-cols-[minmax(0,1.75fr)_minmax(280px,.75fr)]'"
        >
          <UserDashboardCharts
            v-model:startDate="startDate"
            v-model:endDate="endDate"
            v-model:granularity="granularity"
            :loading="loadingCharts || refreshing"
            :trend="trendData"
            :models="modelStats"
            @dateRangeChange="loadChartData"
            @granularityChange="loadChartData"
            @refresh="refreshAll"
          />
          <UserDashboardQuotaPanel
            v-if="!authStore.isSimpleMode"
            :stats="stats"
            :platform-quotas="platformQuotas"
          />
        </div>

        <UserDashboardRecentUsage :data="recentUsage" :loading="loadingUsage || refreshing" />
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { usageAPI, type UserDashboardStats as UserStatsType } from '@/api/usage'
import { getMyPlatformQuotas } from '@/api/user'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import UserDashboardStats from '@/components/user/dashboard/UserDashboardStats.vue'
import UserDashboardPlatformBreakdown from '@/components/user/dashboard/UserDashboardPlatformBreakdown.vue'
import UserDashboardCharts from '@/components/user/dashboard/UserDashboardCharts.vue'
import UserDashboardQuotaPanel from '@/components/user/dashboard/UserDashboardQuotaPanel.vue'
import UserDashboardRecentUsage from '@/components/user/dashboard/UserDashboardRecentUsage.vue'
import type { UsageLog, TrendDataPoint, ModelStat, PlatformQuotaItem } from '@/types'
import { formatDateLocalInput } from '@/utils/format'

const { t } = useI18n()
const authStore = useAuthStore()
const user = computed(() => authStore.user)
const displayName = computed(() => user.value?.username || user.value?.email?.split('@')[0] || t('nav.profile'))

const stats = ref<UserStatsType | null>(null)
const trendData = ref<TrendDataPoint[]>([])
const modelStats = ref<ModelStat[]>([])
const recentUsage = ref<UsageLog[]>([])
const platformQuotas = ref<PlatformQuotaItem[] | null>(null)
const initialLoading = ref(true)
const refreshing = ref(false)
const loadingUsage = ref(false)
const loadingCharts = ref(false)
const errorMessage = ref('')

const startDate = ref(formatDateLocalInput(new Date(Date.now() - 6 * 86400000)))
const endDate = ref(formatDateLocalInput(new Date()))
const granularity = ref<'day' | 'hour'>('day')

async function loadStats() {
  await authStore.refreshUser()
  stats.value = await usageAPI.getDashboardStats()
}

async function loadChartData() {
  loadingCharts.value = true
  try {
    const [trendResponse, modelResponse] = await Promise.all([
      usageAPI.getDashboardTrend({
        start_date: startDate.value,
        end_date: endDate.value,
        granularity: granularity.value
      }),
      usageAPI.getDashboardModels({
        start_date: startDate.value,
        end_date: endDate.value
      })
    ])
    trendData.value = trendResponse.trend || []
    modelStats.value = modelResponse.models || []
  } catch (error) {
    console.error('Failed to load dashboard charts:', error)
  } finally {
    loadingCharts.value = false
  }
}

async function loadRecent() {
  loadingUsage.value = true
  try {
    const response = await usageAPI.getByDateRange(startDate.value, endDate.value)
    recentUsage.value = response.items.slice(0, 6)
  } catch (error) {
    console.error('Failed to load recent usage:', error)
  } finally {
    loadingUsage.value = false
  }
}

async function loadPlatformQuotas() {
  try {
    const data = await getMyPlatformQuotas()
    platformQuotas.value = data.platform_quotas ?? []
  } catch (error) {
    console.warn('Failed to load platform quotas:', error)
    platformQuotas.value = []
  }
}

async function refreshAll() {
  errorMessage.value = ''
  refreshing.value = true
  const results = await Promise.allSettled([
    loadStats(),
    loadChartData(),
    loadRecent(),
    loadPlatformQuotas()
  ])
  if (results[0].status === 'rejected') {
    console.error('Failed to load dashboard stats:', results[0].reason)
    errorMessage.value = t('dashboard.loadFailedDescription')
  }
  refreshing.value = false
  initialLoading.value = false
}

onMounted(() => {
  void refreshAll()
})
</script>

<style scoped>
.create-key-button,
.retry-button {
  display: inline-flex;
  min-height: 38px;
  align-items: center;
  justify-content: center;
  gap: 0.45rem;
  border-radius: 6px;
  padding: 0.55rem 0.9rem;
  background: #177d69;
  color: #fff;
  font-size: 0.75rem;
  font-weight: 600;
  transition: background-color 150ms ease;
}

.create-key-button:hover,
.retry-button:hover {
  background: #126554;
}

.dashboard-skeleton {
  border: 1px solid #e2e7e5;
  border-radius: 8px;
  background: linear-gradient(90deg, #edf1f0 25%, #f7f9f8 50%, #edf1f0 75%);
  background-size: 200% 100%;
  animation: dashboard-loading 1.4s linear infinite;
}

.dark .dashboard-skeleton {
  border-color: #263438;
  background: linear-gradient(90deg, #172226 25%, #213034 50%, #172226 75%);
  background-size: 200% 100%;
}

.error-panel {
  border-radius: 8px;
}

@keyframes dashboard-loading {
  from { background-position: 200% 0; }
  to { background-position: -200% 0; }
}

@media (prefers-reduced-motion: reduce) {
  .create-key-button,
  .retry-button {
    transition: none;
  }

  .dashboard-skeleton {
    animation: none;
  }
}
</style>
