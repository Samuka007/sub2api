import { shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  route: { path: '/dashboard' },
  routerPush: vi.fn(),
  appStore: {
    sidebarCollapsed: false,
    mobileOpen: false,
    siteName: 'Sub2API',
    siteLogo: '',
    siteVersion: '1.0.0',
    publicSettingsLoaded: true,
    backendModeEnabled: false,
    cachedPublicSettings: { custom_menu_items: [], channel_monitor_enabled: true },
    sidebarScrollTop: 0,
    toggleSidebar: vi.fn(),
    setMobileOpen: vi.fn(),
  },
  authStore: {
    isAdmin: false,
    isSimpleMode: false,
  },
  adminSettingsStore: {
    opsMonitoringEnabled: false,
    paymentEnabled: false,
    customMenuItems: [],
    fetch: vi.fn(),
  },
  onboardingStore: {
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn(),
  },
  refreshBatchImageAccess: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRoute: () => mocks.route,
  useRouter: () => ({ push: mocks.routerPush }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => ({
        'nav.channelStatus': '渠道状态',
        'nav.channelMonitor': '渠道监控',
        'nav.myAccount': '我的账户',
      })[key] ?? key,
    }),
  }
})

vi.mock('@/stores', () => ({
  useAppStore: () => mocks.appStore,
  useAuthStore: () => mocks.authStore,
  useAdminSettingsStore: () => mocks.adminSettingsStore,
  useOnboardingStore: () => mocks.onboardingStore,
}))

vi.mock('@/utils/featureFlags', () => ({
  FeatureFlags: {
    channelMonitor: { key: 'channel_monitor_enabled' },
    payment: {},
    availableChannels: {},
    affiliate: {},
    riskControl: {},
  },
  makeSidebarFlag: (flag: { key?: string }) => () =>
    flag.key !== 'channel_monitor_enabled' ||
    mocks.appStore.cachedPublicSettings?.channel_monitor_enabled !== false,
}))

vi.mock('@/composables/useBatchImageAccess', () => ({
  useBatchImageAccess: () => ({
    canUseBatchImage: { value: false },
    refreshBatchImageAccess: mocks.refreshBatchImageAccess,
  }),
}))

import AppSidebar from '../AppSidebar.vue'

const RouterLinkStub = {
  props: ['to'],
  template: '<a :data-to="to"><slot /></a>',
}

describe('AppSidebar channel monitor navigation', () => {
  beforeEach(() => {
    mocks.route.path = '/dashboard'
    mocks.authStore.isAdmin = false
    mocks.appStore.cachedPublicSettings.channel_monitor_enabled = true
    mocks.adminSettingsStore.fetch.mockClear()
    mocks.refreshBatchImageAccess.mockClear()
  })

  it('renders channel status in the regular user sidebar', () => {
    const wrapper = shallowMount(AppSidebar, {
      props: { variant: 'user' },
      global: { stubs: { RouterLink: RouterLinkStub, VersionBadge: true } },
    })

    expect(wrapper.find('[data-to="/monitor"]').exists()).toBe(true)
  })

  it('hides channel status when the feature is explicitly disabled', () => {
    mocks.appStore.cachedPublicSettings.channel_monitor_enabled = false

    const wrapper = shallowMount(AppSidebar, {
      props: { variant: 'user' },
      global: { stubs: { RouterLink: RouterLinkStub, VersionBadge: true } },
    })

    expect(wrapper.find('[data-to="/monitor"]').exists()).toBe(false)
  })

  it('keeps the administrator channel monitor entry', () => {
    mocks.authStore.isAdmin = true
    mocks.route.path = '/admin/channels/monitor'

    const wrapper = shallowMount(AppSidebar, {
      props: { variant: 'admin' },
      global: { stubs: { RouterLink: RouterLinkStub, VersionBadge: true } },
    })

    expect(wrapper.find('[data-to="/admin/channels/monitor"]').exists()).toBe(true)
  })
})
