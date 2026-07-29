<template>
  <div class="robot-home relative h-[100dvh] min-h-[540px] overflow-hidden bg-[#090b12] text-white">
    <img
      src="/assets/images/hero-robot-pexels-8294657.jpg"
      :alt="copy.imageAlt"
      class="absolute inset-0 h-full w-full object-cover object-[82%_center] sm:object-center"
    />
    <div class="absolute inset-0 bg-black/20" aria-hidden="true"></div>
    <div class="hero-scrim absolute inset-0" aria-hidden="true"></div>

    <div class="relative z-10 mx-auto flex h-full w-full max-w-[1440px] flex-col px-5 sm:px-8 lg:px-14">
      <header class="hero-enter flex h-20 shrink-0 items-center justify-between border-b border-white/15 sm:h-24">
        <router-link
          to="/home"
          class="flex min-w-0 items-center gap-3 rounded-md outline-none focus-visible:ring-2 focus-visible:ring-white"
          :aria-label="`${siteName} ${copy.home}`"
        >
          <span class="flex h-9 w-9 shrink-0 items-center justify-center overflow-hidden rounded-md border border-white/20 bg-white/10 backdrop-blur-md">
            <img
              :src="siteLogo || '/logo.svg'"
              alt=""
              class="h-6 w-6 object-contain"
            />
          </span>
          <span class="truncate text-base font-semibold tracking-normal sm:text-lg">{{ siteName }}</span>
        </router-link>

        <nav class="flex shrink-0 items-center gap-2 sm:gap-3" :aria-label="copy.navigation">
          <router-link
            :to="primaryPath"
            class="inline-flex h-10 items-center gap-2 rounded-md border border-white/20 bg-white/10 px-3.5 text-sm font-medium text-white backdrop-blur-md transition-colors hover:bg-white/20 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white sm:px-4"
          >
            <Icon :name="isAuthenticated ? 'grid' : 'login'" size="sm" :stroke-width="1.8" />
            {{ primaryLabel }}
          </router-link>
        </nav>
      </header>

      <HomeAnnouncement
        :is-chinese="isChinese"
        :fallback-announcement="showPreviewAnnouncement ? previewAnnouncement : undefined"
      />

      <main class="flex min-h-0 flex-1 items-start pt-10 sm:items-center sm:pt-0">
        <div class="w-full max-w-[43rem] pb-28 sm:pb-20 lg:pb-12">
          <p class="hero-enter hero-delay-1 mb-4 flex items-center gap-3 text-xs font-semibold uppercase tracking-normal text-white/60 sm:text-sm lg:text-lg">
            <span class="h-px w-8 bg-cyan-300" aria-hidden="true"></span>
            {{ copy.eyebrow }}
          </p>

          <h1
            class="hero-enter hero-delay-2 flex items-baseline whitespace-nowrap text-5xl font-semibold leading-none tracking-normal text-white sm:text-6xl lg:text-9xl"
            aria-label="scitrace.cc"
          >
            <span>scitrace</span>
            <span
              aria-hidden="true"
              class="ml-1.5 text-xl font-medium leading-none text-white/60 sm:ml-2 sm:text-2xl lg:ml-2.5 lg:text-5xl"
            >
              .cc
            </span>
          </h1>

          <p class="hero-enter hero-delay-3 mt-5 max-w-xl text-sm leading-7 text-white/70 sm:mt-6 sm:text-base sm:leading-8 lg:text-lg">
            {{ copy.description }}
          </p>

          <div class="hero-enter hero-delay-4 mt-7 flex flex-wrap items-center gap-3 sm:mt-8">
            <router-link
              :to="primaryPath"
              class="inline-flex h-12 items-center gap-3 rounded-md bg-white px-5 text-sm font-semibold text-[#11131b] transition-colors hover:bg-cyan-100 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-cyan-300 sm:px-6"
            >
              {{ copy.primaryAction }}
              <Icon name="arrowRight" size="sm" :stroke-width="2" />
            </router-link>
          </div>
        </div>
      </main>

      <footer class="hero-enter hero-delay-4 flex shrink-0 flex-col gap-3 border-t border-white/15 py-4 text-white/60 sm:flex-row sm:items-center sm:justify-between sm:py-5">
        <ul class="flex items-center gap-4 text-xs sm:gap-6 sm:text-sm" :aria-label="copy.capabilities">
          <li v-for="item in capabilityItems" :key="item.label" class="flex min-w-0 items-center gap-1.5">
            <Icon :name="item.icon" size="xs" :stroke-width="1.8" class="shrink-0 text-cyan-200" />
            <span class="whitespace-nowrap">{{ item.label }}</span>
          </li>
        </ul>
        <a
          href="https://www.pexels.com/photo/close-up-shot-of-white-toy-robot-8294657/"
          target="_blank"
          rel="noopener noreferrer"
          class="w-fit text-[11px] text-white/40 underline decoration-white/25 underline-offset-4 transition-colors hover:text-white/75 focus-visible:rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white"
        >
          {{ copy.photoCredit }}
        </a>
      </footer>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import { useAuthStore, useAppStore } from '@/stores'
