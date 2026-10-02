import { defineComponent, h } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import PendingOAuthBindLoginForm from '../PendingOAuthBindLoginForm.vue'

const getPublicSettings = vi.fn()
const showError = vi.fn()
const reset = vi.fn()
const verifyAction = vi.fn()

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return { ...actual, getPublicSettings: (...args: any[]) => getPublicSettings(...args) }
})

vi.mock('@/stores', () => ({ useAppStore: () => ({ showError }) }))

const CaptchaChallengeStub = defineComponent({
  setup(_, { expose }) {
    expose({ verifyAction, reset })
    return () => h('div')
  }
})

function mountForm() {
  return mount(PendingOAuthBindLoginForm, {
    props: {
      initialEmail: 'existing@example.com',
      testIdPrefix: 'oidc',
      isSubmitting: false,
      canReturnToCreateAccount: true
    },
    global: { stubs: { CaptchaChallenge: CaptchaChallengeStub } }
  })
}

describe('PendingOAuthBindLoginForm', () => {
  beforeEach(() => {
    getPublicSettings.mockReset()
    showError.mockReset()
    reset.mockReset()
    verifyAction.mockReset()
  })

  it.each([
    {
      provider: 'Tencent',
      settings: { tencent_captcha_enabled: true, tencent_captcha_app_id: 'app-id' },
      proof: { token: 'ticket', randstr: '@rand' },
      expected: { tencentCaptchaTicket: 'ticket', tencentCaptchaRandstr: '@rand' }
    },
    {
      provider: 'Aliyun',
      settings: {
        aliyun_captcha_enabled: true,
        aliyun_captcha_scene_id: 'scene',
        aliyun_captcha_prefix: 'prefix'
      },
      proof: { token: 'verify-param', randstr: '' },
      expected: { turnstileToken: 'verify-param' }
    }
  ])('acquires a fresh $provider proof before bind login', async ({ settings, proof, expected }) => {
    getPublicSettings.mockResolvedValue({ turnstile_enabled: false, ...settings })
    verifyAction.mockResolvedValue(proof)
    const wrapper = mountForm()
    await flushPromises()

    await wrapper.get('[data-testid="oidc-bind-login-password"]').setValue('secret-password')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(verifyAction).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('submit')?.[0]?.[0]).toMatchObject({
      email: 'existing@example.com',
      password: 'secret-password',
      ...expected
    })
  })
})
