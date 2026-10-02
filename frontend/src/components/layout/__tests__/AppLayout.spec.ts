import { shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  appStore: {
    sidebarCollapsed: false,
  },
  authStore: {
    isAdmin: false,
  },
  replayTour: vi.fn(),
  setReplayCallback: vi.fn(),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => mocks.appStore,
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => mocks.authStore,
}))

vi.mock('@/composables/useOnboardingTour', () => ({
  useOnboardingTour: () => ({ replayTour: mocks.replayTour }),
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({ setReplayCallback: mocks.setReplayCallback }),
}))

import AppLayout from '../AppLayout.vue'

const layoutStubs = {
  AppSidebar: {
    props: ['variant'],
    template: '<aside data-testid="sidebar" :data-variant="variant" />',
  },
  AppHeader: {
    props: ['variant'],
    template: '<header data-testid="header" :data-variant="variant" />',
  },
}

describe('AppLayout role-aware shell', () => {
  beforeEach(() => {
    mocks.authStore.isAdmin = false
    mocks.appStore.sidebarCollapsed = false
    mocks.replayTour.mockReset()
    mocks.setReplayCallback.mockReset()
  })

  it('keeps the admin shell on shared personal pages for administrators', () => {
    mocks.authStore.isAdmin = true

    const wrapper = shallowMount(AppLayout, {
      global: { stubs: layoutStubs },
    })

    expect(wrapper.get('[data-testid="sidebar"]').attributes('data-variant')).toBe('admin')
    expect(wrapper.get('[data-testid="header"]').attributes('data-variant')).toBe('admin')
    expect(wrapper.get('main').classes()).toContain('lg:p-8')
    expect(wrapper.classes()).not.toContain('user-console-shell')
  })

  it('keeps the user shell for regular users', () => {
    const wrapper = shallowMount(AppLayout, {
      global: { stubs: layoutStubs },
    })

    expect(wrapper.get('[data-testid="sidebar"]').attributes('data-variant')).toBe('user')
    expect(wrapper.get('[data-testid="header"]').attributes('data-variant')).toBe('user')
    expect(wrapper.get('main').classes()).toContain('lg:py-7')
    expect(wrapper.classes()).toContain('user-console-shell')
  })
})
