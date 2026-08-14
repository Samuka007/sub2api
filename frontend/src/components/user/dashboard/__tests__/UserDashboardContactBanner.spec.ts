import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

const appState = vi.hoisted(() => ({ contactInfo: '' }))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => {
        const dict: Record<string, string> = {
          'dashboard.contactBanner.label': 'Contact us',
          'dashboard.contactBanner.dismiss': 'Dismiss'
        }
        return dict[key] ?? key
      }
    })
  }
})

vi.mock('@/stores', () => ({
  useAppStore: () => appState
}))

import UserDashboardContactBanner from '../UserDashboardContactBanner.vue'

const DISMISS_STORAGE_KEY = 'dashboard_contact_banner_dismissed'
const BANNER_SELECTOR = '[data-testid="dashboard-contact-banner"]'

describe('UserDashboardContactBanner', () => {
  beforeEach(() => {
    localStorage.clear()
    appState.contactInfo = ''
  })

  it('renders nothing when contact info is empty', () => {
    const wrapper = mount(UserDashboardContactBanner)

    expect(wrapper.find(BANNER_SELECTOR).exists()).toBe(false)
  })

  it('renders nothing when contact info is only whitespace', () => {
    appState.contactInfo = '   '

    const wrapper = mount(UserDashboardContactBanner)

    expect(wrapper.find(BANNER_SELECTOR).exists()).toBe(false)
  })

  it('renders the label and trimmed contact info when set', () => {
    appState.contactInfo = '  QQ: 123456789  '

    const wrapper = mount(UserDashboardContactBanner)
    const banner = wrapper.get(BANNER_SELECTOR)

    expect(banner.text()).toContain('Contact us')
    // Assert on the raw value span text (not VTU .text(), which trims) so a
    // broken .trim() that leaks surrounding whitespace would fail this test.
    expect(banner.findAll('span').at(-1)!.element.textContent).toBe('QQ: 123456789')
    expect(wrapper.get('button').attributes('aria-label')).toBe('Dismiss')
  })

  it('hides the banner and persists the dismissal when dismissed', async () => {
    appState.contactInfo = 'QQ: 123456789'

    const wrapper = mount(UserDashboardContactBanner)
    await wrapper.get('button[aria-label="Dismiss"]').trigger('click')

    expect(wrapper.find(BANNER_SELECTOR).exists()).toBe(false)
    expect(localStorage.getItem(DISMISS_STORAGE_KEY)).toBe('true')
  })

  it('stays hidden on a fresh mount when a dismissal was persisted', () => {
    localStorage.setItem(DISMISS_STORAGE_KEY, 'true')
    appState.contactInfo = 'QQ: 123456789'

    const wrapper = mount(UserDashboardContactBanner)

    expect(wrapper.find(BANNER_SELECTOR).exists()).toBe(false)
  })
})
