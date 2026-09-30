/* LoomProxy Console — vanilla SPA with Tailwind CSS */

const API_BASE = '';
const TOKEN_KEY = 'loomproxy_token';

// ---------- Utils ----------
const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));

function getToken() { return localStorage.getItem(TOKEN_KEY) || ''; }
function setToken(t) { localStorage.setItem(TOKEN_KEY, t); }
function clearToken() { localStorage.removeItem(TOKEN_KEY); }

async function api(path, opts = {}) {
  const headers = { 'Content-Type': 'application/json', ...(opts.headers || {}) };
  const token = getToken();
  if (token) headers['Authorization'] = 'Bearer ' + token;
  const res = await fetch(API_BASE + path, { ...opts, headers });
  let body = null;
  try { body = await res.json(); } catch (e) { }
  if (res.status === 401) {
    clearToken();
    if (!location.hash.startsWith('#/login')) location.hash = '#/login';
    throw new Error((body && body.msg) || '请先登录');
  }
  if (!res.ok || (body && body.code !== 0)) {
    throw new Error((body && body.msg) || ('HTTP ' + res.status));
  }
  return body.data;
}

function toast(msg, type = 'info') {
  const el = document.createElement('div');
  el.className = 'animate-slide-in bg-surface border border-border rounded-lg px-4 py-3 text-sm min-w-[240px]';
  el.style.borderLeft = '3px solid ' + (type === 'error' ? '#9F2F2D' : type === 'success' ? '#346538' : '#111111');
  el.textContent = msg;
  $('#toast').appendChild(el);
  setTimeout(() => { el.style.opacity = '0'; el.style.transition = 'opacity 200ms'; setTimeout(() => el.remove(), 200); }, 3000);
}

function escapeHtml(s) {
  if (s == null) return '';
  return String(s).replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}

