// 布局回归护栏(第五十三批;第五十四批打开 CI 严档;第五十五批完善)
//
// 为什么需要浏览器:这类缺陷(2026-09-22 用户实测那次)是**真实布局**的结果 ——
// Sidebar 里 `v-else` 绑错 `v-if` 多渲染了一个 height:100% 的 ☰ 按钮,它按 block 流排在
// 769px 高的 .panel 之后,把文档撑到 1573px。jsdom 之类不算布局(得 0 高度),静态 AST
// 也判不出来(v-else 绑到邻近的另一个 v-if 对 Vue 完全合法),所以只有真渲染才验得了。
//
// 跑法:`cd web-src && npm run test:layout`(需先有 web/dist —— scripts/gen-web.sh)。
// 依赖:本机 Chrome/Chromium;找不到就**跳过**(不拦人)。CI 用 GAH_LAYOUT_REQUIRE=1 变红灯。
//   GAH_LAYOUT_CHROME=/path/to/chrome   指定可执行文件(否则按常见路径 + channel:'chrome' 依次试)
//   GAH_LAYOUT_NO_SANDBOX=1             直接带 --no-sandbox --disable-dev-shm-usage(CI 调试用)
//   GAH_LAYOUT_ARTIFACTS=/dir           失败时把该用例的截图落盘(CI 传 runner.temp 便于排查)
import { after, describe, test } from 'node:test'
import assert from 'node:assert/strict'
import http from 'node:http'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const WEB_SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const DIST = path.resolve(WEB_SRC, '..', 'web', 'dist')
const REQUIRE = process.env.GAH_LAYOUT_REQUIRE === '1'
const ARTIFACTS = process.env.GAH_LAYOUT_ARTIFACTS || ''

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

