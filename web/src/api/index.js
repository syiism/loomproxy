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
  listUsers: ({ page = 1, pageSize = 20, keyword = '', withDeleted = false } = {}) =>
    request('/admin/users', { query: { page, page_size: pageSize, keyword: keyword || undefined, with_deleted: withDeleted ? 1 : undefined } }),
  getUser: (id) => request('/admin/users/' + id),
  createUser: (payload) => request('/admin/users', { method: 'POST', body: payload }),
  updateUser: (id, payload) => request('/admin/users/' + id, { method: 'PATCH', body: payload }),
  deleteUser: (id) => request('/admin/users/' + id, { method: 'DELETE' }),
  restoreUser: (id) => request('/admin/users/' + id + '/restore', { method: 'POST' }),
  updateUserRoles: (id, roleCode) => request('/admin/users/' + id + '/roles', { method: 'POST', body: { role_code: roleCode } }),
  resetUserPassword: (id, newPassword) => request('/admin/users/' + id + '/reset-password', { method: 'POST', body: { new_password: newPassword } }),
  updateUserPlan: (id, planId) => request('/admin/users/' + id + '/plan', { method: 'PUT', body: { plan_id: planId } }),
  getUserQuota: (id) => request('/admin/users/' + id + '/quota'),
  updateUserQuota: (id, overrides) => request('/admin/users/' + id + '/quota', { method: 'PUT', body: { overrides } }),
  // IP 黑名单
  listBlockedIPs: () => request('/admin/blocked-ips'),
  addBlockedIP: (payload) => request('/admin/blocked-ips', { method: 'POST', body: payload }),
  removeBlockedIP: (id) => request('/admin/blocked-ips/' + id, { method: 'DELETE' }),
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
  getMonitorHistory: ({ page = 1, pageSize = 20, source, username, action, keyword, bookName, chapterTitle, mediaType } = {}) =>
    request('/admin/monitor/history', {
      query: {
        page, page_size: pageSize,
        source: source || undefined, username: username || undefined, action: action || undefined,
        keyword: keyword || undefined, book_name: bookName || undefined,
        chapter_title: chapterTitle || undefined, media_type: mediaType || undefined,
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
  // 套餐-数据源关联
  listPlanDataSources: (planId) => request('/admin/quotas/plans/' + planId + '/data-sources'),
  addPlanDataSource: (planId, dataSourceId) => request('/admin/quotas/plans/' + planId + '/data-sources', { method: 'POST', body: { data_source_id: dataSourceId } }),
  batchAddPlanDataSources: (planId, dataSourceIds) => request('/admin/quotas/plans/' + planId + '/data-sources/batch', { method: 'POST', body: { data_source_ids: dataSourceIds } }),
  removePlanDataSource: (planId, dataSourceId) => request('/admin/quotas/plans/' + planId + '/data-sources/' + dataSourceId, { method: 'DELETE' }),
}

export const quotaApi = {
  dashboard: () => request('/quota/dashboard'),
  myUsageLogs: ({ page = 1, pageSize = 10, group } = {}) =>
    request('/quota/usage-logs', { query: { page, page_size: pageSize, group: group || undefined } }),
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