function fmtDate(s) {
  if (!s) return '—';
  const d = new Date(s);
  if (isNaN(d)) return s;
  const pad = n => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function roleTag(code) {
  const map = {
    admin: 'bg-pale-red-bg text-pale-red-fg',
    vip: 'bg-pale-yellow-bg text-pale-yellow-fg',
    user: 'bg-pale-gray-bg text-pale-gray-fg',
  };
  return `<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium uppercase tracking-wider ${map[code] || 'bg-pale-gray-bg text-pale-gray-fg'}">${escapeHtml(code)}</span>`;
}

function statusTag(status) {
  return status === 1
    ? `<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium uppercase tracking-wider bg-pale-green-bg text-pale-green-fg">启用</span>`
    : `<span class="inline-flex items-center px-2.5 py-0.5 rounded-full text-xs font-medium uppercase tracking-wider bg-pale-red-bg text-pale-red-fg">禁用</span>`;
}

// ---------- State ----------
const state = {
  user: null,
  registerEnabled: true,
};

async function loadMe() {
  if (!getToken()) return null;
  try {
    const data = await api('/auth/me');
    state.user = data;
    return data;
  } catch (e) {
    return null;
  }
}

function isAdmin() {
  return state.user && Array.isArray(state.user.roles) && state.user.roles.some(r => r.code === 'admin');
}

// ---------- Router ----------
const routes = {};

function route(path, handler) { routes[path] = handler; }

function navigate(hash) { location.hash = hash; }

function handleHash() {
  const hash = location.hash.slice(1) || '/login';
  const [path, queryStr] = hash.split('?');
  const query = {};
  if (queryStr) {
    queryStr.split('&').forEach(p => {
      const [k, v] = p.split('=');
      query[decodeURIComponent(k)] = decodeURIComponent(v || '');
    });
  }

  if (!state.user && path !== '/login' && path !== '/register') {
    navigate('/login');
    return;
  }
  if (state.user && (path === '/login' || path === '/register')) {
    navigate(isAdmin() ? '/admin' : '/dashboard');
    return;
  }
  if (path.startsWith('/admin') && state.user && !isAdmin()) {
    toast('需要管理员权限', 'error');
    navigate('/dashboard');
    return;
  }

  const handler = routes[path] || routes['/404'];
  handler(query);
  $$('.nav-link').forEach(a => {
    a.classList.toggle('text-text', a.classList.toggle('font-medium', a.getAttribute('href') === '#' + path));
    a.classList.toggle('text-text-muted', a.getAttribute('href') !== '#' + path);
  });
  window.scrollTo(0, 0);
}

// ---------- Layout ----------
function renderShell(content) {
  const app = $('#app');
  app.innerHTML = `
    <header class="sticky top-0 z-50 bg-bg/85 backdrop-blur-md border-b border-border">
      <div class="max-w-6xl mx-auto px-4 md:px-6 py-3 md:py-4 flex items-center justify-between">
        <div class="flex items-center">
          <span class="font-serif text-lg md:text-xl lg:text-2xl tracking-tighter font-medium">LoomProxy</span>
          <span class="font-mono text-xs text-text-muted uppercase tracking-wider ml-3 hidden md:block">Console</span>
        </div>
        <button id="mobile-menu-btn" class="md:hidden p-2 text-text-muted hover:text-text transition-colors" onclick="toggleMobileMenu()">
          <svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M4 6h16M4 12h16M4 18h16"></path></svg>
        </button>
        <nav class="hidden md:flex gap-6 md:gap-7 items-center">
          <a href="#/dashboard" class="nav-link text-sm text-text-muted hover:text-text transition-colors">概览</a>
          <a href="#/datasources" class="nav-link text-sm text-text-muted hover:text-text transition-colors">数据源</a>
          <a href="#/profile" class="nav-link text-sm text-text-muted hover:text-text transition-colors">账号</a>
          ${isAdmin() ? `
            <a href="#/admin" class="nav-link text-sm text-text-muted hover:text-text transition-colors">管理</a>
            <a href="#/admin/users" class="nav-link text-sm text-text-muted hover:text-text transition-colors">用户</a>
            <a href="#/admin/settings" class="nav-link text-sm text-text-muted hover:text-text transition-colors">设置</a>
          ` : ''}
        </nav>
        <div class="flex items-center gap-2 md:gap-3">
          <div class="hidden sm:flex items-center gap-2 px-2 md:px-3 py-1 md:py-1.5 border border-border rounded-md bg-surface">
            <span class="text-xs md:text-sm font-medium">${escapeHtml(state.user.nickname || state.user.username)}</span>
            <button onclick="location.hash='#/profile'" class="text-text-muted hover:text-text transition-colors">
              <svg class="w-4 md:w-5 h-4 md:h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M16 7a4 4 0 11-8 0 4 4 0 018 0zM12 14a7 7 0 00-7 7h14a7 7 0 00-7-7z"></path></svg>
            </button>
          </div>
          <button onclick="handleLogout()" class="text-xs md:text-sm text-text-muted hover:text-text transition-colors">退出</button>
        </div>
      </div>
    </header>
    <div id="mobile-menu" class="fixed inset-0 z-40 bg-black/35 md:hidden hidden">
      <div class="absolute right-0 top-0 h-full w-64 bg-surface shadow-xl animate-slide-in">
        <div class="p-4 border-b border-border flex items-center justify-between">
          <span class="font-serif text-lg font-medium">菜单</span>
          <button onclick="toggleMobileMenu()" class="text-text-muted hover:text-text"><svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M6 18L18 6M6 6l12 12"></path></svg></button>
        </div>
        <div class="p-4 space-y-2">
          <a href="#/dashboard" onclick="toggleMobileMenu()" class="mobile-nav-link block px-3 py-2 rounded-lg text-text-muted hover:text-text hover:bg-surface-alt transition-colors">概览</a>
          <a href="#/datasources" onclick="toggleMobileMenu()" class="mobile-nav-link block px-3 py-2 rounded-lg text-text-muted hover:text-text hover:bg-surface-alt transition-colors">数据源</a>
          <a href="#/profile" onclick="toggleMobileMenu()" class="mobile-nav-link block px-3 py-2 rounded-lg text-text-muted hover:text-text hover:bg-surface-alt transition-colors">账号</a>
          ${isAdmin() ? `
            <div class="mt-4 pt-4 border-t border-border">
              <div class="text-xs text-text-muted uppercase tracking-wider mb-2">管理</div>
              <a href="#/admin" onclick="toggleMobileMenu()" class="mobile-nav-link block px-3 py-2 rounded-lg text-text-muted hover:text-text hover:bg-surface-alt transition-colors">管理后台</a>
              <a href="#/admin/users" onclick="toggleMobileMenu()" class="mobile-nav-link block px-3 py-2 rounded-lg text-text-muted hover:text-text hover:bg-surface-alt transition-colors">用户管理</a>
              <a href="#/admin/settings" onclick="toggleMobileMenu()" class="mobile-nav-link block px-3 py-2 rounded-lg text-text-muted hover:text-text hover:bg-surface-alt transition-colors">系统设置</a>
              <a href="#/admin/quotas" onclick="toggleMobileMenu()" class="mobile-nav-link block px-3 py-2 rounded-lg text-text-muted hover:text-text hover:bg-surface-alt transition-colors">额度套餐</a>
              <a href="#/admin/roles" onclick="toggleMobileMenu()" class="mobile-nav-link block px-3 py-2 rounded-lg text-text-muted hover:text-text hover:bg-surface-alt transition-colors">角色管理</a>
            </div>
          ` : ''}
        </div>
      </div>
    </div>
    <main class="flex-1 max-w-6xl w-full mx-auto px-4 md:px-6 lg:px-8 py-6 md:py-8 lg:py-12">${content}</main>
  `;
  window.handleLogout = async () => {
    try { await api('/auth/logout', { method: 'POST' }); } catch (e) {}
    clearToken();
    state.user = null;
    navigate('/login');
  };
  window.toggleMobileMenu = () => {
    const menu = $('#mobile-menu');
    menu.classList.toggle('hidden');
  };
  revealObserve();
}

function renderAuth(content) {
  $('#app').innerHTML = `
    <div class="min-h-screen flex items-center justify-center px-4 py-8 md:py-12 relative overflow-hidden">
      <div class="fixed inset-0 pointer-events-none bg-[radial-gradient(circle_at_20%_30%,rgba(17,17,17,0.025)_0%,transparent_50%),radial-gradient(circle_at_80%_70%,rgba(17,17,17,0.02)_0%,transparent_50%)] animate-drift"></div>
      <div class="relative w-full max-w-md bg-surface border border-border rounded-xl p-5 md:p-8 lg:p-10">${content}</div>
    </div>
  `;
}

// ---------- Reveal Animation ----------
let io;
function revealObserve() {
  if (!io) {
    io = new IntersectionObserver(entries => {
      entries.forEach(e => {
        if (e.isIntersecting) {
          e.target.classList.add('in');
          io.unobserve(e.target);
        }
      });
    }, { threshold: 0.05 });
  }
  $$('.reveal').forEach((el, i) => {
    el.style.setProperty('--i', i % 8);
    io.observe(el);
  });
}

// ---------- Pages ----------
route('/login', () => {
  renderAuth(`
    <div class="font-serif text-2xl md:text-3xl font-medium tracking-tighter mb-2">登录</div>
    <div class="text-text-muted text-sm md:text-base mb-7">使用账号或邮箱登录 LoomProxy 控制台</div>
    <form id="login-form" class="space-y-5">
      <div>
        <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">用户名 / 邮箱</label>
        <input class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors" name="username" autocomplete="username" required>
      </div>
      <div>
        <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">密码</label>
        <input class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors" type="password" name="password" autocomplete="current-password" required>
      </div>
      <button type="submit" class="w-full bg-accent text-white px-4 py-2.5 rounded-lg text-sm font-medium hover:bg-gray-800 active:scale-[0.98] transition-all">登录</button>
    </form>
    <div class="mt-6 text-center text-sm text-text-muted">还没有账号？ <a href="#/register" class="text-text border-b border-border hover:border-text">注册新用户</a></div>
  `);
  $('#login-form').onsubmit = async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      const data = await api('/auth/login', {
        method: 'POST',
        body: JSON.stringify({ username: f.username.value, password: f.password.value }),
      });
      setToken(data.token);
      state.user = data.user;
      toast('登录成功', 'success');
      navigate(isAdmin() ? '/admin' : '/dashboard');
    } catch (err) {
      toast(err.message, 'error');
    }
  };
});

