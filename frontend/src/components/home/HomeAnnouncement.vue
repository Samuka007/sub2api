<template>
  <Transition name="home-announcement">
    <div
      v-if="announcement && isVisible"
      class="pointer-events-none fixed inset-0 z-30"
      data-testid="home-announcement"
    >
      <button
        v-if="isMaximized"
        type="button"
        class="pointer-events-auto absolute inset-0 bg-black/65 backdrop-blur-sm"
        :aria-label="copy.contract"
        data-testid="home-announcement-backdrop"
        @click="isMaximized = false"
      ></button>

      <aside
        class="home-announcement-panel liquid-glass pointer-events-auto rounded-2xl px-5 pb-5 pt-6 text-left text-white shadow-2xl transition-[width,max-height] duration-300 md:px-6 md:py-5"
        :class="
          isMaximized
            ? 'absolute left-1/2 top-1/2 max-h-[calc(100dvh-2rem)] w-[calc(100%-2rem)] max-w-4xl -translate-x-1/2 -translate-y-1/2'
            : 'absolute inset-x-4 bottom-4 mx-auto max-w-lg md:bottom-auto md:top-[6.75rem] md:max-w-2xl'
        "
        :role="isMaximized ? 'dialog' : 'status'"
        :aria-modal="isMaximized ? 'true' : undefined"
        aria-live="polite"
        aria-atomic="true"
        aria-labelledby="home-announcement-title"
        data-testid="home-announcement-panel"
      >
        <button
          type="button"
          class="absolute right-3 top-3 flex h-9 w-9 items-center justify-center rounded-full text-white/55 outline-none transition-colors hover:bg-white/10 hover:text-white focus-visible:ring-2 focus-visible:ring-white"
          :aria-label="copy.close"
          :title="copy.close"
          data-testid="home-announcement-close"
          @click="close"
        >
          <Icon name="x" size="sm" :stroke-width="2" />
        </button>

        <div class="flex items-start gap-3.5 pr-8">
          <span class="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-white/10 text-white/80">
            <Icon name="infoCircle" size="sm" :stroke-width="1.8" />
          </span>
          <div class="min-w-0 flex-1">
            <p class="mb-1 text-[0.65rem] font-medium uppercase tracking-[0.18em] text-white/45">
              {{ copy.label }}
            </p>
            <h2 id="home-announcement-title" class="pr-1 text-base font-medium leading-snug text-white md:text-lg">
              {{ announcement.title }}
            </h2>
            <p
              class="home-announcement-content mt-2 whitespace-pre-line break-words text-sm leading-relaxed text-white/65"
              :class="{ 'home-announcement-content--collapsed': !isMaximized }"
            >
              {{ announcement.content }}
            </p>
          </div>
        </div>

        <div class="mt-4 flex items-center justify-end gap-2 border-t border-white/10 pt-3 md:mt-3 md:border-0 md:pt-0">
          <button
            type="button"
            class="rounded-full px-3 py-2 text-xs font-medium text-white/55 outline-none transition-colors hover:bg-white/5 hover:text-white focus-visible:ring-2 focus-visible:ring-white"
            data-testid="home-announcement-dismiss"
            @click="close"
          >
            {{ copy.dismiss }}
          </button>
          <button
            v-if="hasDetails"
            type="button"
            class="inline-flex items-center gap-1.5 rounded-full bg-white px-4 py-2 text-xs font-medium text-black outline-none transition-transform hover:scale-[1.03] active:scale-[0.98] focus-visible:ring-2 focus-visible:ring-white"
            data-testid="home-announcement-details"
            :aria-expanded="isMaximized"
            @click="isMaximized = !isMaximized"
          >
            {{ isMaximized ? copy.contract : copy.expand }}
            <Icon :name="isMaximized ? 'contract' : 'expand'" size="xs" :stroke-width="2" />
          </button>
        </div>
      </aside>
    </div>
  </Transition>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import announcementsAPI from '@/api/announcements'
import Icon from '@/components/icons/Icon.vue'
import type { PublicAnnouncement } from '@/types'

const props = defineProps<{
  isChinese: boolean
  fallbackAnnouncement?: PublicAnnouncement
}>()

const announcement = ref<PublicAnnouncement | null>(null)
const isVisible = ref(false)
const isMaximized = ref(false)

const copy = computed(() =>
  props.isChinese
    ? {
        label: '站点公告',
        close: '关闭公告',
        dismiss: '本次关闭',
        expand: '放大公告',
        contract: '缩小公告',
      }
    : {
        label: 'Announcement',
        close: 'Close announcement',
        dismiss: 'Dismiss',
        expand: 'Enlarge',
        contract: 'Reduce',
      },
)

const hasDetails = computed(() => {
  const content = announcement.value?.content || ''
  return content.length > 88 || content.includes('\n')
})

function close() {
  isVisible.value = false
}

onMounted(async () => {
  if (props.fallbackAnnouncement) {
    announcement.value = props.fallbackAnnouncement
    isVisible.value = true
  }

  try {
    const items = await announcementsAPI.listPublic()
    const next = items[0]
    if (!next) return
    announcement.value = next
    isVisible.value = true
  } catch {
    // A public notice request must never prevent the homepage from loading.
  }
})
</script>

<style scoped>
.home-announcement-panel {
  background: linear-gradient(135deg, rgb(10 10 10 / 84%), rgb(22 22 22 / 68%));
  border: 1px solid rgb(255 255 255 / 14%);
  box-shadow:
    0 24px 80px rgb(0 0 0 / 40%),
    inset 0 1px 0 rgb(255 255 255 / 8%);
  backdrop-filter: blur(22px) saturate(125%);
  -webkit-backdrop-filter: blur(22px) saturate(125%);
}

.home-announcement-content--collapsed {
  display: -webkit-box;
  overflow: hidden;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}

.home-announcement-content:not(.home-announcement-content--collapsed) {
  max-height: min(58dvh, 34rem);
  overflow-y: auto;
  overscroll-behavior: contain;
}

.home-announcement-enter-active,
.home-announcement-leave-active {
  transition:
    opacity 420ms cubic-bezier(0.16, 1, 0.3, 1),
    transform 420ms cubic-bezier(0.16, 1, 0.3, 1);
}

.home-announcement-enter-from,
.home-announcement-leave-to {
  opacity: 0;
  transform: translateY(18px);
}

@media (min-width: 768px) {
  .home-announcement-enter-from,
  .home-announcement-leave-to {
    transform: translateY(-14px);
  }
}

@media (prefers-reduced-motion: reduce) {
  .home-announcement-enter-active,
  .home-announcement-leave-active {
    transition: none;
  }
}
</style>
