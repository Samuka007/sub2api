import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: { count?: number }) => (
        key === 'dashboard.platformCount' ? `${params?.count} platforms` : key
      )
    })
  }
})

import UserDashboardPlatformBreakdown from '../UserDashboardPlatformBreakdown.vue'
import type { PlatformDashboardStats } from '@/api/usage'

function platform(overrides: Partial<PlatformDashboardStats> & Pick<PlatformDashboardStats, 'platform'>): PlatformDashboardStats {
  return {
    total_requests: 0,
    total_tokens: 0,
    total_actual_cost: 0,
    today_requests: 0,
    today_tokens: 0,
    today_actual_cost: 0,
    ...overrides
  }
}

describe('UserDashboardPlatformBreakdown', () => {
  it('always renders the five supported platforms in the expected order', () => {
    const wrapper = mount(UserDashboardPlatformBreakdown, { props: { platforms: [] } })
    const cards = wrapper.findAll('[data-testid^="platform-card-"]')

    expect(cards).toHaveLength(5)
    expect(cards.map(card => card.find('h3').text())).toEqual([
      'Claude',
      'OpenAI',
      'Gemini',
      'Antigravity',
      'grok'
    ])
    expect(wrapper.text()).toContain('5 platforms')
    expect(wrapper.text()).toContain('$0.0000')
  })

  it('combines structured Claude aliases and formats usage totals', () => {
    const wrapper = mount(UserDashboardPlatformBreakdown, {
      props: {
        platforms: [
          platform({
            platform: 'anthropic',
            total_requests: 600,
            total_tokens: 65_000_000,
            total_actual_cost: 18.5,
            today_actual_cost: 0.5
          }),
          platform({
            platform: 'Claude',
            total_requests: 88,
            total_tokens: 4_300_000,
            total_actual_cost: 1.2562,
            today_actual_cost: 0.0956
          })
        ]
      }
    })

    const claude = wrapper.get('[data-testid="platform-card-anthropic"]')
    expect(claude.text()).toContain('$19.7562')
    expect(claude.text()).toContain('$0.5956')
    expect(claude.text()).toContain('688')
    expect(claude.text()).toContain('69.3M')
  })

  it('ignores unsupported platform rows instead of misclassifying them', () => {
    const wrapper = mount(UserDashboardPlatformBreakdown, {
      props: {
        platforms: [platform({ platform: 'unknown-provider', total_actual_cost: 99 })]
      }
    })

    expect(wrapper.text()).not.toContain('$99.0000')
  })
})