route('/register', () => {
  renderAuth(`
    <div class="font-serif text-2xl md:text-3xl font-medium tracking-tighter mb-2">注册</div>
    <div class="text-text-muted text-sm md:text-base mb-7">创建一个 LoomProxy 普通用户账号</div>
    <form id="register-form" class="space-y-5">
      <div>
        <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">用户名</label>
        <input class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors" name="username" minlength="3" maxlength="64" required>
      </div>
      <div>
        <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">邮箱（可选）</label>
        <input class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors" type="email" name="email">
      </div>
      <div>
        <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">昵称（可选）</label>
        <input class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors" name="nickname">
      </div>
      <div>
        <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">密码（至少 6 位）</label>
        <input class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors" type="password" name="password" minlength="6" required>
      </div>
      <button type="submit" class="w-full bg-accent text-white px-4 py-2.5 rounded-lg text-sm font-medium hover:bg-gray-800 active:scale-[0.98] transition-all">注册</button>
    </form>
    <div class="mt-6 text-center text-sm text-text-muted">已有账号？ <a href="#/login" class="text-text border-b border-border hover:border-text">返回登录</a></div>
  `);
  $('#register-form').onsubmit = async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      const data = await api('/auth/register', {
        method: 'POST',
        body: JSON.stringify({
          username: f.username.value,
          email: f.email.value,
          nickname: f.nickname.value,
          password: f.password.value,
        }),
      });
      setToken(data.token);
      state.user = data.user;
      toast('注册成功', 'success');
      navigate('/dashboard');
    } catch (err) {
      toast(err.message, 'error');
    }
  };
});

route('/dashboard', () => {
  const u = state.user;
  const rolesHtml = (u.roles || []).map(r => roleTag(r.code)).join(' ');
  renderShell(`
    <div class="reveal">
      <h1 class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-tight mb-2">概览</h1>
      <p class="text-text-muted text-sm md:text-base mb-8 md:mb-10">欢迎回来，${escapeHtml(u.nickname || u.username)}。</p>
    </div>
    <div class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 md:gap-6">
      <div class="reveal bg-surface border border-border rounded-xl p-6 hover:shadow-[0_2px_8px_rgba(0,0,0,0.04)] transition-all">
        <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">用户名</div>
        <div class="font-serif text-xl md:text-2xl font-medium tracking-tight">${escapeHtml(u.username)}</div>
        <div class="mt-3">${rolesHtml}</div>
      </div>
      <div class="reveal bg-surface border border-border rounded-xl p-6 hover:shadow-[0_2px_8px_rgba(0,0,0,0.04)] transition-all">
        <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">账号 ID</div>
        <div class="font-mono text-xl md:text-2xl">${u.id}</div>
        <div class="mt-3 text-sm text-text-muted">注册于 ${fmtDate(u.created_at)}</div>
      </div>
      <div class="reveal bg-surface border border-border rounded-xl p-6 cursor-pointer hover:shadow-[0_2px_8px_rgba(0,0,0,0.04)] active:scale-[0.99] transition-all" onclick="location.hash='#/datasources'">
        <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">数据源</div>
        <div class="font-serif text-xl md:text-2xl font-medium tracking-tight">浏览</div>
        <div class="mt-3 text-sm text-text-muted">查看可用数据源与搜索分类</div>
      </div>
      <div class="reveal bg-surface border border-border rounded-xl p-6 cursor-pointer hover:shadow-[0_2px_8px_rgba(0,0,0,0.04)] active:scale-[0.99] transition-all" onclick="location.hash='#/profile'">
        <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">账号设置</div>
        <div class="font-serif text-xl md:text-2xl font-medium tracking-tight">管理</div>
        <div class="mt-3 text-sm text-text-muted">修改密码、个人信息</div>
      </div>
    </div>
    <div class="reveal mt-12 md:mt-16">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">API 调用</h2>
      <div class="bg-surface border border-border rounded-xl p-6">
        <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">Bearer Token</div>
        <div class="font-mono text-xs md:text-sm word-break-all text-text-muted mt-2">${escapeHtml(getToken())}</div>
        <div class="mt-3 text-sm text-text-muted">在请求头中携带 <kbd class="px-1.5 py-0.5 border border-border rounded bg-surface-alt font-mono text-xs">Authorization: Bearer &lt;token&gt;</kbd> 即可访问受保护接口。</div>
      </div>
    </div>
  `);
});

route('/datasources', async () => {
  renderShell(`
    <div class="reveal">
      <h1 class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-tight mb-2">数据源</h1>
      <p class="text-text-muted text-sm md:text-base mb-8 md:mb-10">所有可用的上游数据源与搜索分类。</p>
    </div>
    <div id="ds-list" class="text-center py-12 text-text-muted">加载中...</div>
  `);
  try {
    const data = await api('/datasources');
    const list = data || [];
    const groups = {};
    list.forEach(d => {
      const cat = d.category || 'other';
      if (!groups[cat]) groups[cat] = [];
      groups[cat].push(d);
    });
    const catNames = { fq: '番茄系', qq: 'QQ阅读', qm: '七猫', sq: '书旗', uxx: '海阔视界', other: '其他' };
    let html = '';
    Object.keys(groups).forEach(cat => {
      html += `<div class="mt-8 md:mt-10 reveal">
        <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">${catNames[cat] || cat}</div>
        <div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 md:gap-6">`;
      groups[cat].forEach(d => {
        const tabs = (d.search_tab || []).map(t => `<span class="px-2 py-1 bg-surface-alt border border-border rounded text-xs font-mono">${escapeHtml(t.name)} · ${t.tab_type}</span>`).join('');
        const files = (d.files || []).map(f => `<span class="px-2 py-1 bg-surface-alt border border-border rounded text-xs font-mono">${escapeHtml(f)}.json</span>`).join('');
        html += `<div class="reveal bg-surface border border-border rounded-xl p-5 md:p-6">
          <div class="font-mono text-xs text-text-muted mb-2">${escapeHtml(d.id)}</div>
          <div class="font-serif text-lg md:text-xl font-medium tracking-tight mb-3">${escapeHtml(d.name)}</div>
          <div class="flex flex-wrap gap-2">${tabs}</div>
          ${files ? `<div class="flex flex-wrap gap-2 mt-3">${files}</div>` : ''}
        </div>`;
      });
      html += `</div></div>`;
    });
    $('#ds-list').innerHTML = html || '<div class="text-center py-12 text-text-muted">无数据源</div>';
    revealObserve();
  } catch (err) {
    $('#ds-list').innerHTML = `<div class="text-center py-12 text-text-muted">加载失败：${escapeHtml(err.message)}</div>`;
  }
});

