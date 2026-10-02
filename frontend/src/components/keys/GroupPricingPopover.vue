<template>
  <span
    ref="triggerEl"
    class="inline-flex"
    @mouseenter="onEnter"
    @mouseleave="onLeave"
    @focusin="onEnter"
    @focusout="onLeave"
  >
    <slot />

    <Teleport to="body">
      <div
        v-show="show"
        ref="popoverEl"
        role="tooltip"
        class="pointer-events-none fixed z-[100000030] w-[min(44rem,calc(100vw-1rem))] overflow-hidden rounded-lg border bg-white text-xs shadow-xl dark:bg-dark-800"
        :class="popoverBorderClass"
        :style="popoverStyle"
      >
        <div
          class="flex items-center justify-between gap-2 rounded-t-lg border-b px-3 py-2"
          :class="[popoverHeaderClass, popoverBorderClass]"
        >
          <span class="truncate font-semibold">{{ groupName }}</span>
          <span class="flex-shrink-0 text-[11px] opacity-80">{{ localText('\u6a21\u578b\u4ef7\u683c', 'Model pricing') }}</span>
        </div>

        <div v-if="sortedModels.length === 0" class="p-3 text-gray-500 dark:text-gray-400">
          {{ localText('\u6682\u65e0\u53ef\u89c1\u6a21\u578b\u5b9a\u4ef7', 'No visible model pricing') }}
        </div>

        <div v-else class="p-3">
          <table class="w-full table-fixed border-collapse text-left">
            <thead>
              <tr class="border-b border-gray-100 text-[11px] font-medium text-gray-500 dark:border-dark-700 dark:text-gray-400">
                <th class="w-[13rem] pb-1.5 pr-2">{{ localText('\u6a21\u578b', 'Model') }}</th>
                <th class="pb-1.5 pr-2">{{ localText('\u8f93\u5165', 'Input') }}</th>
                <th class="pb-1.5 pr-2">{{ localText('\u8f93\u51fa', 'Output') }}</th>
                <th class="pb-1.5 pr-2">{{ localText('\u7f13\u5b58\u8bfb', 'Cache read') }}</th>
                <th class="pb-1.5 pr-2">{{ localText('\u7f13\u5b58\u5199', 'Cache write') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="model in sortedModels"
                :key="`${model.platform || platform || 'model'}-${model.name}`"
                class="border-b border-gray-50 last:border-b-0 dark:border-dark-700/60"
              >
                <td class="py-1.5 pr-2 font-mono text-[11px] font-medium text-gray-800 dark:text-gray-100">
                  <span class="block truncate" :title="model.name">{{ model.name }}</span>
                </td>
                <td class="py-1.5 pr-2 font-mono text-gray-700 dark:text-gray-300">
                  {{ priceFor(model, 'input') }}
                </td>
                <td class="py-1.5 pr-2 font-mono text-gray-700 dark:text-gray-300">
                  {{ priceFor(model, 'output') }}
                </td>
                <td class="py-1.5 pr-2 font-mono text-gray-700 dark:text-gray-300">
                  {{ priceFor(model, 'cacheRead') }}
                </td>
                <td class="py-1.5 pr-2 font-mono text-gray-700 dark:text-gray-300">
                  {{ priceFor(model, 'cacheWrite') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </Teleport>
  </span>
</template>

<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UserSupportedModel } from '@/api/channels'
import {
  BILLING_MODE_IMAGE,
  BILLING_MODE_PER_REQUEST,
  BILLING_MODE_TOKEN
} from '@/constants/channel'
import { formatScaled } from '@/utils/pricing'
import { platformBadgeLightClass, platformBorderClass } from '@/utils/platformColors'

const props = defineProps<{
  groupName: string
  platform?: string | null
  models: UserSupportedModel[]
}>()

const { locale } = useI18n()
const perMillionScale = 1_000_000

const sortedModels = computed(() =>
  [...props.models].sort((a, b) => a.name.localeCompare(b.name))
)

const popoverBorderClass = computed(() =>
  props.platform ? platformBorderClass(props.platform) : 'border-gray-200 dark:border-dark-600'
)

const popoverHeaderClass = computed(() =>
  props.platform
    ? platformBadgeLightClass(props.platform)
    : 'bg-gray-50 text-gray-700 dark:bg-dark-700/60 dark:text-gray-300'
)

function localText(zh: string, en: string): string {
  return String(locale.value).toLowerCase().startsWith('zh') ? zh : en
}

type PriceKind = 'input' | 'output' | 'cacheRead' | 'cacheWrite'

function priceFor(model: UserSupportedModel, kind: PriceKind): string {
  const pricing = model.pricing
  if (!pricing) return '-'

  if (pricing.billing_mode === BILLING_MODE_TOKEN) {
    const valueByKind: Record<PriceKind, number | null> = {
      input: pricing.input_price,
      output: pricing.output_price,
      cacheRead: pricing.cache_read_price,
      cacheWrite: pricing.cache_write_price
    }
    return formatPrice(valueByKind[kind], perMillionScale, '/1M')
  }

  if (kind !== 'output') return '-'
  if (pricing.billing_mode === BILLING_MODE_PER_REQUEST) {
    return formatPrice(pricing.per_request_price, 1, localText('/\u6b21', '/req'))
  }
  if (pricing.billing_mode === BILLING_MODE_IMAGE) {
    return formatPrice(pricing.image_output_price, 1, localText('/\u5f20', '/image'))
  }
  return '-'
}

function formatPrice(value: number | null | undefined, scale: number, unit: string): string {
  if (value == null) return '-'
  return `${formatScaled(value, scale)} ${unit}`
}

const show = ref(false)
const triggerEl = ref<HTMLElement | null>(null)
const popoverEl = ref<HTMLElement | null>(null)
const popoverStyle = ref<Record<string, string>>({ top: '0px', left: '0px' })

function updatePosition() {
  const trigger = triggerEl.value
  if (!trigger) return
  const rect = trigger.getBoundingClientRect()
  const margin = 8
  const popover = popoverEl.value
  const popWidth = popover?.offsetWidth ?? 704
  const popHeight = popover?.offsetHeight ?? 360
  const vw = window.innerWidth
  const vh = window.innerHeight

  let top = rect.bottom + margin
  if (top + popHeight > vh - margin) {
    top = Math.max(margin, rect.top - popHeight - margin)
  }

  let left = rect.left + rect.width / 2 - popWidth / 2
  if (left < margin) left = margin
  if (left + popWidth > vw - margin) left = vw - margin - popWidth

  popoverStyle.value = {
    top: `${Math.round(top)}px`,
    left: `${Math.round(left)}px`
  }
}

function onEnter() {
  show.value = true
  nextTick(() => {
    updatePosition()
    window.addEventListener('scroll', updatePosition, true)
    window.addEventListener('resize', updatePosition)
  })
}

function onLeave() {
  show.value = false
  window.removeEventListener('scroll', updatePosition, true)
  window.removeEventListener('resize', updatePosition)
}

onBeforeUnmount(() => {
  window.removeEventListener('scroll', updatePosition, true)
  window.removeEventListener('resize', updatePosition)
})
</script>
