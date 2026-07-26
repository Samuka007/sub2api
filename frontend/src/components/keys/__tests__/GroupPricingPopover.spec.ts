import { mount } from '@vue/test-utils'
import { ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    locale: ref('en')
  })
}))

import GroupPricingPopover from '../GroupPricingPopover.vue'

describe('GroupPricingPopover', () => {
  it('shows model prices without the display-only reference multiplier', () => {
    const wrapper = mount(GroupPricingPopover, {
      props: {
        groupName: 'GPT-Pro',
        platform: 'openai',
        models: [
          {
            name: 'gpt-5.6-sol',
            platform: 'openai',
            pricing: {
              billing_mode: 'token',
              input_price: 0.000005,
              output_price: 0.00003,
              cache_read_price: 0.0000005,
              cache_write_price: 0.00000625,
              image_input_price: null,
              image_output_price: null,
              per_request_price: null,
              intervals: [],
              reference_multiplier: 0.25,
              reference_source: 'upstream'
            }
          }
        ]
      },
      slots: {
        default: 'GPT-Pro'
      },
      global: {
        stubs: {
          Teleport: true
        }
      }
    })

    expect(wrapper.findAll('th').map((cell) => cell.text())).toEqual([
      'Model',
      'Input',
      'Output',
      'Cache read',
      'Cache write'
    ])
    expect(wrapper.findAll('tbody td')).toHaveLength(5)
    expect(wrapper.text()).toContain('gpt-5.6-sol')
    expect(wrapper.text()).toContain('$5 /1M')
    expect(wrapper.text()).toContain('$30 /1M')
    expect(wrapper.text()).not.toContain('Rate')
    expect(wrapper.text()).not.toContain('x0.25')

    wrapper.unmount()
  })
})
