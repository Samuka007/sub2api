import { shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  appStore: {
    siteName: 'SCIbuddy',
    siteLogo: '',
    cachedPublicSettings: { site_subtitle: 'AI API gateway' },
    publicSettingsLoaded: true,
    fetchPublicSettings: vi.fn(),
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => mocks.appStore,
}))

vi.mock('@/utils/url', () => ({
  sanitizeUrl: (value: string) => value,
}))

import AuthLayout from '../AuthLayout.vue'

describe('AuthLayout background variants', () => {
  beforeEach(() => {
    mocks.appStore.fetchPublicSettings.mockReset()
  })

  it('keeps the existing background by default', () => {
    const wrapper = shallowMount(AuthLayout)

    expect(wrapper.find('[data-testid="auth-default-background"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="auth-home-background"]').exists()).toBe(false)
  })

  it('uses the Home robot background when requested', () => {
    const wrapper = shallowMount(AuthLayout, {
      props: { backgroundVariant: 'home' },
    })

    const background = wrapper.get('[data-testid="auth-home-background"]')
    expect(background.attributes('src')).toBe('/assets/images/hero-robot-pexels-8294657.jpg')
    expect(wrapper.find('[data-testid="auth-default-background"]').exists()).toBe(false)
    expect(wrapper.find('.auth-home-card').exists()).toBe(true)
    expect(wrapper.find('.auth-home-card').classes()).toContain('dark')
  })
})