import Icon from '@/components/icons/Icon.vue'
import HomeAnnouncement from '@/components/home/HomeAnnouncement.vue'
import { sanitizeUrl } from '@/utils/url'
import type { PublicAnnouncement } from '@/types'

const authStore = useAuthStore()
const appStore = useAppStore()
const route = useRoute()
const { locale } = useI18n()

const isChinese = computed(() => locale.value.startsWith('zh'))
const isAuthenticated = computed(() => authStore.isAuthenticated)
const isPreviewRoute = computed(() => route.name === 'HomeRobotPreview')
const showPreviewAnnouncement = computed(() => import.meta.env.DEV || isPreviewRoute.value)
const siteName = 'SCIbuddy'
const siteLogo = computed(() =>
  sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', {
    allowRelative: true,
    allowDataUrl: true,
  }),
)
const primaryPath = computed(() => {
  if (!authStore.isAuthenticated) return '/login'
  return authStore.isAdmin ? '/admin/dashboard' : '/dashboard'
})
const primaryLabel = computed(() => {
  if (authStore.isAuthenticated) return isChinese.value ? '控制台' : 'Console'
  return isChinese.value ? '登录' : 'Log in'
})

const copy = computed(() =>
  isChinese.value
    ? {
        home: '首页',
        navigation: '主导航',
        imageAlt: '深色背景前的白色机器人',
        eyebrow: '专注于 AI API 网关服务',
        description: '为开发者与团队提供便捷、稳定、可管理的 AI 模型接入服务。',
        primaryAction: authStore.isAuthenticated ? '进入控制台' : '立即开始',
        capabilities: '核心能力',
        photoCredit: '图片来源：Pexels',
      }
    : {
        home: 'home',
        navigation: 'Primary navigation',
        imageAlt: 'A white robot against a dark background',
        eyebrow: 'Focused on AI API gateway services',
        description:
          'Convenient, reliable, and manageable AI model access for developers and teams.',
        primaryAction: authStore.isAuthenticated ? 'Open console' : 'Get started',
        capabilities: 'Core capabilities',
        photoCredit: 'Photo via Pexels',
      },
)

const capabilityItems = computed(() => [
  { icon: 'key' as const, label: isChinese.value ? '统一密钥' : 'One key' },
  { icon: 'server' as const, label: isChinese.value ? '模型路由' : 'Model routing' },
  { icon: 'chart' as const, label: isChinese.value ? '用量可见' : 'Visible usage' },
])

const previewAnnouncement = computed<PublicAnnouncement>(() =>
  isChinese.value
    ? {
        id: -1,
        title: '公告预览',
        content: '这里将展示后台发布的最新公告。关闭后，刷新页面会再次弹出。',
      }
    : {
        id: -1,
        title: 'Announcement preview',
        content: 'The latest announcement from the admin console will appear here. It opens again after every refresh.',
      },
)

onMounted(() => {
  authStore.checkAuth()
})
</script>

<style scoped>
.robot-home {
  font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
}

.hero-scrim {
  background:
    linear-gradient(90deg, rgba(5, 7, 13, 0.88) 0%, rgba(7, 8, 16, 0.64) 38%, rgba(8, 10, 18, 0.08) 72%),
    linear-gradient(180deg, rgba(3, 5, 10, 0.34) 0%, rgba(3, 5, 10, 0) 54%, rgba(3, 5, 10, 0.5) 100%);
}

.hero-enter {
  animation: hero-rise 700ms cubic-bezier(0.16, 1, 0.3, 1) both;
}

.hero-delay-1 {
  animation-delay: 80ms;
}

.hero-delay-2 {
  animation-delay: 150ms;
}

.hero-delay-3 {
  animation-delay: 220ms;
}

.hero-delay-4 {
  animation-delay: 290ms;
}

@keyframes hero-rise {
  from {
    opacity: 0;
    transform: translateY(14px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

@media (max-width: 639px) {
  .hero-scrim {
    background:
      linear-gradient(180deg, rgba(5, 7, 13, 0.88) 0%, rgba(7, 8, 16, 0.68) 43%, rgba(8, 10, 18, 0.08) 70%),
      linear-gradient(90deg, rgba(5, 7, 13, 0.48) 0%, rgba(5, 7, 13, 0.06) 100%);
  }
}

@media (max-height: 680px) {
  .robot-home main {
    padding-top: 1rem;
  }

  .robot-home main > div {
    padding-bottom: 1.5rem;
  }

  .robot-home h1 {
    font-size: 2.25rem;
  }

  .robot-home footer {
    padding-top: 0.625rem;
    padding-bottom: 0.625rem;
  }
}

@media (prefers-reduced-motion: reduce) {
  .hero-enter {
    animation: none;
  }
}
</style>
