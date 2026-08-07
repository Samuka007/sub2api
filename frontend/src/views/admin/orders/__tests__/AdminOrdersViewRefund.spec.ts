import { defineComponent, h, type PropType } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AdminOrdersView from '@/views/admin/orders/AdminOrdersView.vue'
import type { PaymentOrder } from '@/types/payment'

const getOrdersMock = vi.fn()
const refundOrderMock = vi.fn()
const queryRefundMock = vi.fn()
const showSuccessMock = vi.fn()
const showErrorMock = vi.fn()

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: showSuccessMock, showError: showErrorMock })
}))

vi.mock('@/api/admin/payment', () => {
  const api = {
    getOrders: (...args: unknown[]) => getOrdersMock(...args),
    refundOrder: (...args: unknown[]) => refundOrderMock(...args),
    queryRefund: (...args: unknown[]) => queryRefundMock(...args)
  }
  return { adminPaymentAPI: api, default: api }
})

const OrderTableStub = defineComponent({
  props: {
    orders: { type: Array as PropType<PaymentOrder[]>, default: () => [] }
  },
  setup(props, { slots }) {
    return () => h('div', props.orders.flatMap((row) => slots.actions?.({ row }) || []))
  }
})

const RefundDialogStub = defineComponent({
  props: {
    show: Boolean,
    requireForce: Boolean,
    warning: { type: String, default: '' }
  },
  emits: ['confirm'],
  setup(props, { emit }) {
    const payload = (force: boolean) => ({ amount: 10, reason: 'admin refund', deduct_balance: true, force })
    return () => props.show
      ? h('div', { 'data-testid': 'refund-dialog' }, [
          h('span', { 'data-testid': 'refund-warning' }, props.warning),
          h('span', { 'data-testid': 'require-force' }, String(props.requireForce)),
          h('button', { 'data-testid': 'refund-confirm', onClick: () => emit('confirm', payload(false)) }, 'confirm'),
          props.requireForce
            ? h('button', { 'data-testid': 'refund-force-confirm', onClick: () => emit('confirm', payload(true)) }, 'force')
            : null
        ])
      : null
  }
})

const order = {
  id: 42,
  status: 'COMPLETED',
  amount: 10,
  pay_amount: 10,
  currency: 'USD'
} as PaymentOrder

function mountView() {
  return mount(AdminOrdersView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        OrderTable: OrderTableStub,
        AdminRefundDialog: RefundDialogStub,
        Pagination: true,
        BaseDialog: true,
        Select: true,
        Icon: true,
        OrderStatusBadge: true
      }
    }
  })
}

