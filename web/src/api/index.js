// 按域组织的 API 端点方法；页面不直接拼路径
import { request } from './client.js'

export const authApi = {
  login: (username, password) => request('/auth/login', { method: 'POST', body: { username, password } }),
  register: (payload) => request('/auth/register', { method: 'POST', body: payload }),
  forgotPassword: (payload) => request('/auth/forgot-password', { method: 'POST', body: payload }),
  // 验证码（场景开关由后端 verify_code_scenes 控制，config 决定前端是否渲染输入框）
  verifyConfig: () => request('/verify/config'),
  sendVerifyCode: (scene, target) => request('/verify/send', { method: 'POST', body: { scene, target } }),
  me: () => request('/auth/me'),
  updateMe: (payload) => request('/auth/me', { method: 'PATCH', body: payload }),
  // 阅读数据留存同意位（隐私协议）：唯一写入口，只认会话
  updatePrivacy: (contentConsent) =>
    request('/auth/privacy', { method: 'POST', body: { content_consent: contentConsent } }),
  // 单日额度的清零钟点模式（待办清单 P106 / P119 ①）：端点与服务端口径原样保留，
  // 但**面板不再提供切换入口**（2026-10-10 从个人中心摘掉那一格），所以这里不再有 wrapper——
  // 留一个没人调的 wrapper 就是给下一个读代码的人一句假话（P114 那一族）。要重新给入口时从 git 里取回。
  // 套餐名/角色名的显示别名（待办清单 P43）：alias 传空串 = 清除覆盖、回默认名
  updateDisplayAlias: (kind, targetId, alias) =>
    request('/auth/display-alias', { method: 'PUT', body: { kind, target_id: targetId, alias } }),
  changePassword: (oldPassword, newPassword) =>
    request('/auth/password', { method: 'POST', body: { old_password: oldPassword, new_password: newPassword } }),
  logout: () => request('/auth/logout', { method: 'POST' }),
  listSessions: () => request('/auth/sessions'),
  revokeOtherSessions: () => request('/auth/sessions/revoke-others', { method: 'POST' }),
  revokeSession: (id) => request('/auth/sessions/' + id, { method: 'DELETE' }),
}

export const apikeyApi = {
  list: () => request('/apikey'),
  create: (name) => request('/apikey', { method: 'POST', body: { name } }),
  revoke: (id) => request('/apikey/' + id, { method: 'DELETE' }),
}

