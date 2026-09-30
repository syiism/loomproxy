<template>
  <UiField v-if="enabled" label="验证码" :error="error" hint="请先填写上方邮箱再发送验证码">
    <div class="flex gap-2">
      <input
        :value="modelValue"
        @input="$emit('update:modelValue', $event.target.value.replace(/\D/g, ''))"
        class="input flex-1 font-mono"
        maxlength="6"
        inputmode="numeric"
        autocomplete="one-time-code"
        placeholder="6 位数字"
      >
      <button type="button" class="btn-ghost whitespace-nowrap" :disabled="!canSend" @click="send">
        {{ counting > 0 ? counting + 's 后重发' : (sending ? '发送中…' : '发送验证码') }}
      </button>
    </div>
  </UiField>
</template>

<script setup>
import { ref, computed, onMounted } from 'vue'
import UiField from './UiField.vue'
import { authApi } from '../api/index.js'
import { toast } from '../utils.js'

// 场景开关由后端 verify_code_scenes 控制：未启用时本组件不渲染，页面无感知。
// 配置模块级缓存：注册/找回密码页共享一次 /verify/config 请求。
let configPromise = null
const getConfig = () => {
  if (!configPromise) configPromise = authApi.verifyConfig().catch(() => null)
  return configPromise
}

const props = defineProps({
  scene: { type: String, required: true },
  target: { type: String, default: '' },
  modelValue: { type: String, default: '' },
  error: { type: String, default: '' },
})
const emit = defineEmits(['update:modelValue', 'sent'])

const enabled = ref(false)
const sending = ref(false)
const counting = ref(0)

onMounted(async () => {
  const cfg = await getConfig()
  enabled.value = !!(cfg && (cfg.scenes || []).includes(props.scene))
})

const canSend = computed(() =>
  !sending.value && counting.value <= 0 && /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(props.target.trim())
)

const send = async () => {
  if (!canSend.value || sending.value) return
  sending.value = true
  try {
    await authApi.sendVerifyCode(props.scene, props.target.trim())
    toast('验证码已发送，请查收', 'success')
    emit('sent')
    const cfg = await getConfig()
    const total = (cfg && cfg.send_interval) || 60
    counting.value = total
    const timer = setInterval(() => {
      counting.value--
      if (counting.value <= 0) clearInterval(timer)
    }, 1000)
  } catch (e) {
    toast(e.message, 'error')
  } finally {
    sending.value = false
  }
}
</script>
