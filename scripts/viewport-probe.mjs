#!/usr/bin/env node
// 按视口走一遍面板所有路由，读每一页的横向溢出——这条探针的来路是待办清单 P65 那一族：
// **窄屏溢出构建、用例、css-check 全都不报**，只能被"真的按那个宽度看一次"发现。
// 判据页那条（《窄屏页面横向溢出…》）写的固化形态就是这一页，条件是"第二次需要时再定"——
// P65/P66/P96/P97/P99 已经撞到第五次了，所以它落进仓库，但**不挂进 make vet**：
// 它是分钟级、要浏览器与一份临时实例的东西，挂在构建门禁上会把门禁变成等待。
//
// 用法（配合一份临时实例，绝不在生产上跑）：
//   cd /tmp && npm --prefix /tmp i playwright-core      # 驱动库不进仓库依赖
//   ./scripts/run-viewport-probe.sh                    # 起临时实例 + 跑本脚本 + 收尾
// 或手动：
//   BASE=http://127.0.0.1:PORT ADMIN_USER=admin ADMIN_PASS=xxx node scripts/viewport-probe.mjs
//
// 输出是**文本表**（溢出的元素与像素数），截图只是给人复看的附件——脚本自己不解读像素。
// 退出码：0 = 全部路由在该宽度不溢出；1 = 有溢出（逐行点名元素）；2 = 起不来/登录不上。

import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';

const BASE = (process.env.BASE || 'http://127.0.0.1:8081').replace(/\/$/, '');
const ADMIN_USER = process.env.ADMIN_USER || '';
const ADMIN_PASS = process.env.ADMIN_PASS || '';
const WIDTHS = (process.env.WIDTHS || '390,1280').split(',').map((w) => parseInt(w, 10));
const OUT = process.env.OUT || '/tmp/viewport-probe';
const HEIGHT = parseInt(process.env.HEIGHT || '844', 10);
const TOKEN_KEY = 'loomproxy_token'; // 与 web/src/api/client.js 的 TOKEN_KEY 同一个名字

// 路由表从源码里读，不抄第二份：抄一次就漂一次（P65 那族里"面板显示的那一列没有数据源"是同一条判据）。
function routesFromSource() {
  const main = fs.readFileSync(path.resolve('web/src/main.js'), 'utf8');
  const start = main.indexOf('const routes = [');
  // 块尾按"行首的 ]"找：`createRouter` 这个词在第 2 行的 import 里就出现过，
  // 拿它当右边界会切出一个空块——第一版就是这么空的（探针自己那条"空转"守卫当场拦下）
  const end = main.indexOf('\n]', start);
  const block = main.slice(start, end);
  const out = [];
  for (const m of block.matchAll(/\{\s*path:\s*'([^']+)'[^}]*\}/g)) {
    const seg = m[0];
    if (/redirect:/.test(seg)) continue;              // '/' 只是转发到 /dashboard
    if (/public:\s*true/.test(seg)) continue;         // 登录/注册/找回不要求会话
    if (/[:*]/.test(m[1])) continue;                   // 参数路由与通配 404 不去量（没有真实数据可渲染）
    out.push(m[1]);
  }
  return out;
}

async function login() {
  const res = await fetch(`${BASE}/auth/login`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username: ADMIN_USER, password: ADMIN_PASS }),
  });
  const body = await res.json().catch(() => null);
  if (!res.ok || !body || body.code !== 0 || !body.data || !body.data.token) {
    console.error(`登录失败：status=${res.status} body=${JSON.stringify(body).slice(0, 200)}`);
    process.exit(2);
  }
  return body.data.token;
}

function chromePath() {
  if (process.env.CHROME) return process.env.CHROME;
  const root = path.join(os.homedir(), '.cache/ms-playwright');
  for (const dir of fs.readdirSync(root).sort().reverse()) {
    for (const cand of [`${dir}/chrome-linux64/chrome`, `${dir}/chrome-linux/chrome`]) {
      const p = path.join(root, cand);
      if (fs.existsSync(p)) return p;
    }
  }
  console.error('找不到 chromium：设 CHROME=<可执行文件路径>（判据不许写"本环境没有浏览器"，见踩坑判据《阻塞项》那条）');
  process.exit(2);
}