// apiStub 给页面喂最小可用数据(形状取自 src/types.ts):会话列表给 40 条,长列表正是当年把
// 越界放大到 521 个元素的场景。渲染不出内容不影响判定 —— 这里断言的是外壳几何,不是业务数据。
// 文案一律 ASCII:CI 的 ubuntu 镜像只带 fonts-noto-color-emoji(**没有 CJK 字体**),中文会渲成
// 豆腐块 —— 字形宽度差异不是我们要测的东西,别让它污染几何断言。
function makeStub(withProviders, longTokens = false, running = false, manyPlugins = false, withRoles = false) {
  // seen:记录写类请求(方法/路径/体),供角色面板用例断言「面板真的提交了」而不是只改了本地状态。
  const seen = []
  const handler = (route) => {
    const url = new URL(route.request().url())
    const p = url.pathname
    const json = (v, status = 200) =>
      route.fulfill({ status, contentType: 'application/json; charset=utf-8', body: JSON.stringify(v) })
    if (route.request().method() !== 'GET') {
      let body = ''
      try {
        body = route.request().postData() ?? ''
      } catch {
        body = ''
      }
      seen.push({ method: route.request().method(), path: p, body })
    }
    if (p === '/api/roles') {
      // 未装配 ctx.roles 的环境:真实后端回 503 文本 → 面板应整段隐藏(导航项也不出现)。
      if (!withRoles) return route.fulfill({ status: 503, contentType: 'text/plain; charset=utf-8', body: 'role service unavailable' })
      // 角色名/技能名故意用无空格长 token:那正是「面板多出一条横向滚动条」的触发器。
      if (route.request().method() === 'POST') return json({ id: 'new-role' })
      return json({
        current: 'finance',
        max_agents_bytes: 32768,
        roles: [
          {
            id: 'finance',
            name: 'Finance-Analyst-With-A-Very-Long-Display-Name-0123456789abcdef',
            description: 'Bookkeeping and reporting with a deliberately long ASCII description to stress panel wrapping',
            identity: 'You are a senior finance analyst.',
            exclude_global: true,
            skills_set: false,
            skills: [],
            agents_bytes: 21,
            seed: true,
          },
          { id: 'assistant', name: 'Assistant', description: '', identity: '', skills_set: true, skills: ['skill-alpha'], agents_bytes: 0 },
        ],
        library: [
          { name: 'skill-alpha', description: 'Alpha skill description with a long ASCII tail 0123456789abcdef' },
          { name: 'skill-with-a-very-long-name-0123456789abcdef', description: 'Another skill' },
          { name: 'private-beta', description: 'Private skill of finance', role: 'finance' },
        ],
      })
    }
    if (p.startsWith('/api/roles/') && p.endsWith('/agents')) return json({ ok: true, bytes: 21 })
    if (p.startsWith('/api/roles/')) {
      if (route.request().method() === 'DELETE') return route.fulfill({ status: 200, body: '' })
      if (p.endsWith('/use')) return json({ ok: true, current: p.split('/')[3] })
      if (p.endsWith('/rename')) return json({ id: 'renamed' })
      if (route.request().method() === 'PATCH') {
        return json({
          id: 'finance',
          name: 'Finance',
          skills_set: true,
          skills: ['skill-alpha', 'skill-with-a-very-long-name-0123456789abcdef'],
          agents_bytes: 21,
        })
      }
      return json({
        id: 'finance',
        name: 'Finance',
        identity: 'You are a senior finance analyst.',
        description: 'Bookkeeping',
        exclude_global: true,
        skills_set: false,
        skills: [],
        agents: 'Always reconcile before reporting.\n'.repeat(3),
        agents_bytes: 96,
      })
    }
    if (p === '/api/skills') return json({ name: 'new-skill', path: '/tmp/skills/new-skill/SKILL.md' })
    if (p.startsWith('/api/skills/')) return json({ name: 'skill-alpha', content: '---\nname: skill-alpha\n---\nbody', path: '/tmp/skills/skill-alpha/SKILL.md' })
    if (p === '/api/state') {
      return json({
        model: 'layout-guard/model',
        thinking: 'off',
        sandbox: 'full',
        // stats 内层字段不带 json tag(直接用 sdk.UsageStats 字段名)——必须 PascalCase,
        // 写成 snake_case 前端读不到(上下文会显示 '–',桩就与真实契约不一致了)。
        stats: { PromptTokens: 1200, CompletionTokens: 300, CachedTokens: 0, Requests: 3, LastPromptTokens: 1200, Window: 200000 },
        // 角色徽标(第七十九批):状态栏多一个 "角色 <名>" 项 —— 长角色名不得把底栏挤变形。
        ...(withRoles ? { role: 'finance', role_name: 'Finance-Analyst-With-A-Very-Long-Display-Name-0123456789abcdef' } : {}),
        running,
        version: 'layout-guard',
      })
    }
    if (p === '/api/sessions') {
      const now = Date.now()
      return json(
        Array.from({ length: 40 }, (_, i) => ({
          ID: i === 0 ? '' : `layout-${i}`,
          Path: `/tmp/layout-${i}.jsonl`,
          Name: i === 0 ? 'main' : `session ${i}`,
          Preview: `Session ${i} preview text, long enough to wrap into several lines. `.repeat(3),
          MTime: Math.floor((now - i * 3600_000) / 1000),
          Frames: 100 + i,
        })),
      )
    }
    if (p === '/api/notices') return json({ items: [], max_id: 0 })
    if (p === '/api/session/events') return json({ events: [], from: 0, to: 0, count: 0, has_more: false })
    // provider 数为 0 时首屏会自动弹设置面板(App.vue maybeOnboard)—— 那也是要覆盖的真实状态,
    // 所以不是一律给 1 个(见「首启态」用例)。
    if (p === '/api/providers') {
      if (!withProviders) return json([])
      // longTokens:provider 名与插件 ID 都是「用户/打包决定、可能出现无空格长 token」的字段。
      const pname = longTokens ? 'provider-name-without-any-break-0123456789abcdef' : 'layout'
      return json([{ Name: pname, BaseURL: 'http://127.0.0.1:1/v1', APIKey: 'sk-layout', Model: 'layout-guard/model', Active: true }])
    }
    if (p === '/api/plugins') {
      // manyPlugins:43 条正是真机里把「关于 gah / 检查更新」顶到面板最底的那个长度(插件段收纳的用例源)
      if (manyPlugins) {
        return json(
          Array.from({ length: 43 }, (_, i) => ({
            ID: `host-plugin-${String(i).padStart(2, '0')}`,
            Type: 'host',
            State: i < 3 ? 'loaded' : 'idle',
            manage: 'web',
          })),
        )
      }
      if (longTokens) {
        return json([{ ID: 'host-plugin-with-a-very-long-identifier-0123456789abcdef', Type: 'host', State: 'loaded', manage: 'external' }])
      }
      return json([])
    }
    if (p === '/api/models') return json({ providers: [] })
    if (p === '/api/mcp') {
      if (!longTokens) return json({ path: '', servers: [], reload_available: false, plugin_loaded: false })
      // 长 token 场景照搬真机形状:Windows 配置路径与 MCP 启动命令都是一整段无空格文本,正是
      // 「设置面板多出一条横向滚动条」的触发器(见下方专门用例)。
      return json({
        path: 'C:\\Users\\nekole\\AppData\\Local\\dev.gah.desktop\\bin\\gah-data\\config\\mcp.json',
        servers: [
          {
            name: 'deja',
            command: 'npx -y @modelcontextprotocol/server-memory --registry=https://registry.npmmirror.com',
            enabled: true,
            mode: 'direct',
          },
        ],
        reload_available: true,
        plugin_loaded: true,
      })
    }
    if (p === '/api/schedules' && longTokens) {
      return json([
        {
          id: 's1',
          name: '每日对账',
          cron: '0 8 * * *',
          prompt: 'x',
          enabled: true,
          next_run: 1_900_000_000,
          last_run_at: 1_900_000_000,
          last_status: 'failed',
          last_error: 'connect ECONNREFUSED 127.0.0.1:11434',
        },
      ])
    }
    if (p === '/api/input') {
      // 运行中的提交回 accepted=steer(宿主把消息注入当前回合);否则 turn(开新回合)。
      return json({ ok: true, accepted: running ? 'steer' : 'turn' })
    }
    if (p === '/api/doc/tree') return json({ entries: [] })
    return json([])
  }
  handler.seen = seen
  return handler
}
const apiStub = makeStub(true)

// launchOpts args 非空时一并带上(重试路径用)。依次尝试:显式路径 → 系统 Chrome → 常见 Linux 路径
// → 交给 Playwright 的 channel 解析(它自己的表就是 linux `/opt/google/chrome/chrome`、
// darwin `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`,与 CI 镜像给的位置一致)。
function launchOpts(args) {
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
  const opts = found ? { executablePath: found } : { channel: 'chrome' }
  if (args.length) opts.args = args
  return opts
}

// UBUNTU_SANDBOX_ARGS:Ubuntu 23.10+ 用 AppArmor 限制非特权 user namespace,Chrome 内建沙箱起不来
// (报 "No usable sandbox!")。CI 的 ubuntu-latest 就是 24.04 ⇒ 首次失败后带这两个参数重试一次
// (仅测试期的自渲染,风险可忽略);GAH_LAYOUT_NO_SANDBOX=1 可直接走这条参数。
const UBUNTU_SANDBOX_ARGS = ['--no-sandbox', '--disable-dev-shm-usage']

// shouldRetryWithoutSandbox 只对「沙箱 / user namespace」类失败重试 —— 其余错误(浏览器没装、
// 路径不对、版本不匹配)必须原样暴露,不能被重试掩盖。
function shouldRetryWithoutSandbox(msg) {
  return /sandbox|namespace/i.test(String(msg))
}

