<template>
  <div class="min-h-screen flex items-center justify-center px-4 py-8 md:py-12">
    <div class="w-full max-w-md card !p-6 md:!p-10 reveal in">
      <div class="font-serif text-2xl md:text-3xl font-medium tracking-tighter mb-2">找回密码</div>
      <div class="text-text-muted text-sm md:text-base mb-7">输入用户名与注册邮箱，验证后重置密码并自动登录</div>
      <form @submit.prevent="onSubmit" class="space-y-5">
        <UiField label="用户名" :error="errors.username">
          <input v-model="username" class="input" autocomplete="username" required @input="errors.username = ''">
        </UiField>
        <UiField label="邮箱" :error="errors.email">
          <input v-model="email" type="email" class="input" autocomplete="email" required @input="errors.email = ''">
        </UiField>
        <VerifyCodeField v-model="verificationCode" scene="forgot_password" :target="email" :error="errors.code" />
        <UiField label="新密码" hint="8–16 位，包含字母和数字" :error="errors.password">
          <input v-model="password" type="password" class="input" minlength="8" maxlength="16" autocomplete="new-password" required @input="errors.password = ''">
        </UiField>
        <div v-if="password" class="space-y-1">
          <div class="flex items-center gap-2">
            <div class="flex-1 h-1.5 bg-border rounded-full overflow-hidden">
              <div class="h-full transition-all duration-300" :style="{ width: strengthPct + '%', backgroundColor: strengthColor }"></div>
            </div>
            <span class="text-xs font-mono text-text-muted" :style="{ color: strengthColor }">{{ strengthLabel }}</span>
          </div>
          <div class="flex gap-3 text-xs text-text-muted">
            <span :style="{ color: pwLenOk ? strengthColor : '' }">8–16 位</span>
            <span :style="{ color: hasLetter ? strengthColor : '' }">字母</span>
            <span :style="{ color: hasDigit ? strengthColor : '' }">数字</span>
          </div>
        </div>
        <UiField label="确认新密码" :error="errors.confirm">
          <input v-model="confirmPassword" type="password" class="input" minlength="8" maxlength="16" autocomplete="new-password" required @input="errors.confirm = ''">
        </UiField>
        <button type="submit" class="btn-primary w-full" :disabled="submitting">{{ submitting ? '重置中' : '重置密码并登录' }}</button>
      </form>
      <div class="mt-6 text-center text-sm text-text-muted">想起密码了？ <router-link to="/login" class="text-text border-b border-border hover:border-text transition-colors">返回登录</router-link></div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed } from 'vue'
import { useRouter } from 'vue-router'
import UiField from '../components/UiField.vue'
import VerifyCodeField from '../components/VerifyCodeField.vue'
import { authApi, setToken } from '../api/index.js'
import { initSession, isAdmin } from '../store.js'
import { toast } from '../utils.js'

const router = useRouter()

const username = ref('')
const email = ref('')
const password = ref('')
const confirmPassword = ref('')
const verificationCode = ref('')
const submitting = ref(false)
const errors = ref({ username: '', email: '', password: '', confirm: '', code: '' })

const pwLenOk = computed(() => password.value.length >= 8 && password.value.length <= 16)
const hasLetter = computed(() => /[a-zA-Z]/.test(password.value))
const hasDigit = computed(() => /[0-9]/.test(password.value))

const strength = computed(() => {
  let s = 0
  if (pwLenOk.value) s++
  if (hasLetter.value) s++
  if (hasDigit.value) s++
  return s
})

const strengthPct = computed(() => strength.value / 3 * 100)

const strengthColor = computed(() => {
  if (strength.value <= 1) return '#9F2F2D'
  if (strength.value === 2) return '#956400'
  return '#346538'
})

const strengthLabel = computed(() => {
  if (!password.value) return ''
  if (strength.value <= 1) return '弱'
  if (strength.value === 2) return '中'
  return '强'
})

const onSubmit = async () => {
  if (submitting.value) return
  errors.value = { username: '', email: '', password: '', confirm: '', code: '' }

  if (!username.value.trim()) {
    errors.value.username = '请输入用户名'
    return
  }
  if (!email.value || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email.value)) {
    errors.value.email = '请输入有效的邮箱地址'
    return
  }
  if (password.value.length < 8 || password.value.length > 16) {
    errors.value.password = '密码必须为 8–16 位'
    return
  }
  if (!/[a-zA-Z]/.test(password.value)) {
    errors.value.password = '密码必须包含至少一个字母'
    return
  }
  if (!/[0-9]/.test(password.value)) {
    errors.value.password = '密码必须包含至少一个数字'
    return
  }
  if (password.value !== confirmPassword.value) {
    errors.value.confirm = '两次输入的新密码不一致'
    return
  }

  submitting.value = true
  try {
    const data = await authApi.forgotPassword({
      username: username.value.trim(),
      email: email.value.trim(),
      new_password: password.value,
      confirm_password: confirmPassword.value,
      verification_code: verificationCode.value || undefined,
    })
    setToken(data.token)
    await initSession(true)
    toast('密码已重置，已自动登录', 'success')
    router.push(isAdmin() ? '/admin' : '/dashboard')
  } catch (err) {
    const msg = err.message || '找回密码失败，请稍后重试'
    toast(msg, 'error')
    if (msg.includes('不匹配')) {
      errors.value.username = msg
      errors.value.email = msg
    } else if (msg.includes('验证码')) {
      errors.value.code = msg
    } else if (msg.includes('邮箱')) {
      errors.value.email = msg
    } else if (msg.includes('一致')) {
      errors.value.confirm = msg
    } else if (msg.includes('密码')) {
      errors.value.password = msg
    }
  } finally {
    submitting.value = false
  }
}
</script>
