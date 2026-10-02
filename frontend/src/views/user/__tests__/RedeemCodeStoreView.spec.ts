import { flushPromises, shallowMount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RedeemCodeStoreView from '../RedeemCodeStoreView.vue'

const mocks = vi.hoisted(() => ({
  getProducts: vi.fn(),
  getPaymentChannels: vi.fn(),
  getPriceQuote: vi.fn(),
  push: vi.fn(),
  refreshUser: vi.fn(),
  showError: vi.fn(),
  showInfo: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: mocks.push }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    user: {
      username: 'buyer',
      email: 'buyer@example.com',
      balance: 20,
    },
    refreshUser: mocks.refreshUser,
  }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: mocks.showError,
    showInfo: mocks.showInfo,
    showSuccess: mocks.showSuccess,
  }),
}))

vi.mock('@/api/redeem', () => ({
  default: { redeem: vi.fn() },
}))

vi.mock('@/api/catfkStore', () => ({
  getCatfkProductImage: () => '',
  default: {
    getProducts: mocks.getProducts,
    getPaymentChannels: mocks.getPaymentChannels,
    getPriceQuote: mocks.getPriceQuote,
    createOrder: vi.fn(),
    isOrderPaid: vi.fn(),
    getOrderInfo: vi.fn(),
  },
}))

const product = {
  link: '',
  goods_type: 'card',
  goods_key: 've2u60',
  name: 'STEM中转10刀兑换码',
  price: 10,
  market_price: 10,
  description: '',
  image: '',
  category: { id: 6266, name: 'API 额度兑换码' },
  extend: {
    stock_count: 13,
    show_stock_type: 1,
    send_order: 1,
    limit_count: 1,
    query_password_status: 0,
  },
}

describe('RedeemCodeStoreView checkout payment methods', () => {
  beforeEach(() => {
    mocks.getProducts.mockReset().mockResolvedValue([product])
    mocks.getPaymentChannels.mockReset().mockResolvedValue([
      {
        id: 1,
        name: 'alipay',
        code: 'alipay',
        show_name: '支付宝（费率低）',
        status: 1,
        rate: 1.8,
        paytype: { name: 'alipay', icon: '' },
      },
      {
        id: 2,
        name: 'wechat',
        code: 'wechat',
        show_name: '微信',
        status: 1,
        rate: 2.8,
        paytype: { name: 'wechat', icon: '' },
      },
    ])
    mocks.getPriceQuote.mockReset().mockResolvedValue({
      original_amount: 10,
      total_amount: 10.18,
      fee: 0.18,
      fee_payer: 1,
      coupon_available: 0,
      coupon_price: 0,
    })
  })

  it('shows the VPN access warning before purchase content', () => {
    const wrapper = shallowMount(RedeemCodeStoreView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          BaseDialog: true,
          Icon: true,
        },
      },
    })

    const warning = wrapper.get('[data-testid="vpn-access-warning"]')
    expect(warning.attributes('role')).toBe('alert')
    const lines = warning.findAll('p')
    expect(lines).toHaveLength(3)
    expect(lines.every(line => line.text() === '必须关vpn才能访问！！！')).toBe(true)
  })

  it('shows channel names without exposing their fee rates', async () => {
    const wrapper = shallowMount(RedeemCodeStoreView, {
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /></div>',
          },
          Icon: true,
        },
      },
    })
    await flushPromises()

    await wrapper.get('[data-testid="buy-ve2u60"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="channel-1"]').text()).toBe('支付宝')
    expect(wrapper.get('[data-testid="channel-2"]').text()).toBe('微信')
    expect(wrapper.text()).not.toContain('费率')
    expect(wrapper.text()).not.toContain('1.8%')
    expect(wrapper.text()).not.toContain('2.8%')
  })
})
