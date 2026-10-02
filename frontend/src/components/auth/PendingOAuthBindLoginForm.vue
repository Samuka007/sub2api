<template>
  <form class="space-y-3" @submit.prevent="handleSubmit">
    <input
      v-model="email"
      :data-testid="`${testIdPrefix}-bind-login-email`"
      type="email"
      class="input w-full"
      :placeholder="t('auth.emailPlaceholder')"
      :disabled="isSubmitting"
    />
    <input
      v-model="password"
      :data-testid="`${testIdPrefix}-bind-login-password`"
      type="password"
      class="input w-full"
      :placeholder="t('auth.passwordPlaceholder')"
      :disabled="isSubmitting"
    />
    <CaptchaChallenge
      v-if="captchaEnabled"
      ref="captchaRef"
      :site-key="turnstileSiteKey"
      :turnstile-enabled="turnstileEnabled"
      :turnstile-site-key="turnstileSiteKey"
      :tencent-enabled="tencentCaptchaEnabled"
      :tencent-app-id="tencentCaptchaAppId"
      :aliyun-enabled="aliyunCaptchaEnabled"
      :aliyun-scene-id="aliyunCaptchaSceneId"
      :aliyun-prefix="aliyunCaptchaPrefix"
      :aliyun-region="aliyunCaptchaRegion"
      @verify="onVerify"
      @expire="resetCaptcha"
      @error="resetCaptcha"
    />
    <button
      :data-testid="`${testIdPrefix}-bind-login-submit`"
      type="submit"
      class="btn btn-primary w-full"
      :disabled="isSubmitting || !email.trim() || !password || (turnstileEnabled && !captchaToken)"
      @click.prevent="handleSubmit"
    >
      {{ isSubmitting ? t('common.processing') : t('auth.oauthFlow.logInAndBind') }}
    </button>
    <button
      v-if="canReturnToCreateAccount"
      type="button"
      class="btn btn-secondary w-full"
      :disabled="isSubmitting"
      @click="emit('switchToCreate', email.trim())"
    >
      {{ t('auth.oauthFlow.useDifferentEmail') }}
    </button>
  </form>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import CaptchaChallenge from '@/components/CaptchaChallenge.vue'
import { getPublicSettings } from '@/api/auth'
import { useAppStore } from '@/stores'

export type PendingOAuthBindLoginPayload = {
  email: string
  password: string
  turnstileToken?: string
  tencentCaptchaTicket?: string
  tencentCaptchaRandstr?: string
}

const props = defineProps<{
  initialEmail: string
  testIdPrefix: string
  isSubmitting: boolean
  canReturnToCreateAccount: boolean
  errorMessage?: string
}>()

const emit = defineEmits<{
  submit: [payload: PendingOAuthBindLoginPayload]
  switchToCreate: [email: string]
}>()

const { t } = useI18n()
const appStore = useAppStore()
const email = ref('')
const password = ref('')
const turnstileEnabled = ref(false)
const turnstileSiteKey = ref('')
const tencentCaptchaEnabled = ref(false)
const tencentCaptchaAppId = ref('')
const aliyunCaptchaEnabled = ref(false)
const aliyunCaptchaSceneId = ref('')
const aliyunCaptchaPrefix = ref('')
const aliyunCaptchaRegion = ref('cn')
const captchaToken = ref('')
const captchaRandstr = ref('')
const captchaRef = ref<InstanceType<typeof CaptchaChallenge> | null>(null)

const actionCaptchaEnabled = computed(
  () =>
    (tencentCaptchaEnabled.value && Boolean(tencentCaptchaAppId.value)) ||
    (aliyunCaptchaEnabled.value &&
      Boolean(aliyunCaptchaSceneId.value) &&
      Boolean(aliyunCaptchaPrefix.value))
)
const captchaEnabled = computed(
  () =>
    (turnstileEnabled.value && Boolean(turnstileSiteKey.value)) || actionCaptchaEnabled.value
)

watch(
  () => props.initialEmail,
  value => {
    email.value = value || ''
  },
  { immediate: true }
)

watch(
  () => props.errorMessage,
  value => {
    if (value && captchaEnabled.value) {
      resetCaptcha()
    }
  }
)

function onVerify(token: string, randstr = '') {
  captchaToken.value = token
  captchaRandstr.value = randstr
}

function resetCaptcha() {
  captchaToken.value = ''
  captchaRandstr.value = ''
  captchaRef.value?.reset()
}

async function handleSubmit() {
  const trimmedEmail = email.value.trim()
  if (!trimmedEmail || !password.value) return

  if (turnstileEnabled.value && !captchaToken.value) {
    appStore.showError(t('auth.completeVerification'))
    return
  }
  if (actionCaptchaEnabled.value) {
    const proof = await captchaRef.value?.verifyAction()
    if (!proof) return
    captchaToken.value = proof.token
    captchaRandstr.value = proof.randstr
  }

  emit('submit', {
    email: trimmedEmail,
    password: password.value,
    ...((turnstileEnabled.value || aliyunCaptchaEnabled.value) && captchaToken.value
      ? { turnstileToken: captchaToken.value }
      : {}),
    ...(tencentCaptchaEnabled.value && captchaToken.value
      ? {
          tencentCaptchaTicket: captchaToken.value,
          tencentCaptchaRandstr: captchaRandstr.value
        }
      : {})
  })

  if (actionCaptchaEnabled.value) {
    resetCaptcha()
  }
}

onMounted(async () => {
  try {
    const settings = await getPublicSettings()
    turnstileEnabled.value = settings.turnstile_enabled === true
    turnstileSiteKey.value = settings.turnstile_site_key || ''
    tencentCaptchaEnabled.value = settings.tencent_captcha_enabled === true
    tencentCaptchaAppId.value = settings.tencent_captcha_app_id || ''
    aliyunCaptchaEnabled.value = settings.aliyun_captcha_enabled === true
    aliyunCaptchaSceneId.value = settings.aliyun_captcha_scene_id || ''
    aliyunCaptchaPrefix.value = settings.aliyun_captcha_prefix || ''
    aliyunCaptchaRegion.value = settings.aliyun_captcha_region || 'cn'
  } catch {
    turnstileEnabled.value = false
    turnstileSiteKey.value = ''
    tencentCaptchaEnabled.value = false
    tencentCaptchaAppId.value = ''
    aliyunCaptchaEnabled.value = false
    aliyunCaptchaSceneId.value = ''
    aliyunCaptchaPrefix.value = ''
    aliyunCaptchaRegion.value = 'cn'
  }
})
</script>