// 溢出判据：整页 scrollWidth 超过视口，或者任何**可见叶子控件**的右边界越过视口右沿，
// 而且**它不在某个能横向滚动的祖先里**。最后这半是第一次跑就教出来的：
// /admin/users 的表格右沿 1395 > 1280，而整页 scrollWidth=1280——那是表体自己可横滚，
// 用户滚得到就不算缺陷；把它报成缺陷，下一轮就没人信这条探针了。
const MEASURE = `(() => {
  const vw = window.innerWidth;
  const doc = document.scrollingElement || document.documentElement;
  const page = { scrollWidth: doc.scrollWidth, innerWidth: vw, overflow: doc.scrollWidth > vw + 1 };
  const reachable = (el) => {
    for (let p = el.parentElement; p; p = p.parentElement) {
      const st = getComputedStyle(p);
      const pr = p.getBoundingClientRect();
      // 只有**装得进视口**的那个滚动容器才算"到达得了"。整页的滚动条不算：
      // 一条 overflow-y:auto 会让 overflow-x 的计算值变成 auto（CSS 规定一个轴非 visible 时另一个也是），
      // 于是"页面自己能横滚"永远成立，判据就退化成只看 scrollWidth 那一个数——
      // 而第一次跑就是这么被糊过去的：种了 2400px 的违例，offenders 列表是空的。
      if ((st.overflowX === 'auto' || st.overflowX === 'scroll') &&
          p.scrollWidth > p.clientWidth + 1 && pr.right <= vw + 1) return p;
    }
    return null;
  };
  const bad = [];
  let scrolled = 0;
  for (const el of document.querySelectorAll('body *')) {
    const r = el.getBoundingClientRect();
    if (r.width === 0 || r.height === 0) continue;
    const st = getComputedStyle(el);
    if (st.visibility === 'hidden' || st.display === 'none' || parseFloat(st.opacity) === 0) continue;
    if (r.right <= vw + 1) continue;
    const cls = (el.className && typeof el.className === 'string') ? '.' + el.className.trim().split(/\\s+/).slice(0, 2).join('.') : '';
    bad.push({ tag: el.tagName.toLowerCase() + cls, right: Math.round(r.right), width: Math.round(r.width),
               blocked: !reachable(el) });
  }
  // 两种信号各有各的用法，混用就会误报：
  //   整页 scrollWidth 已经超了 → 溢出的是页面本身，此时"祖先能滚"那句永远成立（页面根就是那个滚动条），
  //     所以直接列出出界元素当线索；
  //   整页没超 → 只有"滚不到"的那部分才算缺陷，被局部横滚容器装着的（表格横向滚动条）不算。
  const pool = page.overflow ? bad : bad.filter((b) => b.blocked);
  const scrolledCount = bad.length - pool.length;
  const tops = [];
  for (const b of pool.sort((x, y) => y.right - x.right)) {
    if (tops.length >= 4) break;
    if (!tops.some((t) => t.right >= b.right && t.width >= b.width)) tops.push(b);
  }
  const text = (document.querySelector('main') || document.body).innerText.replace(/\\s+/g, ' ').slice(0, 60);
  // 注意：「main」取到的文字**不含弹窗内容**（UiModal 渲染在 main 之外）。
  //   ——这里原本写的是反引号，而**整段是一个模板字符串**：反引号会提前终结它，
  //   整个探针从 2026-10-07 那颗提交起就是语法坏的状态而没人发现（它刻意不进 make build）。
  //   所以这段注释里永远不要再出现反引号，要引用名字用「」。
  // 这一格本轮骗过我们一次：验证「限制项」表格里的一句话时读 main，读不到，判成假红
  // ——文字在页上但不在测量范围内。溢出那半边不受影响（它扫的是 body *）。
  return { page, offenders: tops, scrolledCount, text, loc: location.pathname };
})()`;

const routes = routesFromSource();
if (!routes.length) { console.error('路由表读出来是空的——探针会空转，先修它'); process.exit(2); }
fs.mkdirSync(OUT, { recursive: true });
const token = await login();

// playwright-core 是 CJS：用 createRequire 拿，`await import(...)` 解构出来的 chromium 是 undefined
// （第一版就是这么炸的——Node 的 ESM/CJS 互操作把整个模块挂在 .default 上）
const { createRequire } = await import('node:module');
const requireFn = createRequire(import.meta.url);
const { chromium } = requireFn(process.env.PW_CORE || '/tmp/node_modules/playwright-core');
const browser = await chromium.launch({
  executablePath: chromePath(),
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu'],
});