route('/profile', () => {
  const u = state.user;
  const rolesHtml = (u.roles || []).map(r => roleTag(r.code)).join(' ');
  renderShell(`
    <div class="reveal">
      <h1 class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-tight mb-2">账号</h1>
      <p class="text-text-muted text-sm md:text-base mb-8 md:mb-10">个人信息与密码管理。</p>
    </div>
    <div class="mt-8 md:mt-10 reveal">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">基本信息</h2>
      <div class="bg-surface border border-border rounded-xl p-5 md:p-6 space-y-3">
        <div class="flex justify-between items-center"><span class="text-text-muted">用户名</span><span class="font-mono">${escapeHtml(u.username)}</span></div>
        <div class="flex justify-between items-center"><span class="text-text-muted">邮箱</span><span class="font-mono">${escapeHtml(u.email || '—')}</span></div>
        <div class="flex justify-between items-center"><span class="text-text-muted">昵称</span><span>${escapeHtml(u.nickname || '—')}</span></div>
        <div class="flex justify-between items-center"><span class="text-text-muted">角色</span><span>${rolesHtml}</span></div>
        <div class="flex justify-between items-center"><span class="text-text-muted">注册时间</span><span class="font-mono">${fmtDate(u.created_at)}</span></div>
      </div>
    </div>
    <div class="mt-8 md:mt-10 reveal">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">修改密码</h2>
      <form id="pwd-form" class="space-y-5 max-w-md">
        <div>
          <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">当前密码</label>
          <input class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors" type="password" name="old_password" required>
        </div>
        <div>
          <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">新密码（至少 6 位）</label>
          <input class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors" type="password" name="new_password" minlength="6" required>
        </div>
        <button type="submit" class="bg-accent text-white px-5 py-2.5 rounded-lg text-sm font-medium hover:bg-gray-800 active:scale-[0.98] transition-all">更新密码</button>
      </form>
    </div>
  `);
  $('#pwd-form').onsubmit = async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      await api('/auth/password', {
        method: 'POST',
        body: JSON.stringify({ old_password: f.old_password.value, new_password: f.new_password.value }),
      });
      toast('密码已更新', 'success');
      f.reset();
    } catch (err) {
      toast(err.message, 'error');
    }
  };
});

route('/admin', async () => {
  renderShell(`
    <div class="reveal">
      <h1 class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-tight mb-2">管理后台</h1>
      <p class="text-text-muted text-sm md:text-base mb-8 md:mb-10">站点运营数据与快捷入口。</p>
    </div>
    <div id="stats-grid" class="grid grid-cols-2 lg:grid-cols-4 gap-4 md:gap-6">
      <div class="bg-surface border border-border rounded-xl p-6 text-center"><span class="animate-spin-custom inline-block w-3.5 h-3.5 border-2 border-border border-t-text rounded-full"></span></div>
      <div class="bg-surface border border-border rounded-xl p-6 text-center"><span class="animate-spin-custom inline-block w-3.5 h-3.5 border-2 border-border border-t-text rounded-full"></span></div>
      <div class="bg-surface border border-border rounded-xl p-6 text-center"><span class="animate-spin-custom inline-block w-3.5 h-3.5 border-2 border-border border-t-text rounded-full"></span></div>
      <div class="bg-surface border border-border rounded-xl p-6 text-center"><span class="animate-spin-custom inline-block w-3.5 h-3.5 border-2 border-border border-t-text rounded-full"></span></div>
    </div>
    <div class="mt-8 md:mt-10 reveal">
      <h2 class="font-serif text-xl md:text-2xl font-medium tracking-tight mb-5 pb-3 border-b border-border">快捷入口</h2>
      <div class="grid grid-cols-1 md:grid-cols-3 gap-4 md:gap-6">
        <div class="reveal bg-surface border border-border rounded-xl p-6 cursor-pointer hover:shadow-[0_2px_8px_rgba(0,0,0,0.04)] active:scale-[0.99] transition-all" onclick="location.hash='#/admin/users'">
          <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">用户管理</div>
          <div class="font-serif text-xl font-medium tracking-tight">查看</div>
          <div class="mt-3 text-sm text-text-muted">用户列表、角色分配、重置密码</div>
        </div>
        <div class="reveal bg-surface border border-border rounded-xl p-6 cursor-pointer hover:shadow-[0_2px_8px_rgba(0,0,0,0.04)] active:scale-[0.99] transition-all" onclick="location.hash='#/admin/settings'">
          <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">系统设置</div>
          <div class="font-serif text-xl font-medium tracking-tight">配置</div>
          <div class="mt-3 text-sm text-text-muted">注册开关、默认角色等</div>
        </div>
        <div class="reveal bg-surface border border-border rounded-xl p-6 cursor-pointer hover:shadow-[0_2px_8px_rgba(0,0,0,0.04)] active:scale-[0.99] transition-all" onclick="location.hash='#/admin/quotas'">
          <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">额度套餐</div>
          <div class="font-serif text-xl font-medium tracking-tight">查看</div>
          <div class="mt-3 text-sm text-text-muted">各套餐调用限制</div>
        </div>
      </div>
    </div>
  `);
  try {
    const s = await api('/admin/stats');
    const cards = [
      ['总用户数', s.total_users],
      ['今日新增', s.today_new],
      ['管理员', s.admin_count],
      ['VIP 用户', s.vip_count],
    ];
    $('#stats-grid').innerHTML = cards.map(([label, val]) => `
      <div class="reveal bg-surface border border-border rounded-xl p-6">
        <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">${label}</div>
        <div class="font-serif text-3xl md:text-4xl font-medium tracking-tighter">${val}</div>
      </div>
    `).join('');
    revealObserve();
  } catch (err) {
    $('#stats-grid').innerHTML = `<div class="col-span-full text-center py-12 text-text-muted">加载失败：${escapeHtml(err.message)}</div>`;
  }
});

