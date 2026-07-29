<template>
  <div class="relative flex min-h-[100dvh] items-center justify-center overflow-hidden p-4">
    <template v-if="backgroundVariant === 'home'">
      <img
        data-testid="auth-home-background"
        src="/assets/images/hero-robot-pexels-8294657.jpg"
        alt=""
        class="absolute inset-0 h-full w-full object-cover object-[82%_center] sm:object-center"
        aria-hidden="true"
      />
      <div class="auth-home-scrim absolute inset-0" aria-hidden="true"></div>
    </template>

    <template v-else>
      <div
        data-testid="auth-default-background"
        class="absolute inset-0 bg-gradient-to-br from-gray-50 via-primary-50/30 to-gray-100 dark:from-dark-950 dark:via-dark-900 dark:to-dark-950"
      ></div>

      <!-- Decorative Elements -->
      <div class="pointer-events-none absolute inset-0 overflow-hidden">
        <div
          class="absolute -right-40 -top-40 h-80 w-80 rounded-full bg-primary-400/20 blur-3xl"
        ></div>
        <div
          class="absolute -bottom-40 -left-40 h-80 w-80 rounded-full bg-primary-500/15 blur-3xl"
        ></div>
        <div
          class="absolute left-1/2 top-1/2 h-96 w-96 -translate-x-1/2 -translate-y-1/2 rounded-full bg-primary-300/10 blur-3xl"
        ></div>
        <div
          class="absolute inset-0 bg-[linear-gradient(rgba(20,184,166,0.03)_1px,transparent_1px),linear-gradient(90deg,rgba(20,184,166,0.03)_1px,transparent_1px)] bg-[size:64px_64px]"
        ></div>
      </div>
    </template>

    <!-- Content Container -->
    <div class="relative z-10 w-full max-w-md">
      <!-- Logo/Brand -->
      <div class="mb-8 text-center">
        <!-- Custom Logo or Default Logo -->
        <template v-if="settingsLoaded">
          <div
            class="mb-4 inline-flex h-16 w-16 items-center justify-center overflow-hidden rounded-2xl shadow-lg"
            :class="backgroundVariant === 'home' ? 'border border-white/20 bg-white/10 shadow-black/40 backdrop-blur-md' : 'shadow-primary-500/30'"
          >
            <img :src="siteLogo || '/logo.svg'" alt="Logo" class="h-full w-full object-contain" />
          </div>
          <h1
            class="mb-2 text-3xl font-bold"
            :class="backgroundVariant === 'home' ? 'text-white' : 'text-gradient'"
          >
            {{ siteName }}
          </h1>
          <p
            class="text-sm"
            :class="backgroundVariant === 'home' ? 'text-white/65' : 'text-gray-500 dark:text-dark-400'"
          >
            {{ siteSubtitle }}
          </p>
        </template>
      </div>

      <!-- Card Container -->
      <div
        class="card-glass rounded-2xl p-8 shadow-glass"
        :class="{ 'auth-home-card dark': backgroundVariant === 'home' }"
      >
        <slot />
      </div>

      <!-- Footer Links -->
      <div
        class="mt-6 text-center text-sm"
        :class="{ 'auth-home-footer': backgroundVariant === 'home' }"
      >
        <slot name="footer" />
      </div>

      <!-- Copyright -->
      <div
        class="mt-8 text-center text-xs"
        :class="backgroundVariant === 'home' ? 'text-white/45' : 'text-gray-400 dark:text-dark-500'"
      >
        &copy; {{ currentYear }} {{ siteName }}. All rights reserved.
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useAppStore } from '@/stores'
import { sanitizeUrl } from '@/utils/url'

withDefaults(defineProps<{
  backgroundVariant?: 'default' | 'home'
}>(), {
  backgroundVariant: 'default',
})

const appStore = useAppStore()

const siteName = computed(() => appStore.siteName || 'SCIbuddy')
const siteLogo = computed(() => sanitizeUrl(appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || 'Subscription to API Conversion Platform')
const settingsLoaded = computed(() => appStore.publicSettingsLoaded)

const currentYear = computed(() => new Date().getFullYear())

onMounted(() => {
  appStore.fetchPublicSettings()
})
</script>

<style scoped>
.text-gradient {
  @apply bg-gradient-to-r from-primary-600 to-primary-500 bg-clip-text text-transparent;
}

.auth-home-scrim {
  background:
    linear-gradient(90deg, rgba(5, 7, 13, 0.9) 0%, rgba(7, 8, 16, 0.66) 48%, rgba(8, 10, 18, 0.16) 76%),
    linear-gradient(180deg, rgba(3, 5, 10, 0.38) 0%, rgba(3, 5, 10, 0.08) 55%, rgba(3, 5, 10, 0.58) 100%);
}

.auth-home-card {
  border-color: rgba(255, 255, 255, 0.16);
  background: rgba(12, 15, 23, 0.9);
  box-shadow:
    inset 0 1px 0 rgba(255, 255, 255, 0.08),
    0 24px 70px rgba(0, 0, 0, 0.42);
}

.auth-home-footer :deep(p) {
  color: rgba(255, 255, 255, 0.66);
}

@media (max-width: 639px) {
  .auth-home-scrim {
    background:
      linear-gradient(180deg, rgba(5, 7, 13, 0.86) 0%, rgba(7, 8, 16, 0.7) 54%, rgba(5, 7, 13, 0.82) 100%),
      linear-gradient(90deg, rgba(5, 7, 13, 0.5) 0%, rgba(5, 7, 13, 0.16) 100%);
  }
}
</style>
