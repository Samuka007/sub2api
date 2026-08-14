<template>
  <section
    v-if="contactInfo && !dismissed"
    class="contact-banner flex min-w-0 flex-1 items-center gap-2.5 border border-[#dfe5e3] bg-white px-3 py-1.5 dark:border-[#273439] dark:bg-[#121c20]"
    :aria-label="t('dashboard.contactBanner.label')"
    data-testid="dashboard-contact-banner"
  >
    <span class="banner-icon" aria-hidden="true">
      <Icon name="chat" size="sm" :stroke-width="1.8" />
    </span>
    <p class="min-w-0 flex-1 truncate text-sm text-[#71807c] dark:text-[#899995]">
      <span class="font-semibold text-[#238b76] dark:text-[#63cdb6]">{{ t('dashboard.contactBanner.label') }}</span>
      <span class="mx-2 text-[#8a9693] dark:text-[#758783]" aria-hidden="true">·</span>
      <span class="font-medium text-[#26312f] dark:text-[#dce5e2]">{{ contactInfo }}</span>
    </p>
    <button
      type="button"
      class="dismiss-button"
      :aria-label="t('dashboard.contactBanner.dismiss')"
      @click="dismiss"
    >
      <Icon name="x" size="sm" :stroke-width="1.8" />
    </button>
  </section>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()
const appStore = useAppStore()

const DISMISS_STORAGE_KEY = 'dashboard_contact_banner_dismissed'

const contactInfo = computed(() => (appStore.contactInfo || '').trim())

function readDismissed(): boolean {
  try {
    return localStorage.getItem(DISMISS_STORAGE_KEY) === 'true'
  } catch {
    // ignore localStorage failures
    return false
  }
}

const dismissed = ref(readDismissed())

function dismiss() {
  dismissed.value = true
  try {
    localStorage.setItem(DISMISS_STORAGE_KEY, 'true')
  } catch {
    // ignore localStorage failures
  }
}
</script>

<style scoped>
.contact-banner {
  min-height: 38px;
  border-radius: 8px;
  box-shadow: 0 1px 2px rgb(18 31 28 / 4%);
}

.banner-icon {
  display: inline-flex;
  width: 24px;
  height: 24px;
  flex: 0 0 24px;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  background: #e8f8f3;
  color: #238b76;
}

.dark .banner-icon {
  background: #16352f;
  color: #63cdb6;
}

.dismiss-button {
  display: inline-flex;
  width: 24px;
  height: 24px;
  flex: 0 0 24px;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  color: #8a9693;
  transition: background-color 150ms ease, color 150ms ease;
}

.dismiss-button:hover {
  background: #f1f5f3;
  color: #177d69;
}

.dark .dismiss-button {
  color: #758783;
}

.dark .dismiss-button:hover {
  background: #1b2a2e;
  color: #63cdb6;
}

.dismiss-button:focus-visible {
  outline: 2px solid #238b76;
  outline-offset: 2px;
}

.dark .dismiss-button:focus-visible {
  outline-color: #63cdb6;
}

@media (prefers-reduced-motion: reduce) {
  .dismiss-button {
    transition: none;
  }
}
</style>
