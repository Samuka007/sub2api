import { defineComponent, h } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ForgotPasswordView from '@/views/auth/ForgotPasswordView.vue'

const {
  captchaResetMock,
  forgotPasswordMock,
  getPublicSettingsMock,
  showErrorMock,
  showSuccessMock,
  verifyActionMock
} = vi.hoisted(() => ({
  captchaResetMock: vi.fn(),
  forgotPasswordMock: vi.fn(),
  getPublicSettingsMock: vi.fn(),
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn(),
  verifyActionMock: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: showSuccessMock
  })
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    forgotPassword: (...args: unknown[]) => forgotPasswordMock(...args),
    getPublicSettings: (...args: unknown[]) => getPublicSettingsMock(...args)
  }
})

const CaptchaChallengeStub = defineComponent({
  name: 'CaptchaChallengeStub',
  emits: ['error'],
  setup(_, { expose }) {
    expose({
      verifyAction: verifyActionMock,
      reset: captchaResetMock
    })
    return () => h('div', { 'data-testid': 'captcha-challenge' })
  }
})

const actionCaptchaCases = [
  {
    provider: 'Tencent',
    settings: {
      turnstile_enabled: false,
      turnstile_site_key: '',
      tencent_captcha_enabled: true,
      tencent_captcha_app_id: 'tencent-app-id',
      aliyun_captcha_enabled: false,
      aliyun_captcha_scene_id: '',
      aliyun_captcha_prefix: ''
    },
    proofs: [
      {
        result: { token: 'ticket-1', randstr: '@rand-1' },
        request: {
          tencent_captcha_ticket: 'ticket-1',
          tencent_captcha_randstr: '@rand-1'
        }
      },
      {
        result: { token: 'ticket-2', randstr: '@rand-2' },
        request: {
          tencent_captcha_ticket: 'ticket-2',
          tencent_captcha_randstr: '@rand-2'
        }
      }
    ],
    inactiveFields: ['turnstile_token']
  },
  {
    provider: 'Aliyun',
    settings: {
      turnstile_enabled: false,
      turnstile_site_key: '',
      tencent_captcha_enabled: false,
      tencent_captcha_app_id: '',
      aliyun_captcha_enabled: true,
      aliyun_captcha_scene_id: 'scene-id',
      aliyun_captcha_prefix: 'prefix-id',
      aliyun_captcha_region: 'sgp'
    },
    proofs: [
      {
        result: { token: 'aliyun-proof-1', randstr: '' },
        request: { turnstile_token: 'aliyun-proof-1' }
      },
      {
        result: { token: 'aliyun-proof-2', randstr: '' },
        request: { turnstile_token: 'aliyun-proof-2' }
      }
    ],
    inactiveFields: ['tencent_captcha_ticket', 'tencent_captcha_randstr']
  }
] as const

function mountForgotPassword() {
  return mount(ForgotPasswordView, {
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        Icon: true,
        RouterLink: true,
        TurnstileWidget: CaptchaChallengeStub
      }
    }
  })
}

async function mountActionForgotPassword(
  settings: (typeof actionCaptchaCases)[number]['settings']
) {
  getPublicSettingsMock.mockResolvedValueOnce(settings)
  const wrapper = mountForgotPassword()
  await flushPromises()
  return wrapper
}

async function submitValidEmail(wrapper: VueWrapper) {
  await wrapper.get('#email').setValue('user@example.com')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

beforeEach(() => {
  captchaResetMock.mockReset()
  forgotPasswordMock.mockReset()
  getPublicSettingsMock.mockReset()
  showErrorMock.mockReset()
  showSuccessMock.mockReset()
  verifyActionMock.mockReset()
  forgotPasswordMock.mockResolvedValue({ message: 'sent' })
})

describe.each(actionCaptchaCases)('ForgotPasswordView $provider action captcha', ({
  settings,
  proofs,
  inactiveFields
}) => {
  it('validates the email before opening the captcha', async () => {
    const wrapper = await mountActionForgotPassword(settings)

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).not.toHaveBeenCalled()
    expect(forgotPasswordMock).not.toHaveBeenCalled()
  })

  it('does not request a reset when the captcha is cancelled or reports an error', async () => {
    verifyActionMock.mockResolvedValue(null)
    const wrapper = await mountActionForgotPassword(settings)
    await wrapper.get('#email').setValue('user@example.com')

    await wrapper.get('form').trigger('submit')
    await flushPromises()
    wrapper.getComponent(CaptchaChallengeStub).vm.$emit('error')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledTimes(2)
    expect(forgotPasswordMock).not.toHaveBeenCalled()
  })

  it('submits only the provider proof and displays the success state', async () => {
    verifyActionMock.mockResolvedValue(proofs[0].result)
    const wrapper = await mountActionForgotPassword(settings)

    await submitValidEmail(wrapper)
    expect(verifyActionMock).toHaveBeenCalledOnce()

    const request = forgotPasswordMock.mock.calls[0]?.[0] as Record<string, unknown>
    expect(request).toEqual(expect.objectContaining({
      email: 'user@example.com',
      ...proofs[0].request
    }))
    for (const field of inactiveFields) {
      expect(request[field]).toBeUndefined()
    }
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.text()).toContain('auth.resetEmailSent')
    expect(showSuccessMock).toHaveBeenCalledWith('auth.resetEmailSent')
    expect(captchaResetMock).toHaveBeenCalledOnce()
  })

  it('keeps the form after failure, resets, and uses a fresh proof on retry', async () => {
    forgotPasswordMock
      .mockRejectedValueOnce(new Error('delivery failed'))
      .mockResolvedValueOnce({ message: 'sent' })
    verifyActionMock
      .mockResolvedValueOnce(proofs[0].result)
      .mockResolvedValueOnce(proofs[1].result)
    const wrapper = await mountActionForgotPassword(settings)

    await submitValidEmail(wrapper)
    expect(wrapper.find('form').exists()).toBe(true)
    expect(showErrorMock).toHaveBeenCalledWith('delivery failed')
    expect(captchaResetMock).toHaveBeenCalledOnce()
    expect(showSuccessMock).not.toHaveBeenCalled()

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledTimes(2)
    expect(forgotPasswordMock).toHaveBeenCalledTimes(2)
    expect(forgotPasswordMock.mock.calls[0]?.[0]).toEqual(expect.objectContaining(proofs[0].request))
    expect(forgotPasswordMock.mock.calls[1]?.[0]).toEqual(expect.objectContaining(proofs[1].request))
    expect(captchaResetMock).toHaveBeenCalledTimes(2)
    expect(wrapper.find('form').exists()).toBe(false)
    expect(wrapper.text()).toContain('auth.resetEmailSent')
  })
})
