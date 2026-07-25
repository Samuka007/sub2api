<template>
  <div class="space-y-8">
    <header class="flex flex-col gap-4 border-b border-gray-200 pb-5 dark:border-dark-700 sm:flex-row sm:items-start sm:justify-between">
      <div class="min-w-0">
        <h1 class="text-xl font-semibold text-gray-900 dark:text-white">模型雷达</h1>
        <div v-if="radar" class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-gray-500 dark:text-gray-400">
          <a
            :href="radar.source_url"
            target="_blank"
            rel="noopener noreferrer"
            class="font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
          >
            {{ radar.source_name }}
          </a>
          <span>抓取于 {{ formatDate(radar.fetched_at) }}</span>
          <span v-if="radar.stale" class="font-medium text-amber-700 dark:text-amber-400">缓存已过期</span>
        </div>
      </div>

      <button
        type="button"
        class="inline-flex h-9 shrink-0 items-center justify-center gap-2 rounded-md border border-gray-300 bg-white px-3 text-sm font-medium text-gray-700 transition-colors hover:bg-gray-50 disabled:cursor-not-allowed disabled:opacity-50 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200 dark:hover:bg-dark-700"
        :disabled="refreshing || !canRefresh"
        :title="refreshTitle"
        @click="refresh"
      >
        <svg class="h-4 w-4" :class="{ 'animate-spin': refreshing }" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="1.8" aria-hidden="true">
          <path stroke-linecap="round" stroke-linejoin="round" d="M16.023 9.348h4.992V4.356m-1.636 11.272a8.25 8.25 0 01-14.034 2.198M7.977 14.652H2.985v4.992m1.636-11.272a8.25 8.25 0 0114.034-2.198" />
        </svg>
        <span>{{ refreshing ? '刷新中' : '刷新' }}</span>
      </button>
    </header>

    <div v-if="loading" class="py-12 text-sm text-gray-500 dark:text-gray-400">正在读取模型雷达数据...</div>

    <div v-else-if="errorMessage" class="flex flex-wrap items-center gap-3 border-l-2 border-red-500 py-2 pl-3 text-sm text-red-700 dark:text-red-300">
      <span>{{ errorMessage }}</span>
      <button type="button" class="font-medium underline underline-offset-2" @click="load">重试</button>
    </div>

    <template v-else-if="radar">
      <section v-for="section in sections" :key="section.key" class="border-b border-gray-200 pb-8 dark:border-dark-700">
        <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div class="min-w-0">
            <div class="flex flex-wrap items-baseline gap-x-3 gap-y-1">
              <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ section.data.title }}</h2>
              <span v-if="section.data.source_updated_at" class="text-sm text-gray-500 dark:text-gray-400">{{ section.data.source_updated_at }}</span>
            </div>
            <p v-for="paragraph in section.data.summary" :key="paragraph" class="mt-2 max-w-4xl text-sm leading-6 text-gray-600 dark:text-gray-300">
              {{ paragraph }}
            </p>
          </div>

          <div v-if="section.data.highlights?.length" class="flex shrink-0 flex-wrap gap-2">
            <span
              v-for="highlight in section.data.highlights"
              :key="highlight"
              class="rounded-md border border-primary-200 bg-primary-50 px-2 py-1 text-sm font-medium text-primary-700 dark:border-primary-900/70 dark:bg-primary-950/30 dark:text-primary-300"
            >
              {{ highlight }}
            </span>
          </div>
        </div>

        <div v-if="section.kind === 'quality' && section.data.cards?.length" class="mt-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          <article
            v-for="card in section.data.cards"
            :key="card.model"
            class="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3 gap-y-2 rounded-md border border-gray-200 bg-white p-4 dark:border-dark-700 dark:bg-dark-850"
          >
            <h3 class="min-w-0 truncate text-sm font-semibold text-gray-900 dark:text-white" :title="card.model">{{ card.model }}</h3>
            <span class="text-sm font-semibold text-primary-700 dark:text-primary-300">IQ {{ card.score }}</span>
            <dl class="col-span-2 grid grid-cols-2 gap-3 border-t border-gray-100 pt-2 text-sm dark:border-dark-700">
              <div>
                <dt class="text-gray-500 dark:text-gray-400">评测费用</dt>
                <dd class="mt-0.5 font-medium text-gray-800 dark:text-gray-200">{{ card.cost }}</dd>
              </div>
              <div>
                <dt class="text-gray-500 dark:text-gray-400">耗时</dt>
                <dd class="mt-0.5 font-medium text-gray-800 dark:text-gray-200">{{ card.duration }}</dd>
              </div>
            </dl>
          </article>
        </div>

        <div v-else-if="section.data.table" class="mt-5 overflow-x-auto rounded-md border border-gray-200 dark:border-dark-700">
          <table class="min-w-full border-collapse text-left text-sm">
            <thead v-if="section.data.table.headers.length" class="bg-gray-50 text-gray-600 dark:bg-dark-800 dark:text-gray-300">
              <tr>
                <th v-for="header in section.data.table.headers" :key="header" scope="col" class="whitespace-nowrap px-4 py-3 font-medium">{{ header }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="(row, rowIndex) in section.data.table.rows" :key="rowIndex" class="text-gray-700 dark:text-gray-200">
                <td v-for="(cell, cellIndex) in row" :key="cellIndex" class="whitespace-nowrap px-4 py-3">{{ cell }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { getModelRadar, refreshModelRadar, type ModelRadarSection, type ModelRadarView } from '@/api/modelRadar'

interface RadarSectionEntry {
  key: 'quota' | 'fast' | 'quality'
  kind: 'table' | 'quality'
  data: ModelRadarSection
}

const radar = ref<ModelRadarView | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const errorMessage = ref('')

const sections = computed<RadarSectionEntry[]>(() => {
  if (!radar.value) return []
  return [
    { key: 'quota', kind: 'table', data: radar.value.quota },
    { key: 'fast', kind: 'table', data: radar.value.fast },
    { key: 'quality', kind: 'quality', data: radar.value.quality }
  ]
})

const canRefresh = computed(() => {
  if (!radar.value?.refresh_allowed_at) return true
  return new Date(radar.value.refresh_allowed_at).getTime() <= Date.now()
})

const refreshTitle = computed(() => {
  if (canRefresh.value) return '刷新数据'
  return '可在 ' + formatDate(radar.value?.refresh_allowed_at) + ' 后刷新'
})

async function load(): Promise<void> {
  loading.value = true
  errorMessage.value = ''
  try {
    radar.value = await getModelRadar()
  } catch (error) {
    errorMessage.value = getErrorMessage(error, '模型雷达数据暂时不可用')
  } finally {
    loading.value = false
  }
}

async function refresh(): Promise<void> {
  if (!canRefresh.value || refreshing.value) return

  refreshing.value = true
  errorMessage.value = ''
  try {
    radar.value = await refreshModelRadar()
  } catch (error) {
    errorMessage.value = getErrorMessage(error, '模型雷达刷新失败')
  } finally {
    refreshing.value = false
  }
}

function formatDate(value?: string): string {
  if (!value) return '--'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'medium',
    timeStyle: 'short'
  }).format(date)
}

function getErrorMessage(error: unknown, fallback: string): string {
  if (typeof error === 'object' && error !== null && 'message' in error) {
    const message = (error as { message?: unknown }).message
    if (typeof message === 'string' && message.trim()) return message
  }
  return fallback
}

onMounted(load)
</script>
