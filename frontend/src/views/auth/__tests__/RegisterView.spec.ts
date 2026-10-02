import { defineComponent, h } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RegisterView from '@/views/auth/RegisterView.vue'

const {
  captchaResetMock,
  getPublicSettingsMock,
  registerMock,
  showErrorMock,
  showSuccessMock,
  verifyActionMock,
  routerPush,
  appStoreMock
} = vi.hoisted(() => {
  const routerPush = vi.fn()
  return {
    captchaResetMock: vi.fn(),
    getPublicSettingsMock: vi.fn(),
    registerMock: vi.fn(),
    showErrorMock: vi.fn(),
    showSuccessMock: vi.fn(),
    verifyActionMock: vi.fn(),
    routerPush,
    appStoreMock: {
      cachedPublicSettings: null as { promo_code_enabled?: boolean } | null,
      showError: (...args: unknown[]) => showErrorMock(...args),
      showSuccess: (...args: unknown[]) => showSuccessMock(...args),
      showWarning: vi.fn()
    }
  }
})

const publicSettings = {
  registration_enabled: true,
  email_verify_enabled: false,
  promo_code_enabled: false,
  invitation_code_enabled: false,
  affiliate_enabled: true,
  turnstile_enabled: true,
  turnstile_site_key: 'site-key',
  site_name: 'Sub2API',
  registration_email_suffix_whitelist: [],
  linuxdo_oauth_enabled: false,
  wechat_oauth_enabled: false,
  oidc_oauth_enabled: false,
  github_oauth_enabled: false,
  google_oauth_enabled: false
}

vi.mock('vue-router', () => ({
  useRouter: () => ({ push: routerPush }),
  useRoute: () => ({ query: {} })
}))

