#!/usr/bin/env node
// 表头几何探针：量「表头标题相对自己那一行偏了多少」与「往下滚之后它还钉不钉得住」。
//
// 为什么要有这一支（不是把判据写成一句话就算防住了）：
// 2026-10-10 那颗"全局表头吸顶"把 `position: sticky; top: 64px` 加在 .table-base th 上，
// 而表格的横向滚动容器 `.table-wrap` 就是最近的滚动口——**top 是相对滚动口量的**，
// 于是未滚动时表头被整行往下推 64 像素、压住第一行；同时它一次也没真的"吸"在顶栏下面。
// 构建、用例、css-check、横向溢出探针**全都不报这件事**：它不改数据、不越界，只是画歪了。
// 判据页那条「窄屏错构建、用例、css-check 全都不报，只能按那个视口量一次」的第三个入口。
//
// 用法（配合一份临时实例，绝不在生产上跑）：
//   PROBE=scripts/header-geometry-probe.mjs ./scripts/run-viewport-probe.sh
//   STICKY=1 ... 同上        # 把"卡片滚起来之后 th 还贴着卡片顶边"也变成硬要求（桌面端才适用）
// 或手动：BASE=http://127.0.0.1:PORT ADMIN_USER=admin ADMIN_PASS=xxx node scripts/header-geometry-probe.mjs
//
// 退出码：0 = 所有路由的表头零偏移且不压行（STICKY=1 时还要吸住）；1 = 有偏移/压行（逐行点名）；2 = 起不来或量不到样本。

import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';

const BASE = (process.env.BASE || 'http://127.0.0.1:8081').replace(/\/$/, '');
const ADMIN_USER = process.env.ADMIN_USER || '';
const ADMIN_PASS = process.env.ADMIN_PASS || '';
const WIDTHS = (process.env.WIDTHS || '1280,390').split(',').map((w) => parseInt(w, 10));
const HEIGHT = parseInt(process.env.HEIGHT || '800', 10);
const TOKEN_KEY = 'loomproxy_token'; // 与 web/src/api/client.js 的 TOKEN_KEY 同一个名字

// 路由表从源码里读，不抄第二份（同 viewport-probe.mjs 那条判据）。
function routesFromSource() {
  const main = fs.readFileSync(path.resolve('web/src/main.js'), 'utf8');
  const start = main.indexOf('const routes = [');
  const end = main.indexOf('\n]', start);
  const out = [];
  for (const m of main.slice(start, end).matchAll(/\{\s*path:\s*'([^']+)'[^}]*\}/g)) {
    const seg = m[0];
    if (/redirect:/.test(seg)) continue;
    if (/public:\s*true/.test(seg)) continue;
    if (/[:*]/.test(m[1])) continue;
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
    console.error('登录失败：status=' + res.status + ' body=' + JSON.stringify(body).slice(0, 200));
    process.exit(2);
  }
  return body.data.token;
}

function chromePath() {
  if (process.env.CHROME) return process.env.CHROME;
  const root = path.join(os.homedir(), '.cache/ms-playwright');
  for (const dir of fs.readdirSync(root).sort().reverse()) {
    for (const cand of [dir + '/chrome-linux64/chrome', dir + '/chrome-linux/chrome']) {
      const p = path.join(root, cand);
      if (fs.existsSync(p)) return p;
    }
  }
  console.error('找不到 chromium：设 CHROME=<可执行文件路径>');
  process.exit(2);
}

