<template>
  <div
    class="min-h-screen"
    :class="isAdminRoute ? 'bg-gray-50 dark:bg-dark-950' : 'user-console-shell bg-[#f4f6f5] dark:bg-[#0b1113]'"
  >
    <!-- Background Decoration -->
    <div v-if="isAdminRoute" class="pointer-events-none fixed inset-0 bg-mesh-gradient"></div>

    <!-- Sidebar -->
    <AppSidebar :variant="layoutVariant" />

    <!-- Main Content Area -->
    <div
      class="relative min-h-screen transition-all duration-300"
      :class="[
        sidebarCollapsed
          ? 'lg:ml-[72px]'
          : isAdminRoute
            ? 'lg:ml-64'
            : 'lg:ml-[232px]'
      ]"
    >
      <!-- Header -->
      <AppHeader :variant="layoutVariant" />

      <!-- Main Content -->
      <main :class="isAdminRoute ? 'p-4 md:p-6 lg:p-8' : 'px-4 py-6 sm:px-6 lg:px-8 lg:py-7'">
        <slot />
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import '@/styles/onboarding.css'
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useAppStore } from '@/stores'
import { useAuthStore } from '@/stores/auth'
import { useOnboardingTour } from '@/composables/useOnboardingTour'
import { useOnboardingStore } from '@/stores/onboarding'
import AppSidebar from './AppSidebar.vue'
import AppHeader from './AppHeader.vue'

const appStore = useAppStore()
const authStore = useAuthStore()
const route = useRoute()
const sidebarCollapsed = computed(() => appStore.sidebarCollapsed)
const isAdmin = computed(() => authStore.user?.role === 'admin')
const isAdminRoute = computed(() => (
  route.path.startsWith('/admin') || (route.name === 'CustomPage' && isAdmin.value)
))
const layoutVariant = computed<'admin' | 'user'>(() => isAdminRoute.value ? 'admin' : 'user')

const { replayTour } = useOnboardingTour({
  storageKey: isAdmin.value ? 'admin_guide' : 'user_guide',
  autoStart: true
})

const onboardingStore = useOnboardingStore()

onMounted(() => {
  onboardingStore.setReplayCallback(replayTour)
})

defineExpose({ replayTour })
</script>