vi.mock('vue-i18n', () => ({
  createI18n: () => ({
    global: {
      t: (key: string) => key
    }
  }),
  useI18n: () => ({
    t: (key: string) =>
      key === 'auth.emailDomainRegistrationLimit'
        ? '该邮箱域名无法注册新账户。请使用主流邮箱注册；如需使用企业邮箱，请联系客服添加域名白名单。'
        : key,
    locale: { value: 'en' }
  })
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({ register: (...args: unknown[]) => registerMock(...args) }),
  useAppStore: () => appStoreMock
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
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
      ...publicSettings,
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
      ...publicSettings,
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

function mountRegister() {
  return mount(RegisterView, {
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        Icon: true,
        TurnstileWidget: CaptchaChallengeStub,
        LoginAgreementPrompt: true,
        EmailOAuthButtons: true,
        LinuxDoOAuthSection: true,
        WechatOAuthSection: true,
        OidcOAuthSection: true,
        RouterLink: true,
        transition: false
      }
    }
  })
}

async function mountActionRegister(settings: (typeof actionCaptchaCases)[number]['settings']) {
  getPublicSettingsMock.mockResolvedValueOnce(settings)
  const wrapper = mountRegister()
  await flushPromises()
  return wrapper
}

async function submitValidRegistration(wrapper: VueWrapper) {
  await wrapper.get('#email').setValue('user@example.com')
  await wrapper.get('#password').setValue('secret-123')
  await wrapper.get('#confirmPassword').setValue('secret-123')
  await wrapper.get('form').trigger('submit')
  await flushPromises()
}

beforeEach(() => {
  captchaResetMock.mockReset()
  getPublicSettingsMock.mockReset()
  registerMock.mockReset()
  showErrorMock.mockReset()
  showSuccessMock.mockReset()
  verifyActionMock.mockReset()
  routerPush.mockReset()
  appStoreMock.cachedPublicSettings = null
  sessionStorage.clear()
  localStorage.clear()
  sessionStorage.removeItem('register_data')
  verifyActionMock.mockResolvedValue({ token: 'ticket', randstr: 'randstr' })
  getPublicSettingsMock.mockResolvedValue(publicSettings)
  registerMock.mockResolvedValue({})
  routerPush.mockResolvedValue(undefined)
})

describe('RegisterView', () => {
  it('does not flash the promo-code field before disabled settings finish loading', async () => {
    let resolveSettings!: (settings: typeof publicSettings) => void
    getPublicSettingsMock.mockReturnValueOnce(
      new Promise<typeof publicSettings>((resolve) => {
        resolveSettings = resolve
      })
    )

    const wrapper = mountRegister()

    expect(wrapper.find('#promo_code').exists()).toBe(false)

    resolveSettings(publicSettings)
    await flushPromises()

    expect(wrapper.find('#promo_code').exists()).toBe(false)
  })

  it('uses injected public settings to show an enabled promo-code field on first render', () => {
    appStoreMock.cachedPublicSettings = { promo_code_enabled: true }
    getPublicSettingsMock.mockReturnValueOnce(new Promise(() => {}))

    const wrapper = mountRegister()

    expect(wrapper.find('#promo_code').exists()).toBe(true)
  })

  it.each([
    ['', 'auth.confirmPasswordRequired'],
    ['different-password', 'auth.passwordsDoNotMatch']
  ])('blocks invalid confirmation %j before captcha and allows correction', async (confirmation, error) => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      tencent_captcha_enabled: true,
      tencent_captcha_app_id: 'app-id'
    })
    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue(confirmation)
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(showErrorMock).toHaveBeenCalledWith(error)
    expect(wrapper.get('#confirmPassword').classes()).toContain('input-error')
    expect(registerMock).not.toHaveBeenCalled()
    expect(verifyActionMock).not.toHaveBeenCalled()
    expect(routerPush).not.toHaveBeenCalled()
    expect(sessionStorage.getItem('register_data')).toBeNull()

    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(wrapper.get('#confirmPassword').classes()).not.toContain('input-error')
    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(registerMock).toHaveBeenCalledWith({
      email: 'user@example.com',
      password: 'secret-123',
      turnstile_token: undefined,
      tencent_captcha_ticket: 'ticket',
      tencent_captcha_randstr: 'randstr',
      promo_code: undefined,
      invitation_code: undefined
    })
    expect(routerPush).toHaveBeenCalledWith('/dashboard')
  })

  it('requires matching confirmation before storing only the registration fields for email verification', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      email_verify_enabled: true
    })
    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('different-password')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(sessionStorage.getItem('register_data')).toBeNull()
    expect(routerPush).not.toHaveBeenCalled()

    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(JSON.parse(sessionStorage.getItem('register_data')!)).toEqual({
      email: 'user@example.com',
      password: 'secret-123'
    })
    expect(routerPush).toHaveBeenCalledWith('/email-verify')
    expect(registerMock).not.toHaveBeenCalled()
  })

  it('keeps the optional affiliate invitation field before Turnstile', async () => {
    const wrapper = mountRegister()
    await flushPromises()

    const invitationField = wrapper.get('[data-testid="affiliate-invitation-field"]')
    const turnstile = wrapper.get('[data-testid="registration-turnstile"]')

    expect(invitationField.get('input').attributes('id')).toBe('affiliate_code')
    expect(invitationField.text()).toContain('common.optional')
    expect(
      invitationField.element.compareDocumentPosition(turnstile.element) &
        Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy()
  })

  it('uses the mandatory invitation field without duplicating the affiliate field', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      invitation_code_enabled: true
    })

    const wrapper = mountRegister()
    await flushPromises()

    expect(wrapper.find('[data-testid="affiliate-invitation-field"]').exists()).toBe(false)
    expect(wrapper.get('#invitation_code').exists()).toBe(true)
  })

  it('submits a non-whitelist email domain so the backend can enforce its registration quota', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      registration_email_suffix_whitelist: ['allowed.com'],
      registration_email_domain_quota_enabled: true
    })

    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('first@custom.example')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).toHaveBeenCalledWith(
      expect.objectContaining({ email: 'first@custom.example' })
    )
    expect(showErrorMock).not.toHaveBeenCalled()
  })

  it('shows the localized registration domain quota message returned by the backend', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      registration_email_suffix_whitelist: ['allowed.com'],
      registration_email_domain_quota_enabled: true
    })
    registerMock.mockRejectedValueOnce({
      reason: 'EMAIL_DOMAIN_REGISTRATION_LIMIT',
      message: 'raw backend message'
    })

    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('second@custom.example')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(showErrorMock).toHaveBeenCalledWith(
      '该邮箱域名无法注册新账户。请使用主流邮箱注册；如需使用企业邮箱，请联系客服添加域名白名单。'
    )
  })

  // 域名限量注册开关默认关闭：恢复 PR5423 之前的客户端白名单预检，非白名单域名不发起注册请求。
  it('rejects a non-whitelist email domain locally when the domain quota switch is disabled', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      registration_email_suffix_whitelist: ['allowed.com']
    })

    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('first@custom.example')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).not.toHaveBeenCalled()
    // 校验失败通过 validationToastMessage watcher 弹 toast
    expect(showErrorMock).toHaveBeenCalledWith('auth.emailSuffixNotAllowedWithAllowed')
    expect(wrapper.get('#email').classes()).toContain('input-error')
  })

  it('still submits whitelisted email domains when the domain quota switch is disabled', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      ...publicSettings,
      turnstile_enabled: false,
      registration_email_suffix_whitelist: ['allowed.com']
    })

    const wrapper = mountRegister()
    await flushPromises()
    await wrapper.get('#email').setValue('user@allowed.com')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).toHaveBeenCalledWith(
      expect.objectContaining({ email: 'user@allowed.com' })
    )
    expect(showErrorMock).not.toHaveBeenCalled()
  })
})