describe('AdminOrdersView force refund transition', () => {
  beforeEach(() => {
    getOrdersMock.mockReset().mockResolvedValue({ data: { items: [order], total: 1 } })
    refundOrderMock.mockReset()
    queryRefundMock.mockReset()
    showSuccessMock.mockReset()
    showErrorMock.mockReset()
  })

  it('keeps the dialog open, requires force, then closes and refreshes after success', async () => {
    refundOrderMock
      .mockResolvedValueOnce({ data: { success: false, require_force: true, warning: 'balance changed' } })
      .mockResolvedValueOnce({ data: { success: true } })

    const wrapper = mountView()
    await flushPromises()
    const refundButton = wrapper.findAll('button').find((button) => button.text() === 'payment.admin.refund')
    expect(refundButton).toBeDefined()
    await refundButton!.trigger('click')

    await wrapper.get('[data-testid="refund-confirm"]').trigger('click')
    await flushPromises()
    expect(refundOrderMock).toHaveBeenNthCalledWith(1, 42, {
      amount: 10,
      reason: 'admin refund',
      deduct_balance: true,
      force: false
    })
    expect(wrapper.get('[data-testid="refund-dialog"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="require-force"]').text()).toBe('true')
    expect(wrapper.get('[data-testid="refund-warning"]').text()).toBe('balance changed')

    await wrapper.get('[data-testid="refund-force-confirm"]').trigger('click')
    await flushPromises()
    expect(refundOrderMock).toHaveBeenNthCalledWith(2, 42, {
      amount: 10,
      reason: 'admin refund',
      deduct_balance: true,
      force: true
    })
    expect(wrapper.find('[data-testid="refund-dialog"]').exists()).toBe(false)
    expect(showSuccessMock).toHaveBeenCalledWith('payment.admin.refundSuccess')
    expect(getOrdersMock).toHaveBeenCalledTimes(2)
  })

  it('queries an in-progress refund instead of offering another gateway refund', async () => {
    getOrdersMock.mockResolvedValue({ data: { items: [{ ...order, status: 'REFUNDING' }], total: 1 } })
    queryRefundMock.mockResolvedValue({ data: { success: true } })

    const wrapper = mountView()
    await flushPromises()
    const queryButton = wrapper.findAll('button').find((button) => button.text() === 'payment.admin.queryRefundStatus')
    expect(queryButton).toBeDefined()
    await queryButton!.trigger('click')
    await flushPromises()

    expect(queryRefundMock).toHaveBeenCalledWith(42)
    expect(refundOrderMock).not.toHaveBeenCalled()
    expect(showSuccessMock).toHaveBeenCalledWith('payment.admin.refundSuccess')
  })

  it('opens force confirmation when pending finalization reports insufficient balance', async () => {
    getOrdersMock.mockResolvedValue({ data: { items: [{ ...order, status: 'REFUND_PENDING' }], total: 1 } })
	queryRefundMock
		.mockResolvedValueOnce({ data: { success: false, require_force: true, warning: 'balance changed' } })
		.mockResolvedValueOnce({ data: { success: true } })

    const wrapper = mountView()
    await flushPromises()
    const queryButton = wrapper.findAll('button').find((button) => button.text() === 'payment.admin.queryRefundStatus')
    expect(queryButton).toBeDefined()
    await queryButton!.trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-testid="refund-dialog"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="require-force"]').text()).toBe('true')
    expect(wrapper.get('[data-testid="refund-warning"]').text()).toBe('balance changed')
    await wrapper.get('[data-testid="refund-force-confirm"]').trigger('click')
    await flushPromises()
	expect(queryRefundMock).toHaveBeenNthCalledWith(2, 42, { force: true })
	expect(refundOrderMock).not.toHaveBeenCalled()
	expect(wrapper.find('[data-testid="refund-dialog"]').exists()).toBe(false)
	expect(showSuccessMock).toHaveBeenCalledWith('payment.admin.refundSuccess')
  })

  it('uses the structured pending state instead of parsing warning text', async () => {
    getOrdersMock.mockResolvedValue({ data: { items: [{ ...order, status: 'REFUND_PENDING' }], total: 1 } })
    queryRefundMock.mockResolvedValue({ data: { success: false, state: 'pending', warning: 'awaiting gateway confirmation' } })

    const wrapper = mountView()
    await flushPromises()
    const queryButton = wrapper.findAll('button').find((button) => button.text() === 'payment.admin.queryRefundStatus')
    await queryButton!.trigger('click')
    await flushPromises()

    expect(showSuccessMock).toHaveBeenCalledWith('payment.admin.refundPending')
    expect(showErrorMock).not.toHaveBeenCalled()
  })

  it('does not infer pending state from warning text', async () => {
    getOrdersMock.mockResolvedValue({ data: { items: [{ ...order, status: 'REFUND_PENDING' }], total: 1 } })
    queryRefundMock.mockResolvedValue({ data: { success: false, warning: 'gateway refund is pending' } })

    const wrapper = mountView()
    await flushPromises()
    const queryButton = wrapper.findAll('button').find((button) => button.text() === 'payment.admin.queryRefundStatus')
    await queryButton!.trigger('click')
    await flushPromises()

    expect(showSuccessMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('gateway refund is pending')
  })
})