route('/admin/users', async (query) => {
  const page = parseInt(query.page || '1', 10);
  renderShell(`
    <div class="reveal">
      <h1 class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-tight mb-2">用户管理</h1>
      <p class="text-text-muted text-sm md:text-base mb-6">所有注册用户。</p>
    </div>
    <div class="reveal flex flex-col sm:flex-row gap-3 mb-6">
      <input id="user-search" placeholder="搜索用户名 / 邮箱 / 昵称" value="${escapeHtml(query.keyword || '')}" class="flex-1 px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors">
      <button onclick="handleUserSearch()" class="bg-accent text-white px-5 py-2.5 rounded-lg text-sm font-medium hover:bg-gray-800 active:scale-[0.98] transition-all whitespace-nowrap">搜索</button>
    </div>
    <div id="user-table" class="text-center py-12 text-text-muted">加载中...</div>
  `);
  window.handleUserSearch = () => {
    const kw = $('#user-search').value.trim();
    navigate('/admin/users?page=1' + (kw ? '&keyword=' + encodeURIComponent(kw) : ''));
  };
  $('#user-search').onkeydown = (e) => { if (e.key === 'Enter') window.handleUserSearch(); };

  try {
    const kw = query.keyword || '';
    const data = await api('/admin/users?page=' + page + (kw ? '&keyword=' + encodeURIComponent(kw) : ''));
    const list = data.list || [];
    const total = data.total || 0;
    const pageSize = data.page_size || 20;
    const totalPages = Math.max(1, Math.ceil(total / pageSize));

    if (list.length === 0) {
      $('#user-table').innerHTML = '<div class="text-center py-12 text-text-muted">无用户</div>';
      return;
    }

    let cards = '';
    let rows = '';
    list.forEach(u => {
      const roles = (u.roles || []).map(r => roleTag(r.code)).join(' ');
      cards += `<div class="reveal bg-surface border border-border rounded-xl p-4 md:p-5 mb-3 sm:hidden">
        <div class="flex justify-between items-start mb-3">
          <div>
            <div class="font-medium text-base">${escapeHtml(u.username)}</div>
            <div class="font-mono text-xs text-text-muted">#${u.id}</div>
          </div>
          ${statusTag(u.status)}
        </div>
        <div class="space-y-2 text-sm">
          ${u.email ? `<div><span class="text-text-muted">邮箱：</span>${escapeHtml(u.email)}</div>` : ''}
          ${u.nickname ? `<div><span class="text-text-muted">昵称：</span>${escapeHtml(u.nickname)}</div>` : ''}
          <div><span class="text-text-muted">角色：</span>${roles}</div>
          <div><span class="text-text-muted">注册：</span>${fmtDate(u.created_at)}</div>
        </div>
        <div class="flex flex-wrap gap-3 mt-4 pt-3 border-t border-border">
          <button data-act="edit" data-id="${u.id}" class="flex-1 py-2 border border-border rounded-lg text-xs font-medium hover:bg-surface-alt transition-colors">编辑</button>
          <button data-act="roles" data-id="${u.id}" class="flex-1 py-2 border border-border rounded-lg text-xs font-medium hover:bg-surface-alt transition-colors">角色</button>
          <button data-act="pwd" data-id="${u.id}" class="flex-1 py-2 border border-border rounded-lg text-xs font-medium hover:bg-surface-alt transition-colors">重置密码</button>
          <button data-act="del" data-id="${u.id}" class="flex-1 py-2 border border-pale-red-bg text-pale-red-fg rounded-lg text-xs font-medium hover:bg-pale-red-bg transition-colors">删除</button>
        </div>
      </div>`;
      rows += `<tr class="border-b border-border hover:bg-surface-alt/50 transition-colors">
        <td class="font-mono text-xs md:text-sm px-3 md:px-5 py-3 md:py-4">${u.id}</td>
        <td class="px-3 md:px-5 py-3 md:py-4 font-medium">${escapeHtml(u.username)}</td>
        <td class="px-3 md:px-5 py-3 md:py-4 text-text-muted text-sm hidden sm:table-cell">${escapeHtml(u.email || '—')}</td>
        <td class="px-3 md:px-5 py-3 md:py-4 text-sm hidden md:table-cell">${escapeHtml(u.nickname || '—')}</td>
        <td class="px-3 md:px-5 py-3 md:py-4">${roles}</td>
        <td class="px-3 md:px-5 py-3 md:py-4">${statusTag(u.status)}</td>
        <td class="px-3 md:px-5 py-3 md:py-4 font-mono text-xs text-text-muted hidden lg:table-cell">${fmtDate(u.created_at)}</td>
        <td class="px-3 md:px-5 py-3 md:py-4">
          <div class="flex flex-wrap gap-2">
            <button data-act="edit" data-id="${u.id}" class="text-xs md:text-sm text-text-muted hover:text-text transition-colors">编辑</button>
            <button data-act="roles" data-id="${u.id}" class="text-xs md:text-sm text-text-muted hover:text-text transition-colors">角色</button>
            <button data-act="pwd" data-id="${u.id}" class="text-xs md:text-sm text-text-muted hover:text-text transition-colors">重置密码</button>
            <button data-act="del" data-id="${u.id}" class="text-xs md:text-sm text-pale-red-fg hover:text-red-700 transition-colors">删除</button>
          </div>
        </td>
      </tr>`;
    });

    let pagHtml = '';
    if (totalPages > 1) {
      pagHtml = '<div class="flex justify-end gap-1.5 mt-4">';
      for (let i = 1; i <= totalPages; i++) {
        pagHtml += `<button onclick="location.hash='#/admin/users?page=${i}${kw ? '&keyword=' + encodeURIComponent(kw) : ''}'" class="px-3 py-1.5 border border-border rounded text-xs font-mono ${i === page ? 'bg-text text-white border-text' : 'hover:bg-surface-alt'}">${i}</button>`;
      }
      pagHtml += '</div>';
    }

    $('#user-table').innerHTML = `
      <div class="sm:hidden">${cards}</div>
      <div class="hidden sm:block reveal bg-surface border border-border rounded-xl overflow-hidden">
        <table class="w-full text-sm">
          <thead class="bg-surface-alt">
            <tr>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">ID</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">用户名</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left hidden sm:table-cell">邮箱</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left hidden md:table-cell">昵称</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">角色</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">状态</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left hidden lg:table-cell">注册时间</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">操作</th>
            </tr>
          </thead>
          <tbody>${rows}</tbody>
        </table>
      </div>
      <div class="mt-3 text-sm text-text-muted">共 ${total} 条</div>
      ${pagHtml}
    `;
    revealObserve();

    $$('#user-table [data-act]').forEach(btn => {
      btn.onclick = () => {
        const act = btn.dataset.act;
        const id = btn.dataset.id;
        if (act === 'edit') showEditUserModal(id);
        else if (act === 'roles') showRolesModal(id);
        else if (act === 'pwd') showResetPwdModal(id);
        else if (act === 'del') showDeleteModal(id);
      };
    });
  } catch (err) {
    $('#user-table').innerHTML = `<div class="text-center py-12 text-text-muted">加载失败：${escapeHtml(err.message)}</div>`;
  }
});

