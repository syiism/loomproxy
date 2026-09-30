// HTTP 客户端：统一 token 注入、错误规范化、401 处理
export const TOKEN_KEY = 'loomproxy_token'

export function getToken() { return localStorage.getItem(TOKEN_KEY) || '' }
export function setToken(t) { localStorage.setItem(TOKEN_KEY, t) }
export function clearToken() { localStorage.removeItem(TOKEN_KEY) }

export class ApiError extends Error {
  constructor(message, status = 0) {
    super(message)
    this.name = 'ApiError'
    this.status = status
  }
}

function buildQuery(query) {
  if (!query) return ''
  const parts = []
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '') continue
    parts.push(encodeURIComponent(k) + '=' + encodeURIComponent(v))
  }
  return parts.length ? '?' + parts.join('&') : ''
}

/**
 * 统一请求封装。
 * 成功返回 body.data；业务错误（code !== 0）与 HTTP 错误统一抛 ApiError。
 */
export async function request(path, { method = 'GET', body, query, headers: extraHeaders } = {}) {
  const headers = { ...(extraHeaders || {}) }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  const token = getToken()
  if (token) headers['Authorization'] = 'Bearer ' + token

  let res
  try {
    res = await fetch(path + buildQuery(query), {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
    })
  } catch (e) {
    throw new ApiError('网络错误，请检查连接后重试', 0)
  }

  let payload = null
  try { payload = await res.json() } catch (e) { /* 非 JSON 响应 */ }

  if (res.status === 401) {
    clearToken()
    if (location.pathname !== '/panel/login') location.href = '/panel/login'
    throw new ApiError((payload && payload.msg) || '请先登录', 401)
  }
  if (!res.ok || (payload && payload.code !== 0)) {
    throw new ApiError((payload && payload.msg) || ('请求失败（HTTP ' + res.status + '）'), res.status)
  }
  return payload ? payload.data : null
}