export const adminApi = {
  // 统计
  stats: () => request('/admin/stats'),
  // 用户
  // 用户：筛选条件全走 query（待办清单 P46）。空值由 client.js 的 buildQuery 统一丢掉，
  // 「不发送」才是「不筛」——后端对非法枚举值也是忽略，但那是兜底，不是这条通路
  listUsers: ({
    page = 1, pageSize = 20, keyword = '', status = '', plan = '', role = '',
    expire = '', expireDays = '', activity = '', consent = '', quotaReset = '', keys = '', overCap = false,
  } = {}) =>
    request('/admin/users', { query: {
      page, page_size: pageSize, keyword, status, plan, role, expire, activity, consent,
      quota_reset: quotaReset, keys,
      // 天数只在「即将到期」这一档有意义，切走就别把它留在 URL 里
      expire_days: expire === 'expiring' ? expireDays : '',
      over_cap: overCap ? 1 : '',
    } }),
  getUser: (id) => request('/admin/users/' + id),
  createUser: (payload) => request('/admin/users', { method: 'POST', body: payload }),
  updateUser: (id, payload) => request('/admin/users/' + id, { method: 'PATCH', body: payload }),
  deleteUser: (id) => request('/admin/users/' + id, { method: 'DELETE' }),
  restoreUser: (id) => request('/admin/users/' + id + '/restore', { method: 'POST' }),
  updateUserRoles: (id, roleCode) => request('/admin/users/' + id + '/roles', { method: 'POST', body: { role_code: roleCode } }),
  resetUserPassword: (id, newPassword) => request('/admin/users/' + id + '/reset-password', { method: 'POST', body: { new_password: newPassword } }),
  // 单日额度刷新：把该用户的用量起算点推到此刻（流水不删，待办清单 P41）
  refreshUserQuota: (id) => request('/admin/users/' + id + '/refresh-quota', { method: 'POST' }),
  updateUserPlan: (id, planId) => request('/admin/users/' + id + '/plan', { method: 'PUT', body: { plan_id: planId } }),
  getUserQuota: (id) => request('/admin/users/' + id + '/quota'),
  updateUserQuota: (id, overrides) => request('/admin/users/' + id + '/quota', { method: 'PUT', body: { overrides } }),
  // IP 黑名单
  listBlockedIPs: () => request('/admin/blocked-ips'),
  addBlockedIP: (payload) => request('/admin/blocked-ips', { method: 'POST', body: payload }),
  removeBlockedIP: (id) => request('/admin/blocked-ips/' + id, { method: 'DELETE' }),
  // 登录防爆破的限频状态（进程内存，不落库）：读快照 + 定向解锁
  // 多设备与密钥分布读数（待办清单 P44）；sort/dir 见 P45，非法值后端回默认而不是 400
  listDeviceActivity: ({ page = 1, pageSize = 20, keyword = '', sort = '', dir = '' } = {}) =>
    request('/admin/devices', { query: { page, page_size: pageSize, keyword, sort, dir } }),
  listSecurityAttempts: () => request('/admin/security/attempts'),
  resetSecurityAttempts: (payload) => request('/admin/security/attempts/reset', { method: 'POST', body: payload }),
  // 角色
  listRoles: () => request('/admin/roles'),
  createRole: (payload) => request('/admin/roles', { method: 'POST', body: payload }),
  updateRole: (id, payload) => request('/admin/roles/' + id, { method: 'PATCH', body: payload }),
  deleteRole: (id) => request('/admin/roles/' + id, { method: 'DELETE' }),
  // 设置
  listSettings: () => request('/admin/settings'),
  createSetting: (payload) => request('/admin/settings', { method: 'POST', body: payload }),
  updateSetting: (key, value) => request('/admin/settings/' + encodeURIComponent(key), { method: 'PUT', body: { value } }),
  deleteSetting: (key) => request('/admin/settings/' + encodeURIComponent(key), { method: 'DELETE' }),
  // 额度套餐与限制
  listPlans: () => request('/admin/quotas/plans'),
  createPlan: (payload) => request('/admin/quotas/plans', { method: 'POST', body: payload }),
  updatePlan: (id, payload) => request('/admin/quotas/plans/' + id, { method: 'PATCH', body: payload }),
  deletePlan: (id) => request('/admin/quotas/plans/' + id, { method: 'DELETE' }),
  listLimits: (planId) => request('/admin/quotas/plans/' + planId + '/limits'),
  createLimit: (payload) => request('/admin/quotas/limits', { method: 'POST', body: payload }),
  updateLimit: (id, payload) => request('/admin/quotas/limits/' + id, { method: 'PUT', body: payload }),
  deleteLimit: (id) => request('/admin/quotas/limits/' + id, { method: 'DELETE' }),
  // 接口消耗
  listQuotaCosts: (sourceCode) => request('/admin/quota-costs', { query: { source_code: sourceCode || undefined } }),
  updateQuotaCost: (id, payload) => request('/admin/quota-costs/' + id, { method: 'PUT', body: payload }),
  listPlanQuotaCosts: () => request('/admin/quota-costs/plans'),
  updatePlanQuotaCost: (id, payload) => request('/admin/quota-costs/plans/' + id, { method: 'PUT', body: payload }),
  upsertPlanQuotaCost: (payload) => request('/admin/quota-costs/plans/upsert', { method: 'POST', body: payload }),
  // 用量流水
  listUsageLogs: ({ page = 1, pageSize = 20, username, group } = {}) =>
    request('/admin/usage-logs', { query: { page, page_size: pageSize, username: username || undefined, group: group || undefined } }),
  // 接口监控（内存计数，与计费无关）
  getMonitor: () => request('/admin/monitor'),
  getMonitorTrend: () => request('/admin/monitor/trend'),
  // 内容维度榜单：dim ∈ keyword|book|chapter|media
  // 公开排行榜（登录用户可读；放行范围由设置项 rank_public_sources 按数据源决定）
  rankBoards: ({ days = 7, source, media } = {}) =>
    request('/rank/boards', { query: { days, source: source || undefined, media: media || undefined } }),
  getMonitorSubjects: ({ dim, days, source, media } = {}) =>
    request('/admin/monitor/subjects', { query: { dim, days: days || undefined, source: source || undefined, media: media || undefined } }),
  listPools: () => request('/admin/pools'),
  resetMonitor: () => request('/admin/monitor/reset', { method: 'POST' }),
  getMonitorHistory: ({ page = 1, pageSize = 20, source, username, action, keyword, bookName, chapterTitle, mediaType, inBandCode } = {}) =>
    request('/admin/monitor/history', {
      query: {
        page, page_size: pageSize,
        source: source || undefined, username: username || undefined, action: action || undefined,
        keyword: keyword || undefined, book_name: bookName || undefined,
        chapter_title: chapterTitle || undefined, media_type: mediaType || undefined,
        in_band_code: inBandCode || undefined,
      },
    }),
  // 卡密（套餐兑换）
  createRedeemCodes: (payload) => request('/admin/redeem-codes', { method: 'POST', body: payload }),
  listRedeemCodes: ({ page = 1, pageSize = 20, batchNo, status, planId } = {}) =>
    request('/admin/redeem-codes', { query: { page, page_size: pageSize, batch_no: batchNo || undefined, status: status || undefined, plan_id: planId || undefined } }),
  revokeRedeemCode: (id) => request('/admin/redeem-codes/' + id + '/revoke', { method: 'POST' }),
  revokeRedeemCodes: (ids) => request('/admin/redeem-codes/revoke-selected', { method: 'POST', body: { ids } }),
  revokeRedeemBatch: (batchNo) => request('/admin/redeem-codes/batch/' + batchNo, { method: 'DELETE' }),
  // 数据源默认地址
  listSourceConfigs: () => request('/admin/source-configs'),
  updateSourceConfigs: (configs) => request('/admin/source-configs', { method: 'PUT', body: { configs } }),
  // 数据源管理
  listDataSources: () => request('/admin/data-sources'),
  createDataSource: (payload) => request('/admin/data-sources', { method: 'POST', body: payload }),
  updateDataSource: (id, payload) => request('/admin/data-sources/' + id, { method: 'PATCH', body: payload }),
  deleteDataSource: (id) => request('/admin/data-sources/' + id, { method: 'DELETE' }),
  // 数据源分组：归类与筛选视图（标签），不参与额度/计费/限速解析
  listSourceGroups: () => request('/admin/source-groups'),
  createSourceGroup: (payload) => request('/admin/source-groups', { method: 'POST', body: payload }),
  updateSourceGroup: (id, payload) => request('/admin/source-groups/' + id, { method: 'PATCH', body: payload }),
  deleteSourceGroup: (id) => request('/admin/source-groups/' + id, { method: 'DELETE' }),
  updateSourceGroupMembers: (id, sourceNames) => request('/admin/source-groups/' + id + '/members', { method: 'PUT', body: { source_names: sourceNames } }),
  applySourceGroupLimits: (id, payload) => request('/admin/source-groups/' + id + '/apply-limits', { method: 'POST', body: payload }),
  // 套餐的授权数据源（= quota_limits 里 scope=source 的行；增删走「限制项」的 limits 接口）
  listPlanDataSources: (planId) => request('/admin/quotas/plans/' + planId + '/data-sources'),
}

