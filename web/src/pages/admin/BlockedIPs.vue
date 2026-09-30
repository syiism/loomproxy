<template>
  <div>
    <PageHeader title="IP 拉黑" subtitle="被拉黑的 IP 无法登录、注册，也无法调用任何接口。" />

    <div class="reveal flex flex-col sm:flex-row gap-3 mb-6">
      <input v-model="form.ip" placeholder="IP 地址（IPv4 / IPv6）" class="input sm:w-64 font-mono" @keydown.enter="add">
      <input v-model="form.note" placeholder="备注（可选）" class="input flex-1" @keydown.enter="add">
      <button @click="add" class="btn-primary whitespace-nowrap" :disabled="submitting">拉黑</button>
    </div>

    <UiSpinner v-if="loading" />
    <UiEmpty v-else-if="error" title="加载失败" :text="error" />
    <UiEmpty v-else-if="list.length === 0" title="黑名单为空" text="添加 IP 后，该地址的所有请求将被拒绝（403）。" />
    <template v-else>
      <div class="table-wrap reveal overflow-x-auto">
        <table class="table-base">
          <thead>
            <tr><th>ID</th><th>IP</th><th>来源</th><th>备注</th><th>拉黑时间</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="b in list" :key="b.id">
              <td class="font-mono text-xs">{{ b.id }}</td>
              <td class="font-mono text-sm">{{ b.ip }}</td>
              <td><UiTag :tone="b.source === 'auto' ? 'yellow' : 'gray'" :label="b.source === 'auto' ? '自动' : '手动'" /></td>
              <td class="text-text-muted max-w-[240px] truncate">{{ b.note || '—' }}</td>
              <td class="font-mono text-xs text-text-muted whitespace-nowrap">{{ fmtDate(b.created_at) }}</td>
              <td>
                <button @click="remove(b)" class="text-xs text-pale-red-fg hover:opacity-70 transition-opacity" :disabled="submitting">解除</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>
  </div>
</template>

<script setup>
import { ref, onMounted, nextTick } from 'vue'
import PageHeader from '../../components/PageHeader.vue'
import UiSpinner from '../../components/UiSpinner.vue'
import UiTag from '../../components/UiTag.vue'
import UiEmpty from '../../components/UiEmpty.vue'
import { adminApi } from '../../api/index.js'
import { fmtDate, toast, revealObserve } from '../../utils.js'

const loading = ref(true)
const error = ref('')
const list = ref([])
const submitting = ref(false)
const form = ref({ ip: '', note: '' })

const load = async () => {
  loading.value = true
  error.value = ''
  try {
    list.value = await adminApi.listBlockedIPs() || []
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
}

const add = async () => {
  if (submitting.value) return
  const ip = form.value.ip.trim()
  if (!ip) { toast('请输入 IP 地址', 'error'); return }
  submitting.value = true
  try {
    await adminApi.addBlockedIP({ ip, note: form.value.note.trim() || undefined })
    toast('已拉黑', 'success')
    form.value = { ip: '', note: '' }
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

const remove = async (b) => {
  if (submitting.value) return
  submitting.value = true
  try {
    await adminApi.removeBlockedIP(b.id)
    toast('已解除', 'success')
    load()
  } catch (e) { toast(e.message, 'error') } finally { submitting.value = false }
}

onMounted(() => { revealObserve() })
load()
</script>
