// 布局回归护栏(第五十三批):把"主界面被整页滚走"这类事故钉死在 CI 里。
//
// 为什么需要浏览器:这类缺陷(2026-09-22 用户实测那次)是**真实布局**的结果 ——
// Sidebar 里 `v-else` 绑错 `v-if` 多渲染了一个 height:100% 的 ☰ 按钮,它按 block 流排在
// 769px 高的 .panel 之后,把文档撑到 1573px。jsdom 之类不算布局(得 0 高度),静态 AST
// 也判不出来(v-else 绑到邻近的另一个 v-if 对 Vue 完全合法),所以只有真渲染才验得了。
//
// 跑法:`cd web-src && npm test`(已并入默认前端测试;需先有 web/dist —— scripts/gen-web.sh)。
// 依赖:本机 Chrome/Chromium;找不到就**跳过**(不拦人)。CI 想强制设 GAH_LAYOUT_REQUIRE=1。
//   GAH_LAYOUT_CHROME=/path/to/chrome  指定可执行文件(否则按常见路径 + channel:'chrome' 依次试)
import { after, before, describe, test } from 'node:test'
import assert from 'node:assert/strict'
import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const WEB_SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const DIST = path.resolve(WEB_SRC, '..', 'web', 'dist')
const REQUIRE = process.env.GAH_LAYOUT_REQUIRE === '1'

const MIME = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon',
  '.woff2': 'font/woff2',
  '.png': 'image/png',
}

// startStatic 起一个只读静态服务(不发包、不落盘;SPA 兜底到 index.html)。
function startStatic(root) {
  const server = http.createServer((req, res) => {
    const rel = decodeURIComponent(req.url.split('?')[0])
    let file = path.join(root, rel)
    if (!file.startsWith(root) || !fs.existsSync(file) || fs.statSync(file).isDirectory()) {
      file = path.join(root, 'index.html')
    }
    const body = fs.readFileSync(file)
    res.writeHead(200, { 'Content-Type': MIME[path.extname(file)] || 'application/octet-stream' })
    res.end(body)
  })
  return new Promise((ok) => server.listen(0, '127.0.0.1', () => ok(server)))
}

// apiStub 给页面喂最小可用数据:40 条会话(长列表正是当年把越界放大到 521 个元素的场景),
// 其余端点给空壳。渲染不出内容不影响判定 —— 这里断言的是外壳几何,不是业务数据。
function apiStub(route) {
  const url = new URL(route.request().url())
  const p = url.pathname
  const json = (v, status = 200) =>
    route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(v) })
  if (p === '/api/state') {
    return json({
      model: 'layout-guard/model',
      thinking: 'off',
      sandbox: 'full',
      stats: { prompt_tokens: 1200, completion_tokens: 300, cached_tokens: 0, requests: 3, window: 200000 },
      running: false,
      version: 'layout-guard',
    })
  }
  if (p === '/api/sessions') {
    const now = Date.now()
    return json(
      Array.from({ length: 40 }, (_, i) => ({
        ID: i === 0 ? '' : `layout-${i}`,
        Path: `/tmp/layout-${i}.jsonl`,
        Name: i === 0 ? '主会话' : `会话 ${i}`,
        Preview: `第 ${i} 条会话的预览文本,用来撑出真实内容高度。`.repeat(3),
        MTime: Math.floor((now - i * 3600_000) / 1000),
        Frames: 100 + i,
      })),
    )
  }
  if (p === '/api/notices') return json({ items: [], max_id: 0 })
  if (p === '/api/session/events') return json({ events: [], from: 0, to: 0, count: 0, has_more: false })
  // 配一个 provider:provider 数为 0 时首屏会自动弹设置面板(App.vue maybeOnboard),
  // 那层 .mask 会盖住侧栏(点不动 .toggle)且不反映真实布局。
  if (p === '/api/providers') return json([{ Name: 'layout', BaseURL: 'http://127.0.0.1:1/v1', APIKey: 'sk-layout', Model: 'layout-guard/model', Active: true }])
  // 各端点的空壳形状取自 src/types.ts;形状不对会让 Vue 渲染中途抛错(页面半渲染 ⇒ 断言失效)。
  if (p === '/api/models') return json({ providers: [] })
  if (p === '/api/mcp') return json({ path: '', servers: [], reload_available: false, plugin_loaded: false })
  if (p === '/api/doc/tree') return json({ entries: [] })
  return json([])
}

