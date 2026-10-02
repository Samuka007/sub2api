import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import GroupRateMultipliersModal from '../GroupRateMultipliersModal.vue'

const {
  getGroupRateMultipliers,
  batchSetGroupRateMultipliers,
  showSuccess,
  showError
} = vi.hoisted(() => ({
  getGroupRateMultipliers: vi.fn(),
  batchSetGroupRateMultipliers: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      getGroupRateMultipliers,
      batchSetGroupRateMultipliers
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError })
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

const group = {
  id: 7,
  name: 'OpenAI',
  platform: 'openai',
  rate_multiplier: 1
} as never

const mountModal = async () => {
  const wrapper = mount(GroupRateMultipliersModal, {
    props: { show: false, group },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show', 'title'],
          template: '<div v-if="show"><slot /></div>'
        },
        Icon: true,
        Pagination: true,
        PlatformIcon: true
      }
    }
  })

  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('GroupRateMultipliersModal batch adjustment', () => {
  beforeEach(() => {
    getGroupRateMultipliers.mockReset()
    batchSetGroupRateMultipliers.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    getGroupRateMultipliers.mockResolvedValue([
      {
        user_id: 11,
        user_name: 'user',
        user_email: 'user@example.test',
        user_notes: '',
        user_status: 'active',
        rate_multiplier: 0.8,
        rpm_override: null
      }
    ])
  })

  it('applies an addition with the multiplier left blank', async () => {
    const wrapper = await mountModal()

    const apply = wrapper.get('[data-testid="apply-batch-rate-adjustment"]')
    expect(apply.attributes('disabled')).toBeDefined()

    await wrapper.get('[data-testid="batch-rate-addition"]').setValue('0.1')
    expect(apply.attributes('disabled')).toBeUndefined()

    await apply.trigger('click')

    expect(
      (wrapper.get('[data-testid="group-rate-input"]').element as HTMLInputElement).value
    ).toBe('0.9')
    expect(wrapper.get('[data-testid="batch-rate-multiplier"]').element.value).toBe('')
    expect(wrapper.get('[data-testid="batch-rate-addition"]').element.value).toBe('')
  })
})