async function showEditUserModal(id) {
  try {
    const u = await api('/admin/users/' + id);
    showModal(`
      <div class="font-serif text-xl font-medium tracking-tight mb-5">编辑用户 #${u.id}</div>
      <form id="edit-form" class="space-y-5">
        <div>
          <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">用户名</label>
          <input value="${escapeHtml(u.username)}" disabled class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface-alt text-sm opacity-60">
        </div>
        <div>
          <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">昵称</label>
          <input name="nickname" value="${escapeHtml(u.nickname || '')}" class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors">
        </div>
        <div>
          <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">邮箱</label>
          <input name="email" value="${escapeHtml(u.email || '')}" class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors">
        </div>
        <div>
          <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">状态</label>
          <select name="status" class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors">
            <option value="1" ${u.status === 1 ? 'selected' : ''}>启用</option>
            <option value="0" ${u.status === 0 ? 'selected' : ''}>禁用</option>
          </select>
        </div>
        <div class="flex justify-end gap-3 mt-6">
          <button type="button" onclick="closeModal()" class="px-4 py-2 border border-border rounded-lg text-sm hover:bg-surface-alt transition-colors">取消</button>
          <button type="submit" class="bg-accent text-white px-4 py-2 rounded-lg text-sm font-medium hover:bg-gray-800 active:scale-[0.98] transition-all">保存</button>
        </div>
      </form>
    `);
    $('#edit-form').onsubmit = async (e) => {
      e.preventDefault();
      const f = e.target;
      try {
        await api('/admin/users/' + id, {
          method: 'PATCH',
          body: JSON.stringify({
            nickname: f.nickname.value,
            email: f.email.value,
            status: parseInt(f.status.value, 10),
          }),
        });
        toast('已更新', 'success');
        closeModal();
        handleHash();
      } catch (err) {
        toast(err.message, 'error');
      }
    };
  } catch (err) {
    toast(err.message, 'error');
  }
}

async function showRolesModal(id) {
  try {
    const [user, roles] = await Promise.all([
      api('/admin/users/' + id),
      api('/admin/roles'),
    ]);
    const userCodes = new Set((user.roles || []).map(r => r.code));
    const checks = (roles || []).map(r => `
      <label class="flex items-center gap-2 mb-3 cursor-pointer">
        <input type="checkbox" name="role" value="${escapeHtml(r.code)}" ${userCodes.has(r.code) ? 'checked' : ''} class="w-4 h-4 rounded border-border text-text focus:ring-text">
        <span>${roleTag(r.code)} <span class="text-text-muted ml-2 text-sm">${escapeHtml(r.name)}</span></span>
      </label>
    `).join('');
    showModal(`
      <div class="font-serif text-xl font-medium tracking-tight mb-5">修改角色 — ${escapeHtml(user.username)}</div>
      <form id="roles-form" class="space-y-2">
        ${checks}
        <div class="flex justify-end gap-3 mt-6">
          <button type="button" onclick="closeModal()" class="px-4 py-2 border border-border rounded-lg text-sm hover:bg-surface-alt transition-colors">取消</button>
          <button type="submit" class="bg-accent text-white px-4 py-2 rounded-lg text-sm font-medium hover:bg-gray-800 active:scale-[0.98] transition-all">保存</button>
        </div>
      </form>
    `);
    $('#roles-form').onsubmit = async (e) => {
      e.preventDefault();
      const codes = $$('#roles-form [name=role]:checked').map(c => c.value);
      try {
        await api('/admin/users/' + id + '/roles', {
          method: 'POST',
          body: JSON.stringify({ role_codes: codes }),
        });
        toast('角色已更新', 'success');
        closeModal();
      } catch (err) {
        toast(err.message, 'error');
      }
    };
  } catch (err) {
    toast(err.message, 'error');
  }
}

function showResetPwdModal(id) {
  showModal(`
    <div class="font-serif text-xl font-medium tracking-tight mb-5">重置密码 #${id}</div>
    <form id="pwd-reset-form" class="space-y-5">
      <div>
        <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">新密码（至少 6 位）</label>
        <input type="password" name="new_password" minlength="6" required class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors">
      </div>
      <div class="flex justify-end gap-3 mt-6">
        <button type="button" onclick="closeModal()" class="px-4 py-2 border border-border rounded-lg text-sm hover:bg-surface-alt transition-colors">取消</button>
        <button type="submit" class="bg-accent text-white px-4 py-2 rounded-lg text-sm font-medium hover:bg-gray-800 active:scale-[0.98] transition-all">重置</button>
      </div>
    </form>
  `);
  $('#pwd-reset-form').onsubmit = async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      await api('/admin/users/' + id + '/reset-password', {
        method: 'POST',
        body: JSON.stringify({ new_password: f.new_password.value }),
      });
      toast('密码已重置', 'success');
      closeModal();
    } catch (err) {
      toast(err.message, 'error');
    }
  };
}