// launchBrowser 先按标准参数启动;若失败且形如沙箱问题,再带 --no-sandbox 重试一次。
async function launchBrowser(chromium) {
  const forced = process.env.GAH_LAYOUT_NO_SANDBOX === '1'
  try {
    const browser = await chromium.launch(launchOpts(forced ? UBUNTU_SANDBOX_ARGS : []))
    return { browser, how: forced ? 'GAH_LAYOUT_NO_SANDBOX=1 指定 --no-sandbox' : '标准参数' }
  } catch (e) {
    if (forced || !shouldRetryWithoutSandbox(e.message)) throw e
    console.log(`  首次启动失败(疑似沙箱限制),带 --no-sandbox 重试:${String(e.message).split('\n')[0]}`)
    return { browser: await chromium.launch(launchOpts(UBUNTU_SANDBOX_ARGS)), how: '--no-sandbox 重试' }
  }
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
  if (REQUIRE) {
    throw new Error(
      `GAH_LAYOUT_REQUIRE=1 但布局护栏跑不了:${why}\n` +
        '  runner 上装 Chrome 或设 GAH_LAYOUT_CHROME=/path/to/chrome;确属环境不可用再撤掉 CI 那步的 GAH_LAYOUT_REQUIRE。',
    )
  }
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
    const r = await launchBrowser(chromium)
    browser = r.browser
    console.log(`  布局护栏浏览器:${browser.version()}(${r.how})`)
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

// shoot 失败取证:把该用例当帧截图落盘(仅在 GAH_LAYOUT_ARTIFACTS 指定时;CI 传 runner.temp)。
async function shoot(page, name) {
  if (!ARTIFACTS || !page) return
  try {
    fs.mkdirSync(ARTIFACTS, { recursive: true })
    const file = path.join(ARTIFACTS, name.replace(/[^\w.\u4e00-\u9fa5-]+/g, '_') + '.png')
    await page.screenshot({ path: file })
    console.log(`  失败快照:${file}`)
  } catch (e) {
    console.log(`  失败快照写入失败:${String(e.message).slice(0, 120)}`)
  }
}

// waitSkeleton 等骨架 DOM 出现(不再靠固定 sleep):侧栏两种形态之一 + 输入区 + 状态栏。
// 之后给一帧时间让样式/字体落定。
async function waitSkeleton(page) {
  await page.waitForFunction(
    () =>
      !!document.querySelector('.sidebar') &&
      (!!document.querySelector('.sidebar .panel') || !!document.querySelector('.sidebar .handle')) &&
      !!document.querySelector('.input-slot') &&
      !!document.querySelector('.statusbar-slot'),
    null,
    { timeout: 10_000 },
  )
  await page.waitForTimeout(250)
}

// open 统一开页:注布局偏好 → 桩 API → 加载 → 等骨架稳定。
async function open(ctx, stub, dock) {
  await ctx.addInitScript(
    ([k, v]) => {
      window.localStorage.setItem(k, v)
      window.sessionStorage.setItem('gah.onboard.auto', '1') // 首屏引导标记:默认不自动弹设置面板
    },
    ['gah.dock', JSON.stringify(dock)],
  )
  const page = await ctx.newPage()
  await page.route('**/api/**', stub)
  await page.goto(baseURL(), { waitUntil: 'load' })
  await waitSkeleton(page)
  return page
}

// measure 在页面里量三个不变量(与人工验收用的探针同一套判定):
//   ① 文档不被撑高(外壳永不滚:html/body overflow:hidden + 无越界子元素)
//   ② 关键骨架在视口内(输入区/状态栏在场且不越界)
//   ③ 没有"无裁剪祖先"的元素越出视口(裁剪=内容被切掉,同样是缺陷)
async function measure(page) {
  return page.evaluate(() => {
    const se = document.scrollingElement
    window.scrollTo(0, 500)
    const scrolled = se.scrollTop
    window.scrollTo(500, 0)
    const scrolledX = se.scrollLeft
    window.scrollTo(0, 0)
    const vh = window.innerHeight
    const vw = window.innerWidth
    // clip:该元素上方有没有裁切柜(垂直或水平任一侧都算 —— 只要有一侧不 visible,
    // 另一侧在 CSS 上也会计算成 auto)。走链到 body 为止:html/body 的 overflow:hidden
    // **不算裁切柜**,否则 2026-09-22 那种"被 body 裁掉但实际越界"的元素就漏检了。
    const HIDES = ['auto', 'scroll', 'hidden']
    const clip = (el) => {
      for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
        const cs = getComputedStyle(a)
        if (HIDES.includes(cs.overflowY) || HIDES.includes(cs.overflowX)) return true
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
      const offV = bb.bottom > vh + 1 || bb.top < -1
      const offH = bb.right > vw + 1 || bb.left < -1
      if ((offV || offH) && !clip(el)) {
        outside.push({
          el: name(el),
          axis: offV && offH ? 'vh' : offV ? 'v' : 'h',
          top: Math.round(bb.top),
          bottom: Math.round(bb.bottom),
          left: Math.round(bb.left),
          right: Math.round(bb.right),
          h: Math.round(bb.height),
        })
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
      scrollWidth: se.scrollWidth,
      clientWidth: se.clientWidth,
      scrolled,
      scrolledX,
      innerHeight: vh,
      innerWidth: vw,
      outside: outside.slice(0, 6),
      outsideTotal: outside.length,
      panels: document.querySelectorAll('.sidebar .panel').length,
      handles: document.querySelectorAll('.sidebar .handle').length,
      hasSidebar: !!document.querySelector('.sidebar'),
      hasSettings: !!document.querySelector('[aria-label="设置"]'),
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
  assert.equal(m.scrolled, 0, `页面能竖向滚动(scrollTo 生效),越界元素=${JSON.stringify(m.outside)}`)
  assert.ok(
    m.scrollWidth <= m.clientWidth + 1,
    `文档被撑宽(能横向滚):scrollWidth=${m.scrollWidth} > clientWidth=${m.clientWidth};越界元素=${JSON.stringify(m.outside)}`,
  )
  assert.equal(m.scrolledX, 0, `页面能横向滚动,越界元素=${JSON.stringify(m.outside)}`)
  assert.equal(m.outsideTotal, 0, `有元素越出视口且未被裁剪:${JSON.stringify(m.outside)}`)
  assert.ok(m.hasSidebar, '侧栏未渲染')
  assert.ok(m.composer && m.composer.inside, `输入区不在视口内:${JSON.stringify(m.composer)}`)
  assert.ok(m.statusbar && m.statusbar.inside, `状态栏不在视口内:${JSON.stringify(m.statusbar)}`)
}

const viewports = [
  { w: 1200, h: 800 },
  { w: 1440, h: 1000 },
  { w: 1000, h: 620 },
  { w: 820, h: 560 },
  { w: 700, h: 460 },
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
      test(`${vp.w}x${vp.h} ${d.tag}`, async (t) => {
        const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
        let page = null
        try {
          page = await open(ctx, apiStub, d.dock)
          assertInvariants(await measure(page))
        } catch (e) {
          await shoot(page, t.name)
          throw e
        } finally {
          await ctx.close()
        }
      })
    }
  }

  // 设置面板内部不许出现横向滚动条(第六十三批真机反馈)。
  // 为何整页护栏查不到:.body 的 overflow-y:auto 会把 overflow-x 也算成 auto ⇒ 面板里任何一处
  // 不换行的长 token(Windows 配置路径、MCP 启动命令、计划报错)都会在面板底部多出一条横向滚动条;
  // 而 measure() 的 clip() 把带 auto/hidden 的祖先当裁切柜 —— .body 恰好就是。只能单测面板内部。
  for (const vp of [{ w: 1200, h: 800 }, { w: 820, h: 560 }]) {
    test(`${vp.w}x${vp.h} 设置面板内不出现横向滚动条(长路径/长命令)`, async (t) => {
      const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
      let page = null
      try {
        page = await open(ctx, makeStub(true, true), docks[1].dock)
        await page.click('.gear')
        await page.waitForSelector('[aria-label="设置"]')
        await page.waitForTimeout(300)
        const m = await page.evaluate(() => {
          const panel = document.querySelector('[aria-label="设置"]')
          const body = panel.querySelector('.body')
          const br = body.getBoundingClientRect()
          const nm = (el) =>
            el.tagName.toLowerCase() +
            (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : '')
          const off = []
          // 扫的是 .body 的后代:左导航是它的兄弟节点(在 body 左侧),拿 panel 当基准会把导航本身当成越界。
          for (const el of body.querySelectorAll('*')) {
            const bb = el.getBoundingClientRect()
            if (bb.width === 0 && bb.height === 0) continue
            if (bb.right > br.right + 1 || bb.left < br.left - 1) off.push(nm(el))
          }
          return { scrollW: body.scrollWidth, clientW: body.clientWidth, off: off.slice(0, 6), panelScrollW: panel.scrollWidth, panelClientW: panel.clientWidth }
        })
        assert.ok(
          m.scrollW <= m.clientW + 1,
          `设置面板被撑出横向滚动条:scrollWidth=${m.scrollW} > clientWidth=${m.clientW};越界元素=${JSON.stringify(m.off)}`,
        )
        assert.ok(m.panelScrollW <= m.panelClientW + 1, `设置面板外壳被撑宽:${m.panelScrollW} > ${m.panelClientW}`)
      } catch (e) {
        await shoot(page, t.name)
        throw e
      } finally {
        await ctx.close()
      }
    })
  }

  // 设置面板的分段导航:插件列表 43 条曾把「关于 gah / 检查更新」顶到必须长滚的位置。
  // 这条验的是导航真的能一下到达 + 高亮跟着走(修的是「要滚到底」,不是换个写法)。
  test('设置面板:段导航点击即到达,高亮跟随滚动', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, true, false, true), docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[aria-label="设置"] .nav-it')
      // 先展开到 43 条:真机里把面板拉成长滚动的就是它 —— 导航要在那种长度下仍然一跳到顶。
      await page.click('[data-sec="plugin"] .more')
      await page.click('.nav-it:has-text("数据备份")')
      await page.waitForTimeout(500) // smooth 滚动落定
      const m = await page.evaluate(() => {
        const panel = document.querySelector('[aria-label="设置"]')
        const body = panel.querySelector('.body')
        const sec = body.querySelector('section[data-sec="backup"]')
        return {
          scrollTop: body.scrollTop,
          delta: Math.abs(sec.getBoundingClientRect().top - body.getBoundingClientRect().top),
          on: panel.querySelector('.nav-it.on')?.textContent?.trim() ?? '',
        }
      })
      assert.ok(m.scrollTop > 0, '点「数据备份」后内容区没有滚动')
      assert.ok(m.delta <= 40, `「数据备份」段未对齐到内容区顶部:偏差 ${m.delta}px`)
      assert.equal(m.on, '数据备份', `高亮没落在「数据备份」上,而是「${m.on}」`)
      // 末段(插件)在内容已被展开后仍可能顶不到上沿:高亮仍必须落在它身上(滚到底特判)
      await page.click('.nav-it:has-text("插件")')
      await page.waitForTimeout(500)
      const lastOn = await page.evaluate(
        () => document.querySelector('[aria-label="设置"] .nav-it.on')?.textContent?.trim() ?? '',
      )
      assert.equal(lastOn, '插件', `滚到底后高亮应留在「插件」上,实际「${lastOn}」`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 插件段收纳:≤ 5 条全列,> 5 条只列前 5 + 「展开全部」;筛选命中少时不需要折叠。
  test('设置面板:插件列表默认只列 5 条,展开后全列、筛选后自动收窄', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, true, false, true), docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[aria-label="设置"] [data-sec="plugin"] .prow')
      const read = () =>
        page.evaluate(() => {
          const sec = document.querySelector('[aria-label="设置"] [data-sec="plugin"]')
          return { rows: sec.querySelectorAll('.prow').length, more: sec.querySelector('.more')?.textContent?.trim() ?? '' }
        })
      const before = await read()
      assert.equal(before.rows, 5, `默认应只列 5 条,实际 ${before.rows} 条`)
      assert.match(before.more, /还有 38 个/, `折叠按钮文案不对:「${before.more}」`)

      await page.click('[data-sec="plugin"] .more')
      const expanded = await read()
      assert.equal(expanded.rows, 43, `展开后应列全部 43 条,实际 ${expanded.rows} 条`)
      assert.equal(expanded.more, '收起', `展开后的按钮应为「收起」:「${expanded.more}」`)

      await page.fill('[data-sec="plugin"] input', 'host-plugin-42')
      const filtered = await read()
      assert.equal(filtered.rows, 1, `筛选后应只剩 1 条,实际 ${filtered.rows} 条`)
      assert.equal(filtered.more, '', '命中 ≤ 5 条时不应再出现折叠按钮')
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 浮层显隐动效(第七十七批):抽屉/停靠区不再是「啪」一下出现与消失。动效本身没法靠截图断言,
  // 这里钉它的两个可观测不变量 —— ① 退场那一瞬过渡确实挂在元素上(时长 > 0、透明度已在变);
  // ② 元素在退场期间仍留在 DOM(异步退场),过渡结束后必须清干净。删掉动效退回硬切,这条必红。
  // 角色面板(第七十九批 1b):装配态可展开编辑、挂载技能即时提交、删除走全局二次确认。
  // 这里钉的是**面板真的提交了** —— 只改本地状态的假保存(刷新即回)正是这类面板最容易犯的错。
  test('设置面板:角色段可展开编辑、挂载即提交、删除进二次确认', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      const stub = makeStub(true, true, false, false, true)
      page = await open(ctx, stub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[data-sec="role"] .prow')
      const rows = await page.evaluate(() =>
        Array.from(document.querySelectorAll('[data-sec="role"] .prow')).map((r) => r.textContent ?? ''),
      )
      assert.equal(rows.length, 2, `角色行数不对:${rows.length}`)
      assert.ok(rows[0].includes('当前'), '当前角色未标注「当前」')
      assert.ok(rows[1].includes('切换'), '非当前角色应有「切换」按钮')
      // 段导航能到达(与其它段同一套 key→DOM 机制)
      await page.click('.nav-it:has-text("角色")')
      await page.waitForTimeout(400)
      const nav = await page.evaluate(() => {
        const panel = document.querySelector('[aria-label="设置"]')
        const body = panel.querySelector('.body')
        const sec = body.querySelector('section[data-sec="role"]')
        return {
          on: panel.querySelector('.nav-it.on')?.textContent?.trim() ?? '',
          delta: Math.abs(sec.getBoundingClientRect().top - body.getBoundingClientRect().top),
        }
      })
      assert.equal(nav.on, '角色', `高亮没落在「角色」上,而是「${nav.on}」`)
      assert.ok(nav.delta <= 40, `「角色」段未对齐到内容区顶部:偏差 ${nav.delta}px`)

      // 切换另一个角色 → POST /use
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("切换")')
      await page.waitForTimeout(200)
      assert.ok(
        stub.seen.some((r) => r.method === 'POST' && r.path === '/api/roles/assistant/use'),
        `切换未提交到后端:${JSON.stringify(stub.seen)}`,
      )

      // 展开编辑:拉详情(正文只在展开时拉)+ 字节计数器按上限显示
      await page.click('[data-sec="role"] .prow:has-text("Finance-Analyst") button:has-text("编辑")')
      await page.waitForSelector('[data-sec="role"] textarea')
      const detail = await page.evaluate(() => {
        const sec = document.querySelector('[data-sec="role"]')
        const l = Array.from(sec.querySelectorAll('.fld-lab')).find((x) => (x.textContent ?? '').includes('/ 32768'))
        return { lab: l?.textContent?.trim() ?? '', ta: sec.querySelector('textarea')?.value?.length ?? 0 }
      })
      assert.match(detail.lab, /\/ 32768/, `工作规则未显示字节上限:「${detail.lab}」`)
      assert.ok(detail.ta > 0, '展开后未回填 AGENTS.md 正文')

      // 默认池 → 勾一个技能 = 切「替换」并把 skills 一起提交(只改本地会刷新即回)
      await page.click('[data-sec="role"] .m-list .m-item input[type=checkbox]')
      await page.waitForTimeout(200)
      const patch = stub.seen.find((r) => r.method === 'PATCH' && r.path === '/api/roles/finance')
      assert.ok(patch, `挂载未提交 PATCH:${JSON.stringify(stub.seen)}`)
      const pbody = JSON.parse(patch.body)
      assert.equal(pbody.skills_set, true, `挂载应带 skills_set=true:${patch.body}`)
      assert.ok(pbody.skills.includes('skill-alpha'), `挂载应带上被勾的技能:${patch.body}`)

      // 删除 = 有副作用 → 必须二次确认;取消后不得发请求
      const before = stub.seen.filter((r) => r.method === 'DELETE').length
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("删除")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("取消")')
      await page.waitForTimeout(150)
      assert.equal(stub.seen.filter((r) => r.method === 'DELETE').length, before, '取消确认后仍发出了 DELETE')
      await page.click('[data-sec="role"] .prow:has-text("Assistant") button:has-text("删除")')
      await page.waitForSelector('[aria-label="操作确认"]')
      await page.click('[aria-label="操作确认"] button:has-text("确认")')
      await page.waitForTimeout(200)
      assert.ok(
        stub.seen.some((r) => r.method === 'DELETE' && r.path === '/api/roles/assistant'),
        `确认后未删除:${JSON.stringify(stub.seen)}`,
      )
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 未装配 ctx.roles(真实后端 503)时整段隐藏:导航项与 section 都不出现,
  // 而不是渲染一个点不动的空壳(旧后端/裁剪装配下都会走到这条)。
  test('设置面板:未装配角色服务时「角色」段整段不出现', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true), docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[aria-label="设置"] .nav-it')
      const m = await page.evaluate(() => ({
        nav: Array.from(document.querySelectorAll('[aria-label="设置"] .nav-it')).map((b) => b.textContent?.trim() ?? ''),
        sec: !!document.querySelector('[data-sec="role"]'),
      }))
      assert.ok(!m.sec, '503 时不应渲染角色段')
      assert.ok(!m.nav.includes('角色'), `503 时导航不应有「角色」:${JSON.stringify(m.nav)}`)
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 展开的角色编辑区里全是「用户输入的长 token」(角色名/技能名/技能描述)—— 面板横向滚动条的
  // 高危位置(与长路径/长命令同类)。窄窗口下再验一遍。
  for (const vp of [{ w: 1200, h: 800 }, { w: 820, h: 560 }]) {
    test(`${vp.w}x${vp.h} 角色段展开后仍不出横向滚动条`, async (t) => {
      const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
      let page = null
      try {
        // 停靠收起:窄窗口下打开停靠抽屉会盖住底栏右侧的设置按钮(与本用例无关的干扰)
        page = await open(ctx, makeStub(true, true, false, false, true), docks[0].dock)
        await page.click('.gear')
        await page.waitForSelector('[data-sec="role"] .prow')
        await page.click('[data-sec="role"] .prow:has-text("Finance-Analyst") button:has-text("编辑")')
        await page.waitForSelector('[data-sec="role"] textarea')
        await page.waitForTimeout(200)
        const m = await page.evaluate(() => {
          const panel = document.querySelector('[aria-label="设置"]')
          const body = panel.querySelector('.body')
          const br = body.getBoundingClientRect()
          const nm = (el) =>
            el.tagName.toLowerCase() +
            (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : '')
          const off = []
          for (const el of body.querySelectorAll('[data-sec="role"] *')) {
            const bb = el.getBoundingClientRect()
            if (bb.width === 0 && bb.height === 0) continue
            if (bb.right > br.right + 1 || bb.left < br.left - 1) off.push(nm(el))
          }
          return { scrollW: body.scrollWidth, clientW: body.clientWidth, off: off.slice(0, 6) }
        })
        assert.ok(
          m.scrollW <= m.clientW + 1,
          `角色段展开了横向滚动条:scrollWidth=${m.scrollW} > clientWidth=${m.clientW};越界元素=${JSON.stringify(m.off)}`,
        )
      } catch (e) {
        await shoot(page, t.name)
        throw e
      } finally {
        await ctx.close()
      }
    })
  }

  // 状态栏角色徽标(第七十九批):角色名是底栏唯一「用户自定长度」的字段 ——
  // 长名字必须截断(实测未截断时文字折成多行,底栏 22px → 58px,并把右侧按钮挤出去)。
  for (const vp of [{ w: 1200, h: 800 }, { w: 820, h: 560 }, { w: 700, h: 460 }]) {
    test(`${vp.w}x${vp.h} 状态栏角色徽标截断显示且不破坏整页不变量`, async (t) => {
      const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
      let page = null
      try {
        page = await open(ctx, makeStub(true, true, false, false, true), docks[0].dock)
        const b = await page.evaluate(() => {
          const bar = document.querySelector('.statusbar-slot .bar')
          const badge = bar?.querySelector('.role')
          return {
            has: !!badge,
            // 单行:未截断的溢出文字会把 .bar 撑高(min-height 22px)
            h: bar ? Math.round(bar.getBoundingClientRect().height) : 0,
            barOver: bar ? bar.scrollWidth - bar.clientWidth : 0,
            // 长名字被省略:元素内文本比可见宽度长 ⇒ 出了省略号(而不是溢出到栏外)
            badgeOver: badge ? badge.scrollWidth - badge.clientWidth : 0,
          }
        })
        assert.ok(b.has, '状态栏未显示当前角色徽标')
        assert.ok(b.h <= 26, `底栏被角色名撑成多行:高度 ${b.h}px(应 ≤ 26)`)
        assert.ok(b.barOver <= 1, `底栏内容溢出 ${b.barOver}px(角色名未截断)`)
        assert.ok(b.badgeOver > 0, '超长角色名未被截断(应出现省略号)')
        assertInvariants(await measure(page))
      } catch (e) {
        await shoot(page, t.name)
        throw e
      } finally {
        await ctx.close()
      }
    })
  }

  test('浮层显隐带动效:设置抽屉与侧栏停靠区都不是硬切', async (t) => {
    // 显式声明不要 reduced-motion:宿主系统开了「减弱动效」时全局 CSS 会把时长压到 0.01ms,
    // 那时测的就不是我们的动效而是宿主偏好了。
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 }, reducedMotion: 'no-preference' })
    let page = null
    try {
      page = await open(ctx, apiStub, docks[1].dock)
      await page.click('.gear')
      await page.waitForSelector('[aria-label="设置"]')

      // 过渡类（enter/leave-active）只在过渡期间存在，故不能等它结束再看 computed style：
      // 点完给 Vue 20ms 刷完这轮 DOM（类已挂上、但 0.15s 的过渡远未走完）再读。
      // 容器与内容面分开读：淡出在容器（遮罩）上，位移/缩放在那层内容面（.panel）上。
      const probe = async (clickSel, watchSel, childSel) => {
        await page.evaluate((cs) => document.querySelector(cs)?.click(), clickSel)
        await page.waitForTimeout(20)
        return page.evaluate(
          ([ws, cs]) => {
            const read = (el) => {
              const st = el ? getComputedStyle(el) : null
              return {
                present: !!el,
                dur: st ? parseFloat(st.transitionDuration) || 0 : 0,
                prop: st ? st.transitionProperty : '',
                opacity: st ? parseFloat(st.opacity) : 1,
              }
            }
            const el = document.querySelector(ws)
            return { el: read(el), child: cs ? read(el?.querySelector(cs)) : null }
          },
          [watchSel, childSel ?? null],
        )
      }

      // 不透明度必须真的在变(不是只挂了类):rAF 里连续采 6 帧取最小值 —— 慢机器一帧掉到
      // 过渡结束也不怕(元素已摘 ⇒ 当作 0),快机器则能采到中间值。
      const faded = (ws) =>
        page.evaluate(
          (sel) =>
            new Promise((res) => {
              let min = 1
              let n = 0
              const tick = () => {
                const el = document.querySelector(sel)
                min = el ? Math.min(min, parseFloat(getComputedStyle(el).opacity)) : 0
                if (++n < 6) requestAnimationFrame(tick)
                else res(min)
              }
              requestAnimationFrame(tick)
            }),
          ws,
        )

      const pane = await probe('[aria-label="设置"] .x', '.mask', '[aria-label="设置"]')
      assert.ok(pane.el.present, '设置面板关掉后元素当场就没了 ⇒ 没有退场过程(硬切)')
      assert.ok(pane.el.dur > 0, `设置遮罩退场时长为 0(硬切):prop=${pane.el.prop}`)
      assert.match(pane.el.prop, /opacity/, `设置遮罩退场缺少淡出过渡:${pane.el.prop}`)
      assert.ok(pane.child?.dur > 0, `设置抽屉内容面退场时长为 0(硬切):prop=${pane.child?.prop}`)
      assert.match(pane.child?.prop ?? '', /transform/, `设置抽屉内容面退场缺少位移过渡:${pane.child?.prop}`)
      const paneOpacity = await faded('.mask')
      assert.ok(paneOpacity < 1, `设置遮罩退场没在动(opacity 始终为 ${paneOpacity})`)
      await page.waitForFunction(() => !document.querySelector('.mask'), null, { timeout: 2000 })

      // 侧栏停靠区（变更 / 看板 / 任务都在里面）同理；它在宽屏下是并排的 flex 兄弟，
      // 宽度变化仍是瞬时的（拖拽调宽要手感），但内容面不该硬切。
      const dock = await probe('[data-tip^="收起侧栏"]', '[data-ui-dock]')
      assert.ok(dock.el.present, '停靠区收起后元素当场就没了 ⇒ 没有退场过程(硬切)')
      assert.ok(dock.el.dur > 0, `停靠区退场时长为 0(硬切):prop=${dock.el.prop}`)
      assert.match(dock.el.prop, /opacity/, `停靠区退场缺少淡出过渡:${dock.el.prop}`)
      const dockOpacity = await faded('[data-ui-dock]')
      assert.ok(dockOpacity < 1, `停靠区退场没在动(opacity 始终为 ${dockOpacity})`)
      await page.waitForFunction(() => !document.querySelector('[data-ui-dock]'), null, { timeout: 2000 })
      await page.waitForFunction(() => !document.querySelector('[data-ui-dock]'), null, { timeout: 2000 })

      // 退场结束后外壳必须回到干净状态（动效不能把视图卡在半路）
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 发消息必须能看见回复:视图是全屏切换的,而 `/diff` 会把视图切到「变更」且此前没有
  // 任何逻辑切回 —— 真机反馈的「说什么都返回『本会话还没有捕获到文件改动』」就是它
  // (那是变更视图的空态,回复全进了看不见的会话流)。
  test('发消息自动切回会话流(变更视图不吞掉回复)', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, false), docks[0].dock)
      // 视图按钮循环:会话流 → 轨迹 → 变更。用空态里的「变更」自述确认确实切过去了。
      await page.click('[data-tip^="切换视图"]')
      await page.click('[data-tip^="切换视图"]')
      await page.waitForSelector('.chg .empty', { timeout: 5000 })
      // 非会话流视图必须常驻提醒「消息在会话流」(这条才是真机误判的根治:光靠发消息切回,
      // 切过来之后那一段时间依然看不到回复)。
      const bar = await page.textContent('.vbar-text')
      assert.ok(bar?.includes('会话流'), `非会话流视图应提示消息在会话流: ${bar}`)
      // 发一条消息(桥的 /api/input 走桖的 200 空数组即可 —— 判的是视图,不是回合内容)
      await page.fill('.input-slot textarea', 'hello')
      await page.keyboard.press('Enter')
      await page.waitForFunction(() => !document.querySelector('.chg .empty') && !document.querySelector('.vbar'), null, { timeout: 5000 })
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 回合进行中输入框不得锁死:宿主早已支持把消息注入当前回合(转向),前端一旦把
  // running 当 disabled 传下去,用户按 Enter 什么都不会发生 —— 真机反馈正是
  // 「会话进行时,输入框无法输入」。
  test('回合进行中可输入并 Enter 注入当前回合', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, makeStub(true, false, true), docks[0].dock)
      const ta = '.input-slot textarea'
      assert.equal(await page.isDisabled(ta), false, '回合进行中输入框不应被禁用')
      await page.fill(ta, '改成 B 方案')
      await page.keyboard.press('Enter')
      await page.waitForFunction(
        () => (document.querySelector('.stream-slot')?.textContent || '').includes('已注入当前回合'),
        null,
        { timeout: 5000 },
      )
      assert.equal(await page.inputValue(ta), '', '已受理的注入应清空草稿')
      // 运行中必须有中止出口:审批默认不限时等待,没有它就只剩「拒绝」一招。
      assert.ok(await page.isVisible('.stop'), '回合进行中应显示「停止」按钮')
      const [req] = await Promise.all([
        page.waitForRequest((r) => r.url().includes('/api/control')),
        page.click('.stop'),
      ])
      assert.ok((req.postData() || '').includes('"cancel":true'), '停止应发 cancel:true')
      assertInvariants(await measure(page))
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  test('侧栏开合语义:展开只有 .panel,收起只有 .handle', async (t) => {
    const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
    let page = null
    try {
      page = await open(ctx, apiStub, docks[1].dock)
      const openState = await measure(page)
      assertInvariants(openState)
      assert.equal(
        openState.handles,
        0,
        '侧栏展开时不该有收起态 ☰ 按钮(第五十二批事故:多渲染了一个 height:100% 的按钮把文档撑到 1573px)',
      )
      assert.equal(openState.panels, 1, '侧栏展开时应恰好一个 .panel')
      await page.click('.sidebar .toggle')
      await page.waitForTimeout(400)
      const closedState = await measure(page)
      assertInvariants(closedState)
      assert.equal(closedState.panels, 0, '侧栏收起时不该有 .panel')
      assert.equal(closedState.handles, 1, '侧栏收起时应恰好一个 ☰ 按钮')
    } catch (e) {
      await shoot(page, t.name)
      throw e
    } finally {
      await ctx.close()
    }
  })

  // 首启态(没配过 provider ⇒ App.vue 自动弹设置面板):遮罩层叠在主视图上时,外壳同样不许滚、
  // 不许有元素被挤出视口 —— 这是新用户第一眼看到的画面,也是最容易"没人测到"的状态。
  for (const vp of [{ w: 1200, h: 800 }, { w: 820, h: 560 }]) {
    test(`${vp.w}x${vp.h} 首启态(设置面板遮罩)`, async (t) => {
      const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
      let page = null
      try {
        await ctx.addInitScript(() => window.localStorage.setItem('gah.dock', JSON.stringify(docks[1].dock)))
        page = await ctx.newPage()
        await page.route('**/api/**', makeStub(false))
        await page.goto(baseURL(), { waitUntil: 'load' })
        await waitSkeleton(page)
        await page.waitForTimeout(400)
        const m = await measure(page)
        assert.ok(m.hasSettings, '首启态没弹出设置面板(桩没生效或引导逻辑变了)')
        assertInvariants(m)
      } catch (e) {
        await shoot(page, t.name)
        throw e
      } finally {
        await ctx.close()
      }
    })
  }
})

// 仪器自检:护栏本身失效是最危险的失败模式(全绿但什么都没测)。这里注入一个与 2026-09-22 事故
// 同形的越界元素(侧栏末尾 height:100% —— `.sidebar`/`.app` 无 overflow,故必须被检测到),
// 断言不变量**必须开火**;删掉后必须恢复干净。
test('检测器自检:注入越界元素必须被抓到,移除后必须恢复', { skip: skip && skipWhy }, async (t) => {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  let page = null
  try {
    page = await open(ctx, apiStub, docks[1].dock)
    await page.evaluate(() => {
      const el = document.createElement('div')
      el.className = 'canary-stray'
      el.style.height = '100%'
      el.textContent = 'canary'
      document.querySelector('.sidebar').appendChild(el)
    })
    const dirty = await measure(page)
    const fired = dirty.scrollHeight > dirty.clientHeight + 1 || dirty.scrolled !== 0 || dirty.outsideTotal > 0
    assert.ok(fired, `检测器没开火 —— 护栏本身失效:${JSON.stringify(dirty)}`)
    await page.evaluate(() => document.querySelector('.canary-stray')?.remove())
    assertInvariants(await measure(page))
  } catch (e) {
    await shoot(page, t.name)
    throw e
  } finally {
    await ctx.close()
  }
})

test('布局护栏:跳过原因(仅在没有浏览器/产物时输出)', { skip: !skip }, () => {
  console.log(`  跳过:${skipWhy}`)
})

// CI 的 ubuntu-latest(24.04)那两个参数能不能救场,本地验不了;但"什么失败该重试"这个判定
// 是纯函数,拿真实报错文案钉住 —— 既不让沙箱失败白挂,也不让装错浏览器被重试掩盖。
test('无沙箱重试判定:只对沙箱/namespace 类失败生效(ubuntu-latest 24.04 的 AppArmor)', () => {
  assert.ok(shouldRetryWithoutSandbox('No usable sandbox! If you are running on Ubuntu 23.10+ or another Linux distro...'))
  assert.ok(shouldRetryWithoutSandbox('Failed to move to new namespace: PID namespaces supported, Network namespace supported'))
  assert.ok(shouldRetryWithoutSandbox('Running as root without --no-sandbox is not supported'))
  assert.ok(!shouldRetryWithoutSandbox("Chromium distribution 'chrome' is not found at /opt/google/chrome/chrome"))
  assert.ok(!shouldRetryWithoutSandbox('spawn /usr/bin/google-chrome ENOENT'))
})
