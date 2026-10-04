<template>
  <div class="min-h-screen flex items-center justify-center px-4 py-8 md:py-12">
    <div class="w-full max-w-md card !p-6 md:!p-10 reveal in">
      <div class="font-serif text-2xl md:text-3xl font-medium tracking-tighter mb-2">登录</div>
      <div class="text-text-muted text-sm md:text-base mb-7">使用账号或邮箱登录 LoomProxy 控制台</div>
      <form @submit.prevent="onSubmit" class="space-y-5">
        <UiField label="用户名 / 邮箱">
          <input v-model="username" class="input" autocomplete="username" required>
        </UiField>
        <UiField label="密码">
          <input v-model="password" type="password" class="input" autocomplete="current-password" required>
        </UiField>
        <button type="submit" class="btn-primary w-full" :disabled="submitting">{{ submitting ? '登录中' : '登录' }}</button>
      </form>
      <div class="mt-6 flex items-center justify-between text-sm text-text-muted">
        <span>还没有账号？ <router-link to="/register" class="text-text border-b border-border hover:border-text transition-colors">注册新用户</router-link></span>
        <router-link to="/forgot-password" class="text-text border-b border-border hover:border-text transition-colors">忘记密码</router-link>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import UiField from '../components/UiField.vue'
import { authApi, setToken } from '../api/index.js'
import { initSession, isAdmin } from '../store.js'
import { toast } from '../utils.js'

const router = useRouter()
const username = ref('')
const password = ref('')
const submitting = ref(false)

const onSubmit = async () => {
  if (submitting.value) return
  submitting.value = true
  try {
    const data = await authApi.login(username.value, password.value)
    setToken(data.token)
    await initSession(true)
    toast('登录成功', 'success')
    // 后端在登录响应里算好的安全提示（P44：上次登录以来有几台设备被移出）不能被丢掉——
    // 它是那句「如非本人操作，请尽快修改密码」到达使用者的唯一一条路。
    // 比一般 toast 留更久的时间：这条要让人读完，不是让人瞥见。
    if (data && data.notice) toast(data.notice, 'info', 10000)
    router.push(isAdmin() ? '/admin' : '/dashboard')
  } catch (err) {
    toast(err.message, 'error')
  } finally {
    submitting.value = false
  }
}
</script>