// 这段是 page.evaluate 的函数体，**不要把它放进模板字符串**：
// viewport-probe.mjs 那段 MEASURE 就是一个模板字符串，里面出现一次反引号就把整个脚本弄坏过三周。
function measure() {
  const out = { tables: [], nav: null, path: location.pathname, tableCount: 0 };
  const main = document.querySelector('main') || document.body;
  out.text = (main.innerText || '').replace(/\s+/g, ' ').slice(0, 40);
  out.tableCount = document.querySelectorAll('table.table-base').length;
  const nav = document.querySelector('header.sticky, header[class*="sticky"]');
  if (nav) {
    const r = nav.getBoundingClientRect();
    out.nav = { top: Math.round(r.top), bottom: Math.round(r.bottom), h: Math.round(r.height) };
  }
  for (const table of document.querySelectorAll('table.table-base')) {
    const th = table.querySelector('thead th');
    const tr = th ? th.closest('tr') : null;
    const firstRow = table.querySelector('tbody tr');
    if (!th || !tr || !firstRow) {
      // 量不到就当样本记一条，别静默跳过：跳过之后"这张表没回归"和"这张表不存在"长得一模一样
      out.tables.push({ route: location.pathname, skipped: !th ? '没有 thead th' : (!firstRow ? '没有 tbody 行' : 'th 找不到所在行'), rows: table.querySelectorAll('tbody tr').length });
      continue;
    }
    const rTh = th.getBoundingClientRect();
    const rTr = tr.getBoundingClientRect();
    const rRow = firstRow.getBoundingClientRect();
    // 最近的滚动口：sticky 的 top 相对它量，不是相对视口
    let port = null;
    for (let p = th.parentElement; p; p = p.parentElement) {
      const st = getComputedStyle(p);
      if (/(auto|scroll|hidden)/.test(st.overflowX + ' ' + st.overflowY)) {
        const pr = p.getBoundingClientRect();
        port = {
          tag: p.tagName.toLowerCase() + ((p.className && typeof p.className === 'string') ? '.' + p.className.trim().split(/\s+/)[0] : ''),
          canScrollY: p.scrollHeight > p.clientHeight + 1,
          top: Math.round(pr.top),
          st: Math.round(p.scrollTop),
          bottom: Math.round(pr.bottom),
        };
        break;
      }
    }
    out.tables.push({
      route: location.pathname,
      shift: Math.round(rTh.top - rTr.top),                       // 表头相对自己那一行偏下多少
      overlap: Math.round(rTh.bottom - rRow.top),                  // >0 = 表头压住第一行多少像素
      thTop: Math.round(rTh.top),
      rows: table.querySelectorAll('tbody tr').length,
      port: port,
    });
  }
  return out;
}

// 把每个表格卡片自己往下滚一段——吸顶的"钉住"是相对**卡片顶边**，不是相对视口/顶栏。
function scrollCards() {
  let scrolled = 0;
  for (const w of document.querySelectorAll('.table-wrap')) {
    if (w.scrollHeight > w.clientHeight + 1) { w.scrollTop = 300; if (w.scrollTop > 0) scrolled++; }
  }
  window.scrollTo(0, 0);
  return scrolled;
}

const routes = routesFromSource();
if (!routes.length) { console.error('路由表读出来是空的——探针会空转，先修它'); process.exit(2); }
const only = (process.env.ROUTES || '').split(',').filter(Boolean);
const targets = only.length ? routes.filter((r) => only.includes(r)) : routes;
if (only.length && !targets.length) {
  console.error('ROUTES 里没有一个命中路由表：给了 ' + only.join(',') + '，路由表是 ' + routes.join(','));
  process.exit(2);
}
const token = await login();

const { createRequire } = await import('node:module');
const requireFn = createRequire(import.meta.url);
const { chromium } = requireFn(process.env.PW_CORE || '/tmp/node_modules/playwright-core');
const browser = await chromium.launch({
  executablePath: chromePath(),
  args: ['--no-sandbox', '--disable-dev-shm-usage', '--disable-gpu'],
});