function showDeleteModal(id) {
  showModal(`
    <div class="font-serif text-xl font-medium tracking-tight mb-5">删除用户 #${id}</div>
    <p class="text-text-muted text-sm mb-6">此操作将软删除该用户，无法在控制台登录。确定继续？</p>
    <div class="flex justify-end gap-3">
      <button onclick="closeModal()" class="px-4 py-2 border border-border rounded-lg text-sm hover:bg-surface-alt transition-colors">取消</button>
      <button id="confirm-del" class="px-4 py-2 border border-pale-red-bg text-pale-red-fg rounded-lg text-sm hover:bg-pale-red-bg transition-colors">删除</button>
    </div>
  `);
  $('#confirm-del').onclick = async () => {
    try {
      await api('/admin/users/' + id, { method: 'DELETE' });
      toast('已删除', 'success');
      closeModal();
      handleHash();
    } catch (err) {
      toast(err.message, 'error');
    }
  };
}

function showModal(html) {
  const mask = document.createElement('div');
  mask.className = 'fixed inset-0 bg-black/40 flex items-end sm:items-center justify-center z-50 animate-fade';
  mask.id = 'modal-mask';
  mask.innerHTML = `<div class="bg-surface border-t sm:border border-border rounded-t-xl sm:rounded-xl p-5 sm:p-6 md:p-8 w-full sm:w-full max-w-md animate-rise">${html}</div>`;
  mask.onclick = (e) => { if (e.target === mask) closeModal(); };
  document.body.appendChild(mask);
  document.body.style.overflow = 'hidden';
}
function closeModal() {
  const m = $('#modal-mask');
  if (m) m.remove();
  document.body.style.overflow = '';
}

route('/admin/settings', async () => {
  renderShell(`
    <div class="reveal">
      <h1 class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-tight mb-2">系统设置</h1>
      <p class="text-text-muted text-sm md:text-base mb-8 md:mb-10">站点运行时配置项。</p>
    </div>
    <div id="settings-list" class="text-center py-12 text-text-muted">加载中...</div>
  `);
  try {
    const list = await api('/admin/settings');
    if (!list || list.length === 0) {
      $('#settings-list').innerHTML = '<div class="text-center py-12 text-text-muted">无设置项</div>';
      return;
    }
    let cards = '';
    let rows = '';
    list.forEach(s => {
      const valDisplay = s.type === 'bool'
        ? (s.value === 'true' ? '<span class="px-2.5 py-0.5 rounded-full text-xs font-medium uppercase tracking-wider bg-pale-green-bg text-pale-green-fg">true</span>' : '<span class="px-2.5 py-0.5 rounded-full text-xs font-medium uppercase tracking-wider bg-pale-red-bg text-pale-red-fg">false</span>')
        : `<span class="font-mono text-sm">${escapeHtml(s.value)}</span>`;
      cards += `<div class="reveal bg-surface border border-border rounded-xl p-4 mb-3 sm:hidden">
        <div class="flex justify-between items-start mb-3">
          <div class="font-mono text-sm">${escapeHtml(s.key)}</div>
          <button data-key="${escapeHtml(s.key)}" data-val="${escapeHtml(s.value)}" class="text-xs px-3 py-1.5 border border-border rounded-lg hover:bg-surface-alt transition-colors">编辑</button>
        </div>
        <div class="space-y-2 text-sm">
          <div><span class="text-text-muted">值：</span>${valDisplay}</div>
          <div><span class="text-text-muted">说明：</span>${escapeHtml(s.description || '—')}</div>
          <div><span class="text-text-muted">类型：</span><span class="font-mono text-xs">${escapeHtml(s.type || 'string')}</span></div>
        </div>
      </div>`;
      rows += `<tr class="border-b border-border">
        <td class="font-mono text-sm px-3 md:px-5 py-3 md:py-4">${escapeHtml(s.key)}</td>
        <td class="px-3 md:px-5 py-3 md:py-4">${valDisplay}</td>
        <td class="px-3 md:px-5 py-3 md:py-4 text-text-muted text-sm">${escapeHtml(s.description || '—')}</td>
        <td class="px-3 md:px-5 py-3 md:py-4 font-mono text-xs text-text-muted">${escapeHtml(s.type || 'string')}</td>
        <td class="px-3 md:px-5 py-3 md:py-4"><button data-key="${escapeHtml(s.key)}" data-val="${escapeHtml(s.value)}" class="text-sm text-text-muted hover:text-text transition-colors">编辑</button></td>
      </tr>`;
    });
    $('#settings-list').innerHTML = `
      <div class="sm:hidden">${cards}</div>
      <div class="hidden sm:block reveal bg-surface border border-border rounded-xl overflow-hidden">
        <table class="w-full text-sm">
          <thead class="bg-surface-alt">
            <tr>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">键</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">当前值</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">说明</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">类型</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">操作</th>
            </tr>
          </thead>
          <tbody>${rows}</tbody>
        </table>
      </div>`;
    revealObserve();
    $$('#settings-list [data-key]').forEach(btn => {
      btn.onclick = () => showEditSettingModal(btn.dataset.key, btn.dataset.val);
    });
  } catch (err) {
    $('#settings-list').innerHTML = `<div class="text-center py-12 text-text-muted">加载失败：${escapeHtml(err.message)}</div>`;
  }
});