let bad = 0;
for (const width of WIDTHS) {
  const ctx = await browser.newContext({ viewport: { width, height: HEIGHT }, deviceScaleFactor: 1 });
  // 令牌要在应用脚本之前进 localStorage，否则首屏会 401 跳登录、量到的是登录页
  await ctx.addInitScript(([k, v]) => { try { localStorage.setItem(k, v); } catch (e) {} }, [TOKEN_KEY, token]);
  const page = await ctx.newPage();
  console.log(`\n== 视口 ${width}x${HEIGHT} ==`);
  for (const route of routes) {
    const url = `${BASE}/panel${route}`;
    // 第二个信号：这一页加载时它自己发的请求里有没有 ≥400。
    // 面板的常见坏形状是"页面 200 而数据全红"，只看页面状态码看不出来
    // （`开发约定` 那句"按视口走一遍所有路由、读 scrollWidth 与 ≥400 响应"要的就是这两个数）
    //
    // **但这条信号有口径边界，别把它当万能红灯**：本脚本跑在**本机裸实例**上，前面没有 nginx，
    // 所以由部署侧（前端服务器站点根）提供的静态文件必然 404 —— 那**不是缺陷**。
    // 这一条是待办清单 P107 换来的：我第一次把 `/favicon.svg` 的 404 读成"仓库少一个文件"，
    // 还动手把引用改成相对路径，而落在站点根正是维护者为了让"换图不必重建"做的设计。
    // 判法：只有本服务自己认领的路径（/panel 与 API 前缀）出 4xx 才算未达标，其余出 ℹ️ 不计数。
    const OWNED = ['^/panel', '^/auth', '^/admin', '^/quota', '^/rank', '^/datasources', '^/data', '^/verify', '^/health'];
    const httpBad = [];
    const deploySide = [];
    const onResponse = (res) => {
      const st = res.status();
      if (st < 400) return;
      let u;
      try { u = new URL(res.url()); } catch { return; }
      if (u.origin !== new URL(BASE).origin) return;   // 外部资源不归本页管
      const p = u.pathname;
      const mine = OWNED.some((rx) => new RegExp(rx).test(p));
      (mine ? httpBad : deploySide).push(`${st} ${p}`);
    };
    page.on('response', onResponse);
    try {
      await page.goto(url, { waitUntil: 'networkidle', timeout: 20000 });
    } catch (e) {
      console.log(`  ${route.padEnd(22)} 打不开：${String(e.message).split('\n')[0].slice(0, 80)}`);
      bad++;
      page.off('response', onResponse);
      continue;
    }
    const r = await page.evaluate(MEASURE);
    page.off('response', onResponse);
    const shot = path.join(OUT, `${width}${route.replace(/[^a-z0-9]+/gi, '_') || '_root'}.png`);
    await page.screenshot({ path: shot, fullPage: false });
    // 先确认"量的是这一页"：被弹回登录页时 17 条全都"不溢出"，那就是探针在空转
    // （判据页《断言空转》那一族的第三种形态——不是样本为 0，是根本没走到那一页）
    if (r.loc !== `/panel${route}`) {
      bad++;
      console.log(`  ${route.padEnd(22)} ❌ 没停在本页：location=${r.loc}（读到的不是这一页，溢出结论作废）`);
      continue;
    }
    if (r.text.length < 6) {
      bad++;
      console.log(`  ${route.padEnd(22)} ⚠ 本页文字只有「${r.text}」——渲染可能没完成，这条读数不可信`);
      continue;
    }
    const overflow = r.page.overflow || r.offenders.length;
    if (overflow || httpBad.length) {
      bad++;
      const over = overflow
        ? `溢出 scrollWidth=${r.page.scrollWidth} 视口=${r.page.innerWidth} ` +
          r.offenders.map((o) => `${o.tag}(右沿${o.right},宽${o.width})`).join(' ')
        : `不溢出 (scrollWidth=${r.page.scrollWidth}/${r.page.innerWidth})`;
      const http = httpBad.length ? `  ≥400: ${[...new Set(httpBad)].join(' ')}` : '';
      console.log(`  ${route.padEnd(22)} ${over}${http}`);
    } else {
      const hint = r.scrolledCount ? `（${r.scrolledCount} 处出界但在可横滚容器内，算到达）` : '';
      const info = deploySide.length ? `  ℹ 部署侧提供的东西本机没有：${[...new Set(deploySide)].join(' ')}` : '';
      console.log(`  ${route.padEnd(22)} 不溢出 (scrollWidth=${r.page.scrollWidth}/${r.page.innerWidth})  「${r.text.slice(0, 24)}」${hint}${info}`);
    }
  }
  await ctx.close();
}
await browser.close();
console.log(`\n路由 ${routes.length} 条 × 视口 ${WIDTHS.join('/')}，未达标 ${bad} 处（横向溢出，或本页请求里有 ≥400）。` +
  `截图在 ${OUT}（不解读像素，只留档）。`);
process.exit(bad ? 1 : 0);