// launchOpts 依次尝试:显式路径 → 系统 Chrome → 常见 Linux 路径 → Playwright 默认 chromium。
function launchOpts() {
  const cands = [
    process.env.GAH_LAYOUT_CHROME,
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
    '/usr/bin/google-chrome',
    '/usr/bin/google-chrome-stable',
    '/opt/google/chrome/chrome',
    '/usr/bin/chromium',
    '/usr/bin/chromium-browser',
  ].filter(Boolean)
  const found = cands.find((c) => fs.existsSync(c))
  return found ? { executablePath: found } : { channel: 'chrome' }
}

// 共享上下文:一个浏览器一个静态服务,跑完统一收摊。
// 前置条件缺失(没产物/没浏览器/没 playwright-core)默认**跳过**(不拦人);
// GAH_LAYOUT_REQUIRE=1 时改成硬失败 —— CI 想要"绝不静默通过"就设它。
let server = null
let browser = null
let skip = false
let skipWhy = ''

function unavailable(why) {
  skip = true
  skipWhy = why
  if (REQUIRE) throw new Error(`GAH_LAYOUT_REQUIRE=1 但布局护栏跑不了:${why}`)
}

const ready = (async () => {
  if (!fs.existsSync(path.join(DIST, 'index.html'))) {
    return unavailable(`缺少 ${DIST}/index.html:先 bash scripts/gen-web.sh 再跑本护栏`)
  }
  const { chromium } = await import('playwright-core').catch((e) => {
    unavailable(`未安装 playwright-core(${e.message})`)
    return {}
  })
  if (!chromium) return
  try {
    browser = await chromium.launch(launchOpts())
  } catch (e) {
    // 本机没 Chrome:跳过(不拦人);CI 用 GAH_LAYOUT_REQUIRE=1 把它变成失败。
    return unavailable(`未找到可用 Chrome/Chromium(${String(e.message).split('\n')[0]})`)
  }
  server = await startStatic(DIST)
})()
await ready

const baseURL = () => `http://127.0.0.1:${server.address().port}/`

after(async () => {
  if (browser) await browser.close()
  if (server) await server.close()
})

// measure 在页面里量三个不变量(与人工验收用的探针同一套判定):
//   ① 文档不被撑高(外壳永不滚:html/body overflow:hidden + 无越界子元素)
//   ② 关键骨架在视口内(输入区/状态栏在场且不越界)
//   ③ 没有"无裁剪祖先"的元素越出视口(裁剪=内容被切掉,同样是缺陷)
async function measure(page) {
  return page.evaluate(() => {
    const se = document.scrollingElement
    window.scrollTo(0, 500)
    const scrolled = se.scrollTop
    window.scrollTo(0, 0)
    const vh = window.innerHeight
    const clip = (el) => {
      for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
        const oy = getComputedStyle(a).overflowY
        if (oy === 'auto' || oy === 'scroll' || oy === 'hidden') return true
      }
      return false
    }
    const name = (el) => {
      const c = typeof el.className === 'string' ? el.className.trim().split(/\s+/).slice(0, 2).join('.') : ''
      return el.tagName.toLowerCase() + (c ? '.' + c : '')
    }
    const outside = []
    for (const el of document.querySelectorAll('body *')) {
      const bb = el.getBoundingClientRect()
      if (bb.width === 0 && bb.height === 0) continue
      if ((bb.bottom > vh + 1 || bb.top < -1) && !clip(el)) {
        outside.push({ el: name(el), top: Math.round(bb.top), bottom: Math.round(bb.bottom), h: Math.round(bb.height) })
      }
    }
    const box = (sel) => {
      const el = document.querySelector(sel)
      if (!el) return null
      const bb = el.getBoundingClientRect()
      return { top: Math.round(bb.top), bottom: Math.round(bb.bottom), inside: bb.bottom <= vh + 1 && bb.top >= -1 }
    }
    return {
      scrollHeight: se.scrollHeight,
      clientHeight: se.clientHeight,
      scrolled,
      innerHeight: vh,
      outside: outside.slice(0, 6),
      outsideTotal: outside.length,
      panels: document.querySelectorAll('.sidebar .panel').length,
      handles: document.querySelectorAll('.sidebar .handle').length,
      sidebar: !!document.querySelector('.sidebar'),
      composer: box('.input-slot'),
      statusbar: box('.statusbar-slot'),
    }
  })
}

