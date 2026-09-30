# 前端中控页面 + 管理员体系实施方案

## Context

项目已接入用户系统（注册/登录/JWT 鉴权）和三张基础表（roles / system_settings / quota_plans），但缺少：
1. **前端中控页面**：目前只有 API，无 UI 让用户登录、查看个人信息、让管理员管理用户/设置
2. **管理员引导机制**：注册接口只分配 `default_role=user`，无法产生第一个 admin 用户来管理后台

本次任务：创建一个嵌入式前端中控（vanilla JS + `//go:embed`，符合 minimalist-ui 编辑风格），分用户/管理员视图，管理员账号通过 `.env` 配置在首次启动时自动创建，不能直接注册。

---

## 一、后端改动

### 1.1 新增配置项 — `conf/conf.go`

在 `ConfMagr` 结构体新增两个字段：

```go
AdminUsername string
AdminPassword string
```

在 `Load()` 中读取：
```go
AdminUsername: envStr("ADMIN_USERNAME", ""),
AdminPassword: envStr("ADMIN_PASSWORD", ""),
```

### 1.2 管理员引导 — `db/seed.go`

在 `Seed()` 函数末尾新增 `seedAdmin(db)`：

- 查询是否已存在带 `admin` 角色的用户（通过 `user_roles` + `roles` 表 join 查询）
- 如果已存在 admin 用户 → 跳过
- 如果不存在且 `conf.Config.AdminUsername` 和 `conf.Config.AdminPassword` 都非空 → 创建用户并关联 `admin` 角色
- 日志输出：`Created admin user: <username>`（不输出密码）
- 如果配置为空 → 输出 WARNING 提示用户配置 `ADMIN_USERNAME` / `ADMIN_PASSWORD`

### 1.3 管理员中间件 — `handlers/auth/auth.go`

新增 `AdminRequired()` 中间件函数：

```go
func AdminRequired() gin.HandlerFunc {
    return func(c *gin.Context) {
        // 1. 复用 AuthRequired 的 token 校验逻辑
        // 2. 从 context 取 user_id，加载 user + roles
        // 3. 调用 user.IsAdmin() 判断
        // 4. 非 admin → fail(c, 403, "需要管理员权限")，c.Abort()
        // 5. 是 admin → c.Set("current_user", &user)，c.Next()
    }
}
```

为避免重复，把 token 解析逻辑抽成内部函数 `parseTokenFromContext(c) (uint, string, error)`，`AuthRequired` 和 `AdminRequired` 共用。

### 1.4 管理 API — 新建 `handlers/admin/admin.go`

路由组 `/admin/*`，全部挂 `AdminRequired()` 中间件：

| 方法 | 路径 | 功能 |
|---|---|---|
| GET | `/admin/stats` | 仪表盘统计（用户总数、各角色数、今日新增） |
| GET | `/admin/users` | 用户列表（分页 `page`/`page_size`，支持 `keyword` 搜索） |
| GET | `/admin/users/:id` | 用户详情（含角色） |
| PATCH | `/admin/users/:id` | 更新用户（status、nickname、email） |
| DELETE | `/admin/users/:id` | 删除用户（软删除） |
| POST | `/admin/users/:id/roles` | 修改用户角色（body: `{role_codes: ["user","vip"]}`） |
| POST | `/admin/users/:id/reset-password` | 管理员重置用户密码（body: `{new_password}`） |
| GET | `/admin/roles` | 角色列表 |
| GET | `/admin/settings` | 系统设置列表 |
| PUT | `/admin/settings/:key` | 更新设置项（body: `{value}`） |
| GET | `/admin/quotas/plans` | 额度套餐列表 |
| GET | `/admin/quotas/plans/:id/limits` | 套餐下的额度限制列表 |

所有响应沿用 `{code, msg, data}` 格式。复用 `auth.ok()` / `auth.fail()` 辅助函数（导出为 `Ok` / `Fail`）。

### 1.5 注册路由 + 静态文件 — `app/app.go`

```go
import (
    "loomproxy-go/handlers/admin"
    "loomproxy-go/web"
)

func CreateApp() *gin.Engine {
    // ... 现有逻辑 ...
    auth.RegisterRoutes(r)
    admin.RegisterRoutes(r)              // 新增
    // ... registerHandlers ...

    // 静态文件服务（/panel → web.FS）
    r.StaticFS("/panel", http.FS(web.FS))
}
```

### 1.6 鉴权白名单 — `conf/conf.go`

默认白名单加入 `/panel`（前端页面本身不需鉴权，API 调用时再带 token）：

```go
AuthWhitelist: envList("AUTH_WHITELIST", "/,/datasources,/data,/auth/register,/auth/login,/panel"),
```

---

## 二、前端实现（minimalist-ui 编辑风格）

### 2.1 目录结构

```
web/
├── embed.go        # //go:embed 指令，暴露 web.FS
├── index.html      # SPA 外壳（加载 styles.css + app.js）
├── styles.css      # minimalist-ui 样式
└── app.js          # SPA 逻辑（路由、API、渲染）
```

### 2.2 embed.go

```go
package web

import "embed"

//go:embed index.html styles.css app.js
var FS embed.FS
```

### 2.3 SPA 路由（hash 路由）

| Hash | 页面 | 鉴权 |
|---|---|---|
| `#/login` | 登录 | 公开 |
| `#/register` | 注册 | 公开 |
| `#/dashboard` | 用户仪表盘 | 登录 |
| `#/profile` | 个人信息 + 改密 | 登录 |
| `#/datasources` | 数据源浏览 | 登录 |
| `#/admin` | 管理员仪表盘 | admin |
| `#/admin/users` | 用户管理 | admin |
| `#/admin/settings` | 系统设置 | admin |
| `#/admin/roles` | 角色管理（只读展示） | admin |
| `#/admin/quotas` | 额度套餐 | admin |