function showEditSettingModal(key, val) {
  showModal(`
    <div class="font-serif text-xl font-medium tracking-tight mb-5">编辑 ${escapeHtml(key)}</div>
    <form id="setting-form" class="space-y-5">
      <div>
        <label class="block text-xs uppercase tracking-wider text-text-muted mb-2">值</label>
        <input name="value" value="${escapeHtml(val)}" required class="w-full px-4 py-2.5 border border-border rounded-lg bg-surface text-sm focus:outline-none focus:border-text transition-colors">
      </div>
      <div class="flex justify-end gap-3 mt-6">
        <button type="button" onclick="closeModal()" class="px-4 py-2 border border-border rounded-lg text-sm hover:bg-surface-alt transition-colors">取消</button>
        <button type="submit" class="bg-accent text-white px-4 py-2 rounded-lg text-sm font-medium hover:bg-gray-800 active:scale-[0.98] transition-all">保存</button>
      </div>
    </form>
  `);
  $('#setting-form').onsubmit = async (e) => {
    e.preventDefault();
    const f = e.target;
    try {
      await api('/admin/settings/' + encodeURIComponent(key), {
        method: 'PUT',
        body: JSON.stringify({ value: f.value.value }),
      });
      toast('已更新', 'success');
      closeModal();
      handleHash();
    } catch (err) {
      toast(err.message, 'error');
    }
  };
}

route('/admin/quotas', async () => {
  renderShell(`
    <div class="reveal">
      <h1 class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-tight mb-2">额度套餐</h1>
      <p class="text-text-muted text-sm md:text-base mb-8 md:mb-10">各套餐的调用限制配置。</p>
    </div>
    <div id="quota-list" class="text-center py-12 text-text-muted">加载中...</div>
  `);
  try {
    const plans = await api('/admin/quotas/plans');
    if (!plans || plans.length === 0) {
      $('#quota-list').innerHTML = '<div class="text-center py-12 text-text-muted">无套餐</div>';
      return;
    }
    const cards = await Promise.all(plans.map(async p => {
      const limits = await api('/admin/quotas/plans/' + p.id + '/limits').catch(() => []);
      const limitsHtml = (limits || []).map(l => `
        <div class="flex justify-between items-center mb-2">
          <span class="font-mono text-xs text-text-muted">${escapeHtml(l.scope)} / ${escapeHtml(l.target)}</span>
          <span class="font-mono text-sm">${l.limit < 0 ? '∞' : l.limit} / ${escapeHtml(l.period)}</span>
        </div>
      `).join('');
      return `<div class="reveal bg-surface border border-border rounded-xl p-6">
        <div class="font-mono text-xs uppercase tracking-wider text-text-muted mb-3">${escapeHtml(p.code)}</div>
        <div class="font-serif text-xl font-medium tracking-tight mb-2">${escapeHtml(p.name)}</div>
        <div class="text-sm text-text-muted mb-4">${escapeHtml(p.description || '—')}</div>
        ${limitsHtml || '<div class="text-center py-4 text-text-muted text-sm">无限制项</div>'}
      </div>`;
    }));
    $('#quota-list').innerHTML = `<div class="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4 md:gap-6">${cards.join('')}</div>`;
    revealObserve();
  } catch (err) {
    $('#quota-list').innerHTML = `<div class="text-center py-12 text-text-muted">加载失败：${escapeHtml(err.message)}</div>`;
  }
});

route('/admin/roles', async () => {
  renderShell(`
    <div class="reveal">
      <h1 class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-tight mb-2">角色</h1>
      <p class="text-text-muted text-sm md:text-base mb-8 md:mb-10">系统内置角色。</p>
    </div>
    <div id="roles-list" class="text-center py-12 text-text-muted">加载中...</div>
  `);
  try {
    const list = await api('/admin/roles');
    const cards = (list || []).map(r => `
      <div class="reveal bg-surface border border-border rounded-xl p-4 mb-3 sm:hidden">
        <div class="flex justify-between items-start mb-3">
          <div>
            <div class="font-medium">${escapeHtml(r.name)}</div>
            <div class="font-mono text-xs text-text-muted">#${r.id}</div>
          </div>
          ${statusTag(r.status)}
        </div>
        <div class="space-y-2 text-sm">
          <div>${roleTag(r.code)}</div>
          <div><span class="text-text-muted">说明：</span>${escapeHtml(r.description || '—')}</div>
        </div>
      </div>`).join('');
    const rows = (list || []).map(r => `
      <tr class="border-b border-border">
        <td class="font-mono text-sm px-3 md:px-5 py-3 md:py-4">${r.id}</td>
        <td class="px-3 md:px-5 py-3 md:py-4">${roleTag(r.code)}</td>
        <td class="px-3 md:px-5 py-3 md:py-4 font-medium">${escapeHtml(r.name)}</td>
        <td class="px-3 md:px-5 py-3 md:py-4 text-text-muted text-sm">${escapeHtml(r.description || '—')}</td>
        <td class="px-3 md:px-5 py-3 md:py-4">${statusTag(r.status)}</td>
      </tr>
    `).join('');
    $('#roles-list').innerHTML = `
      <div class="sm:hidden">${cards}</div>
      <div class="hidden sm:block reveal bg-surface border border-border rounded-xl overflow-hidden">
        <table class="w-full text-sm">
          <thead class="bg-surface-alt">
            <tr>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">ID</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">代码</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">名称</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:py-4 text-left">说明</th>
              <th class="font-mono text-xs uppercase tracking-wider text-text-muted px-3 md:px-5 py-3 md:px-4 text-left">状态</th>
            </tr>
          </thead>
          <tbody>${rows}</tbody>
        </table>
      </div>`;
    revealObserve();
  } catch (err) {
    $('#roles-list').innerHTML = `<div class="text-center py-12 text-text-muted">加载失败：${escapeHtml(err.message)}</div>`;
  }
});

route('/404', () => {
  renderShell(`
    <div class="font-serif text-3xl md:text-4xl lg:text-5xl font-medium tracking-tighter leading-tight mb-2">404</div>
    <p class="text-text-muted text-sm md:text-base mb-8">页面不存在。</p>
    <a href="#/dashboard" class="bg-accent text-white px-5 py-2.5 rounded-lg text-sm font-medium hover:bg-gray-800 active:scale-[0.98] transition-all">返回概览</a>
  `);
});

// ---------- Boot ----------
async function boot() {
  await loadMe();
  if (!state.user) {
    if (!location.hash || location.hash === '#/') location.hash = '#/login';
  } else if (!location.hash || location.hash === '#/') {
    location.hash = isAdmin() ? '#/admin' : '#/dashboard';
  }
  handleHash();
}

window.addEventListener('hashchange', handleHash);
window.addEventListener('DOMContentLoaded', boot);
