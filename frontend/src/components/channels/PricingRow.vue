<template>
  <div class="flex justify-between gap-2">
    <span class="text-gray-500 dark:text-gray-400">{{ label }}</span>
    <span class="font-mono">{{ display }}</span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatScaled } from '@/utils/pricing'

const props = withDefaults(
  defineProps<{
    label: string
    value: number | null
    unit: string
    scale: number
    multiplier?: number | null
  }>(),
  { value: null, multiplier: null }
)

const display = computed(() => {
  if (props.value == null) return '-'
  const price = `${formatScaled(props.value, props.scale)} ${props.unit}`
  if (props.multiplier == null) return price
  return `${price}  x${formatMultiplier(props.multiplier)}`
})

function formatMultiplier(value: number): string {
  return value.toPrecision(10).replace(/\.?0+$/, '')
}
</script>