export const quotaApi = {
  dashboard: () => request('/quota/dashboard'),
  // 额度转移（待办清单 P97）：本人侧唯一写入口，只认会话（apiKey 打这里 401，有用例钉着）
  transfer: (from, to, amount) =>
    request('/quota/transfer', { method: 'POST', body: { from, to, amount } }),
  myUsageLogs: ({ page = 1, pageSize = 10, group } = {}) =>
    request('/quota/usage-logs', { query: { page, page_size: pageSize, group: group || undefined } }),
  // 转移历史（P97 的「人也要能查」那一半）：只读，三形态统一可用；回显不带操作者身份，只有 via
  myTransfers: ({ page = 1, pageSize = 5 } = {}) =>
    request('/quota/transfers', { query: { page, page_size: pageSize } }),
  // 清除本人的转移记录（待办清单 P119 ④ 的 C 档：软删）。**只认会话**——
  // 与 POST /quota/transfer 同一守卫口径：长期密钥能读自己挪过什么，不能销毁记录。
  clearTransfers: () => request('/quota/transfers', { method: 'DELETE' }),
}

export const miscApi = {
  datasources: () => request('/datasources'),
  endpoints: () => request('/endpoints'),
  announcement: () => request('/announcement'),
}

export const userConfigApi = {
  listSourceConfigs: () => request('/user/source-configs'),
  updateSourceConfigs: (configs) => request('/user/source-configs', { method: 'PUT', body: { configs } }),
  redeem: (code) => request('/user/redeem', { method: 'POST', body: { code } }),
  getImportConfig: () => request('/user/import-config'),
}

export { getToken, setToken, clearToken, ApiError } from './client.js'