启动逻辑：
1. 读 `localStorage.token`，调 `GET /auth/me` 验证
2. 有效 → 根据 `roles` 是否含 `admin` 决定默认跳 `#/dashboard` 或 `#/admin`
3. 无效 → 跳 `#/login`
4. 普通用户访问 `#/admin/*` → 重定向 `#/dashboard` 并提示无权限

### 2.4 UI 设计要点（遵循 minimalist-ui 协议）

- **字体**：正文用 `'Geist Sans', 'SF Pro Display', sans-serif`；标题用 `'Newsreader', 'Playfair Display', serif`（紧字距 -0.03em，行高 1.1）；代码/元数据用 `'Geist Mono', monospace`
- **配色**：背景 `#FBFBFA`，卡片 `#FFFFFF`，边框 `1px solid #EAEAEA`，正文 `#111111`，次要文字 `#787774`
- **角色标签**：admin → 浅红 `#FDEBEC`/`#9F2F2D`；vip → 浅黄 `#FBF3DB`/`#956400`；user → 浅灰 `#F2F1EF`/`#787774`
- **按钮**：主按钮 `bg:#111` `color:#fff` `radius:6px`，hover `#333`，active `scale(0.98)`
- **卡片**：`border:1px solid #EAEAEA`，`radius:12px`，`padding:24-32px`
- **无阴影**、**无渐变**、**无 emoji**
- **入场动画**：`IntersectionObserver` + `translateY(12px)` + `opacity:0` → `600ms cubic-bezier(0.16,1,0.3,1)`
- **Bento 网格**：仪表盘用 `grid-template-columns: repeat(auto-fit, minmax(240px, 1fr))`

### 2.5 页面功能

**登录页**：居中卡片，用户名/密码输入，登录按钮，底部「注册账号」链接（若 `register_enabled` 为 false 则隐藏）

**注册页**：用户名/邮箱/密码/昵称，注册成功自动登录跳转

**用户仪表盘**：
- 顶部：欢迎语 + 用户名 + 角色标签
- Bento 网格：账号信息卡 / 数据源入口卡 / API Key 说明卡
- 数据源入口卡点击 → `#/datasources`

**数据源浏览页**：调 `GET /datasources`，按 `category` 分组展示数据源卡片，每张卡显示 `id` / `name` / `search_tab` 列表

**个人信息页**：展示用户信息 + 修改密码表单

**管理员仪表盘**：
- 统计卡片：总用户数 / 今日新增 / admin 数 / vip 数
- 快捷入口：用户管理 / 系统设置 / 额度管理

**用户管理页**：
- 搜索框 + 分页表格（用户名/邮箱/角色标签/状态/创建时间/操作）
- 操作：编辑（弹窗改 nickname/email/status）、改角色（弹窗多选 role_codes）、重置密码、删除
- 状态用浅绿（启用）/浅红（禁用）标签

**系统设置页**：键值对列表，每行 `key` / 当前 `value` / 描述 / 编辑按钮，点击编辑弹出输入框

**额度管理页**：套餐卡片列表，每张卡显示套餐名 + 限制列表（scope/target/limit/period）

---

## 三、验证方案

### 3.1 编译
```bash
cd /home/muyang/work/modelscope/loomproxy-go
go build -o /tmp/loomproxy-panel .
```

### 3.2 启动（带 admin 配置）
在 `.env` 追加：
```
ADMIN_USERNAME=admin
ADMIN_PASSWORD=admin123456
AUTH_ENABLED=true
```
删除旧 db 后启动：
```bash
rm -f data/loomproxy.db
/tmp/loomproxy-panel
```
日志应出现 `Created admin user: admin`

### 3.3 端到端测试

**浏览器测试**：
1. 访问 `http://localhost:8081/panel` → 应显示登录页
2. 用 `admin` / `admin123456` 登录 → 应跳转管理员仪表盘
3. 点击「用户管理」→ 应看到 admin 用户
4. 访问 `#/admin/settings` → 应能切换 `register_enabled`

**API 测试**：
```bash
# 1. admin 登录拿 token
TOKEN=$(curl -s -X POST localhost:8081/auth/login -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"admin123456"}' | jq -r .data.token)

# 2. 访问 /admin/stats（应 200）
curl -s localhost:8081/admin/stats -H "Authorization: Bearer $TOKEN"

# 3. 注册普通用户
curl -s -X POST localhost:8081/auth/register -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"alice123"}'

# 4. 普通用户访问 /admin/*（应 403）
USER_TOKEN=$(curl -s -X POST localhost:8081/auth/login -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"alice123"}' | jq -r .data.token)
curl -s -w "\n%{http_code}\n" localhost:8081/admin/stats -H "Authorization: Bearer $USER_TOKEN"
```

---

## 四、涉及文件清单

**新建**：
- `web/embed.go`
- `web/index.html`
- `web/styles.css`
- `web/app.js`
- `handlers/admin/admin.go`

**修改**：
- `conf/conf.go` — 新增 `AdminUsername` / `AdminPassword`，白名单加 `/panel`
- `db/seed.go` — 新增 `seedAdmin()`
- `handlers/auth/auth.go` — 新增 `AdminRequired()`，导出 `Ok()` / `Fail()` 供 admin 包复用
- `app/app.go` — 注册 `/admin/*` 路由，挂载 `/panel` 静态文件