let bad = 0;
let samples = 0;
let holes = 0;
let stickyChecked = 0;   // 本轮有几个卡片真的滚起来了（STICKY=1 时它要是 0，那条硬要求就在空转）
for (const width of WIDTHS) {
  const ctx = await browser.newContext({ viewport: { width, height: HEIGHT }, deviceScaleFactor: 1 });
  await ctx.addInitScript(([k, v]) => { try { localStorage.setItem(k, v); } catch (e) {} }, [TOKEN_KEY, token]);
  const page = await ctx.newPage();
  console.log('\n== 视口 ' + width + 'x' + HEIGHT + ' ==');
  for (const route of targets) {
    // 面板挂在 /panel 下：直接 GET /admin/users 打到的是**后端 API**，
    // 页面会渲染成一行 JSON（第一次跑就是这么量到 0 张表的）——加前缀才是那个 SPA。
    const url = BASE + '/panel' + route;
    try {
      await page.goto(url, { waitUntil: 'networkidle', timeout: 20000 });
    } catch (e) {
      console.log('  ' + route + ' 打不开：' + e.message.split('\n')[0]);
      continue;
    }
    const m = await page.evaluate(measure);
    // 每条路由都要留一行痕迹：静默 continue 会让"没量到"长得像"量过且没问题"
    console.log('  --   ' + route + ' 停在 ' + m.path + '，table-base 共 ' + m.tableCount + ' 张，正文起 ' + m.text);
    // 把每张表格卡片自己往下滚一段再看：吸顶要钉住的是**卡片顶边**，不是视口也不是顶栏
    await page.evaluate(scrollCards);
    await page.waitForTimeout(150);
    const m2 = await page.evaluate(measure);
    for (let i = 0; i < m.tables.length; i++) {
      const t = m.tables[i];
      const s = m2.tables[i] || {};
      if (t.skipped) {
        holes++;
        console.log('  skip ' + t.route + ' 表头[' + i + '] 量不到（' + t.skipped + '，tbody 行数=' + t.rows + '）——不算通过，也不算失败');
        continue;
      }
      samples++;
      const problems = [];
      if (t.shift !== 0) problems.push('未滚动时表头偏下 ' + t.shift + 'px');
      if (t.overlap > 0) problems.push('表头压住第一行 ' + t.overlap + 'px');
      // 吸顶那一半：卡片真的滚起来了，且滚过之后 th 还贴着卡片顶边。
      const portAfter = s.port;
      const rolled = !!(portAfter && portAfter.st > 0);
      const delta = (typeof s.thTop === 'number' && portAfter) ? s.thTop - portAfter.top : null;
      if (rolled) {
        stickyChecked++;
        if (process.env.STICKY === '1' && delta !== null && Math.abs(delta) > 2) {
          problems.push('卡片滚了 ' + portAfter.st + 'px 之后 th 没贴住卡片顶边（thTop−口顶=' + delta + '）');
        }
      }
      const stickNote = rolled
        ? ' 滚后 thTop−口顶=' + delta + (Math.abs(delta) <= 2 ? '（吸住）' : '（没吸住）')
        : '（这张卡没纵滚，吸顶这一半量不到）';
      if (problems.length) {
        bad++;
        console.log('  FAIL ' + t.route + ' 表头[' + i + '] 行数=' + t.rows + '：' + problems.join('；') + stickNote);
      } else {
        console.log('  ok   ' + t.route + ' 表头[' + i + '] 行数=' + t.rows + ' shift=0 overlap=' + t.overlap + stickNote);
      }
    }
  }
  // STICKY=1 却一格都没滚起来 = 那条硬要求在做空转（桌面端 max-height 那半没了，或被媒体查询挡在门外）
  if (process.env.STICKY === '1' && width >= 768 && stickyChecked === 0) {
    bad++;
    console.log('  FAIL 视口 ' + width + '：带 STICKY=1 跑，但没有任何一张表格卡片滚起来——吸顶那半没被验过');
  }
  stickyChecked = 0;
}
await browser.close();
console.log('\n样本 ' + samples + ' 张表（未达标 ' + bad + ' 格、量不到 ' + holes + ' 格）——样本为 0 就是探针在空转，别把没量到当通过');
if (samples === 0) { console.error('一条表头都没量到——路由/登录/表格类名任一处变了，先修探针'); process.exit(2); }
process.exit(bad ? 1 : 0);
