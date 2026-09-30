<template>
  <div>
    <PageHeader title="接入指南" subtitle="在阅读 App 或自有程序中调用本服务的书源接口。" />

    <!-- 服务信息 -->
    <section class="reveal mb-10 md:mb-12">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">服务信息</h2>
      <div class="card space-y-4">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-1.5">服务地址</div>
            <code class="font-mono text-sm">{{ origin }}</code>
          </div>
          <button class="btn-ghost btn-sm" @click="copy(origin, '服务地址')">复制</button>
        </div>
        <div class="pt-4 border-t border-border">
          <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-1.5">调用形式</div>
          <code class="font-mono text-sm break-all">GET {{ origin }}/<span class="text-text-muted">{数据源}</span>/<span class="text-text-muted">{动作}</span>?query=...</code>
        </div>
      </div>
    </section>

    <!-- 鉴权方式 -->
    <section class="reveal mb-10 md:mb-12">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">鉴权方式</h2>
      <div class="grid grid-cols-1 md:grid-cols-3 gap-4 md:gap-6">
        <div class="card card-hover reveal flex flex-col">
          <UiTag tone="green" label="推荐" class="self-start mb-3" />
          <div class="font-medium mb-2">Token</div>
          <p class="text-sm text-text-muted mb-4 flex-1">请求头携带 <code class="font-mono text-xs">Authorization: Bearer &lt;token&gt;</code>。当前登录凭证：</p>
          <div class="flex items-center gap-2">
            <code class="font-mono text-xs bg-surface-alt border border-border rounded px-2 py-1.5 flex-1 truncate">{{ maskedToken }}</code>
            <button class="btn-ghost btn-sm" @click="copy(token, 'Token')">复制</button>
          </div>
          <p class="text-xs text-text-muted mt-3">有效期 7 天，过期后重新登录获取。</p>
        </div>
        <div class="card card-hover reveal flex flex-col">
          <UiTag tone="blue" label="浏览器" class="self-start mb-3" />
          <div class="font-medium mb-2">Cookie</div>
          <p class="text-sm text-text-muted flex-1">登录后浏览器同源请求自动携带 <code class="font-mono text-xs">loomproxy_token</code> Cookie，无需额外操作。适合直接在本控制台或同源页面中调试。</p>
        </div>
        <div class="card card-hover reveal flex flex-col">
          <UiTag tone="yellow" label="长期" class="self-start mb-3" />
          <div class="font-medium mb-2">API Key</div>
          <p class="text-sm text-text-muted flex-1">在个人中心「API 密钥」自助创建，通过 <code class="font-mono text-xs">X-API-Key</code> 请求头或 <code class="font-mono text-xs">?api_key=</code> 参数传递。调用归属你的账号（按所购套餐计费与限流），密钥可随时查看或撤销，适合配置在阅读 App 书源中长期使用。</p>
        </div>
      </div>
    </section>

    <!-- 调用示例 -->
    <section class="reveal mb-10 md:mb-12">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">调用示例</h2>
      <div class="card !p-0 overflow-hidden">
        <div class="flex items-center gap-1.5 px-4 py-2.5 border-b border-border bg-surface-alt">
          <span class="w-2.5 h-2.5 rounded-full bg-border"></span>
          <span class="w-2.5 h-2.5 rounded-full bg-border"></span>
          <span class="w-2.5 h-2.5 rounded-full bg-border"></span>
          <span class="ml-2 font-mono text-xs text-text-muted">terminal</span>
          <button class="ml-auto text-xs text-text-muted hover:text-text transition-colors" @click="copy(curlExample, '示例命令')">复制</button>
        </div>
        <pre class="p-5 font-mono text-xs md:text-sm overflow-x-auto leading-relaxed">{{ curlExample }}</pre>
      </div>
    </section>

    <!-- 可用端点 -->
    <section class="reveal">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">可用端点</h2>
      <UiSpinner v-if="loading" />
      <UiEmpty v-else-if="error" title="加载失败" :text="error" />
      <UiEmpty v-else-if="sources.length === 0" title="暂无可用端点" />
      <div v-else class="grid grid-cols-1 md:grid-cols-2 gap-4 md:gap-6">
        <div v-for="s in sources" :key="s.id" class="card card-hover reveal">
          <div class="flex items-center justify-between mb-1">
            <div class="font-medium">{{ s.name }}</div>
            <span class="font-mono text-xs text-text-muted">{{ s.id }}</span>
          </div>
          <div class="divide-y divide-border mt-3">
            <div v-for="e in s.endpoints" :key="e.path" class="py-2.5 first:pt-0 last:pb-0">
              <div class="flex items-center gap-2 mb-1.5">
                <span class="kbd">{{ e.method }}</span>
                <code class="font-mono text-xs">{{ e.path }}</code>
              </div>
              <div class="flex flex-wrap items-center gap-1.5">
                <span class="text-xs text-text-muted">{{ e.description }}</span>
                <span v-for="p in e.params" :key="p" class="px-1.5 py-0.5 bg-surface-alt border border-border rounded font-mono text-xs text-text-muted">{{ p }}</span>
              </div>
            </div>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, nextTick } from 'vue'
import PageHeader from '../components/PageHeader.vue'
import UiTag from '../components/UiTag.vue'
import UiSpinner from '../components/UiSpinner.vue'
import UiEmpty from '../components/UiEmpty.vue'
import { getToken, miscApi } from '../api/index.js'
import { toast, revealObserve } from '../utils.js'

const loading = ref(true)
const error = ref('')
const sources = ref([])
const origin = window.location.origin
const token = ref(getToken())

const maskedToken = computed(() => {
  if (!token.value) return '未登录'
  if (token.value.length <= 24) return token.value
  return token.value.slice(0, 16) + '…' + token.value.slice(-6)
})

const curlExample = computed(() => {
  const search = findEndpoint('search')
  const path = search ? search.path : '/<数据源>/search'
  const params = search && search.params.includes('query') ? 'query=书名&page=1' : 'query=书名'
  return [
    `# 搜索示例`,
    `curl -H "Authorization: Bearer ${token.value ? token.value.slice(0, 16) + '…' : '<token>'}" \\`,
    `  "${origin}${path}?${params}"`,
  ].join('\n')
})

const findEndpoint = (action) => {
  for (const s of sources.value) {
    const hit = s.endpoints.find(e => e.path.endsWith('/' + action))
    if (hit) return hit
  }
  return null
}

const copy = async (text, label) => {
  try {
    await navigator.clipboard.writeText(text)
    toast(label + '已复制', 'success')
  } catch (e) {
    toast('复制失败，请手动选择复制', 'error')
  }
}

const load = async () => {
  try {
    const [endRes, dsList] = await Promise.all([
      miscApi.endpoints(),
      miscApi.datasources(),
    ])
    const nameOf = {}
    ;(dsList || []).forEach(d => { nameOf[d.id] = d.name })
    const epList = endRes || []
    sources.value = epList.map(ep => ({
      id: ep.id,
      name: nameOf[ep.id] || ep.name || ep.id,
      endpoints: (ep.endpoints || []).map(e => ({
        path: e.path,
        method: e.method,
        description: e.description,
        params: (e.params || []).filter(p => p !== 'base_url'),
      })),
    }))
  } catch (e) {
    error.value = e.message
  }
  loading.value = false
  nextTick(revealObserve)
}

onMounted(() => { revealObserve() })
load()
</script>