describe.each(actionCaptchaCases)('RegisterView $provider action captcha', ({
  settings,
  proofs,
  inactiveFields
}) => {
  it('validates the form before opening the captcha', async () => {
    const wrapper = await mountActionRegister(settings)

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).not.toHaveBeenCalled()
    expect(registerMock).not.toHaveBeenCalled()
    expect(sessionStorage.getItem('register_data')).toBeNull()
    expect(routerPush).not.toHaveBeenCalled()
  })

  it('does not register when the captcha is cancelled or reports an error', async () => {
    verifyActionMock.mockResolvedValue(null)
    const wrapper = await mountActionRegister(settings)
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')
    await wrapper.get('#confirmPassword').setValue('secret-123')

    await wrapper.get('form').trigger('submit')
    await flushPromises()
    wrapper.getComponent(CaptchaChallengeStub).vm.$emit('error')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledTimes(2)
    expect(registerMock).not.toHaveBeenCalled()
    expect(sessionStorage.getItem('register_data')).toBeNull()
    expect(routerPush).not.toHaveBeenCalled()
  })

  it('submits only the provider proof when registering directly', async () => {
    verifyActionMock.mockResolvedValue(proofs[0].result)
    const wrapper = await mountActionRegister(settings)

    await submitValidRegistration(wrapper)
    expect(verifyActionMock).toHaveBeenCalledOnce()

    const request = registerMock.mock.calls[0]?.[0] as Record<string, unknown>
    expect(request).toEqual(expect.objectContaining({
      email: 'user@example.com',
      password: 'secret-123',
      ...proofs[0].request
    }))
    for (const field of inactiveFields) {
      expect(request[field]).toBeUndefined()
    }
    expect(routerPush).toHaveBeenCalledWith('/dashboard')
    expect(showSuccessMock).toHaveBeenCalledWith('auth.accountCreatedSuccess')
    expect(captchaResetMock).toHaveBeenCalledOnce()
  })

  it('stores only the provider proof before continuing to email verification', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({ ...settings, email_verify_enabled: true })
    verifyActionMock.mockResolvedValue(proofs[0].result)
    const wrapper = mountRegister()
    await flushPromises()

    await submitValidRegistration(wrapper)
    expect(verifyActionMock).toHaveBeenCalledOnce()

    expect(registerMock).not.toHaveBeenCalled()
    expect(JSON.parse(sessionStorage.getItem('register_data') || '{}')).toEqual({
      email: 'user@example.com',
      password: 'secret-123',
      ...proofs[0].request
    })
    expect(routerPush).toHaveBeenCalledWith('/email-verify')
    expect(captchaResetMock).toHaveBeenCalledOnce()
  })

  it('resets a failed request and uses a fresh proof on retry', async () => {
    registerMock
      .mockRejectedValueOnce(new Error('registration failed'))
      .mockResolvedValueOnce({})
    verifyActionMock
      .mockResolvedValueOnce(proofs[0].result)
      .mockResolvedValueOnce(proofs[1].result)
    const wrapper = await mountActionRegister(settings)

    await submitValidRegistration(wrapper)
    expect(showErrorMock).toHaveBeenCalledWith('registration failed')
    expect(captchaResetMock).toHaveBeenCalledOnce()
    expect(showSuccessMock).not.toHaveBeenCalled()
    expect(routerPush).not.toHaveBeenCalled()

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledTimes(2)
    expect(registerMock).toHaveBeenCalledTimes(2)
    expect(registerMock.mock.calls[0]?.[0]).toEqual(expect.objectContaining(proofs[0].request))
    expect(registerMock.mock.calls[1]?.[0]).toEqual(expect.objectContaining(proofs[1].request))
    expect(captchaResetMock).toHaveBeenCalledTimes(2)
    expect(routerPush).toHaveBeenCalledWith('/dashboard')
  })
})