function assertInvariants(m) {
  assert.ok(
    m.scrollHeight <= m.clientHeight + 1,
    `文档被撑高(整页可滚):scrollHeight=${m.scrollHeight} > clientHeight=${m.clientHeight};越界元素=${JSON.stringify(m.outside)}`,
  )
  assert.equal(m.scrolled, 0, `页面能滚动(scrollTo 生效),越界元素=${JSON.stringify(m.outside)}`)
  assert.equal(m.outsideTotal, 0, `有元素越出视口且未被裁剪:${JSON.stringify(m.outside)}`)
  assert.ok(m.sidebar, '侧栏未渲染')
  assert.ok(m.composer && m.composer.inside, `输入区不在视口内:${JSON.stringify(m.composer)}`)
  assert.ok(m.statusbar && m.statusbar.inside, `状态栏不在视口内:${JSON.stringify(m.statusbar)}`)
}

const viewports = [
  { w: 1200, h: 800 },
  { w: 1440, h: 1000 },
  { w: 1000, h: 620 },
  { w: 820, h: 560 },
]
const docks = [
  { tag: '停靠收起', dock: { open: false, panel: 'changes', width: 392 } },
  { tag: '停靠-变更', dock: { open: true, panel: 'changes', width: 392 } },
  { tag: '停靠-看板', dock: { open: true, panel: 'board', width: 392 } },
  { tag: '停靠-任务', dock: { open: true, panel: 'jobs', width: 392 } },
]

describe('布局护栏:整页永不滚动(第五十二/五十三批)', { skip: skip && skipWhy }, () => {
  for (const vp of viewports) {
    for (const d of docks) {
      test(`${vp.w}x${vp.h} ${d.tag}`, async () => {
        const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
        try {
          await ctx.addInitScript(([k, v]) => {
            window.localStorage.setItem(k, v)
            window.sessionStorage.setItem('gah.onboard.auto', '1') // 首屏引导标记:不自动弹设置面板
          }, ['gah.dock', JSON.stringify(d.dock)])
          const page = await ctx.newPage()
          await page.route('**/api/**', apiStub)
          await page.goto(baseURL(), { waitUntil: 'load' })
          await page.waitForTimeout(1200)
          assertInvariants(await measure(page))
        } finally {
          await ctx.close()
        }
      })
    }
  }

  test('侧栏开合语义:展开只有 .panel,收起只有 .handle', async () => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    try {
      await ctx.addInitScript(() => window.sessionStorage.setItem('gah.onboard.auto', '1'))
      const page = await ctx.newPage()
      await page.route('**/api/**', apiStub)
      await page.goto(baseURL(), { waitUntil: 'load' })
      await page.waitForTimeout(1200)
      const open = await measure(page)
      assertInvariants(open)
      assert.equal(open.handles, 0, '侧栏展开时不该有收起态 ☰ 按钮(第五十二批事故:多渲染了一个 height:100% 的按钮把文档撑到 1573px)')
      assert.equal(open.panels, 1, '侧栏展开时应恰好一个 .panel')
      await page.click('.sidebar .toggle')
      await page.waitForTimeout(400)
      const closed = await measure(page)
      assertInvariants(closed)
      assert.equal(closed.panels, 0, '侧栏收起时不该有 .panel')
      assert.equal(closed.handles, 1, '侧栏收起时应恰好一个 ☰ 按钮')
    } finally {
      await ctx.close()
    }
  })
})

test('布局护栏:跳过原因(仅在没有浏览器/产物时输出)', { skip: !skip }, () => {
  console.log(`  跳过:${skipWhy}`)
})
