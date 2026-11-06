// 稳态追加性能脚手架(2026-11-06;DESIGN 第一百八十七批 ⑥ / docs/WEB_OPTIMIZE.md §3)。
//
// 它要回答的问题只有一个:**会话流在「逐帧追加」时的每批主线程成本是多少**。
// 现有布局护栏(批零合帧护栏)量的是首屏长任务(>50ms 才算),看不见 14ms 这一档 ——
// 而 400 条窗口下每次追加的 patch 成本正好落在那一档(streamwin.ts 头注的实测口径)。
//
// 做法:
//   1. **真流** SSE —— 桩的 `page.route` 只能一次性 fulfill 静态串,量不出"一条一条来";
//      这里把 /api/events 交给真 http server 流式下发(其余端点仍走共享桩)。
//   2. **指标 = 页面内 rAF 间隔** —— 合帧(framequeue)后每批消费都落在 rAF 里,所以
//      "这一批花了多少主线程"直接体现为相邻两帧的间隔。零生产代码改动、零新依赖。
//   3. 分两段:先把窗口灌满(seed,模拟打开长会话),再按 interval 一帧一帧追加(steady)——
//      统计只取 steady 段(此时 `.msg` 条数已达窗口上限 400)。
//
// 跑法:`cd web-src && npm run test:perf`(需先 bash scripts/gen-web.sh)
//   GAH_PERF_REQUIRE=1   环境不满足时硬失败(CI 用);默认跳过(不拦人)
//   GAH_PERF_FRAMES=120  steady 段帧数
// 判据两条(唯一的基线数字记在 docs/WEB_OPTIMIZE.md §3):
//   ① 帧间距 p95 ≤ 2 帧(33ms):一帧装不下就会掉帧,这是用户能看见的那一档;
//   ② 批处理+patch **p50 ≤ 8ms**:当前基线是 3ms(400 条窗口,2026-11-06),帧间距在这一档
//      是盲的(装得下就恒定 17ms),所以必须有这条探针口径。上限设 8ms 的理由:把
//      「时间戳格式化忘了记忆化」那种回退(实测 12ms)当场抓住,又给慢机器留一倍余量。
// **度量自检**(2026-11-06 做过):把 MAX_LIVE_MSGS 临时改成 800 ⇒ ① 变红、② 从 12ms 涨到 21ms。
// 判据动不得松:松到 3 帧(=50ms)就等于「允许掉两帧」,那已经能看出卡顿了。

import { after, test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { startStatic, makeStub, launchBrowser } from './harness.mjs'

const WEB_SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const DIST = path.resolve(WEB_SRC, '..', 'web', 'dist')
const REQUIRE = process.env.GAH_PERF_REQUIRE === '1'
const STEADY_FRAMES = Number(process.env.GAH_PERF_FRAMES || 120) || 120

// 灌满窗口的量:800 条消息 = 400 回合(与 MAX_LIVE_MSGS=400 对齐,裁剪后正好满窗)。
const SEED_TURNS = 400
// 稳态追加的间隔:留出上一批 patch 的余量(比一帧长,避免把"背靠背两批"算成一条额外延迟)
const STEADY_INTERVAL_MS = 32

let server = null
let browser = null
let skip = ''
let baseURL = ''

if (!fs.existsSync(path.join(DIST, 'index.html'))) {
  skip = `缺少 ${DIST}/index.html:先 bash scripts/gen-web.sh 再跑本脚手架`
}

/** /api/events 的真流实现:先灌满窗口,再按 interval 一条一条追加。 */
function sseHook(state) {
  return (req, res) => {
    const u = new URL(req.url, 'http://127.0.0.1')
    if (u.pathname !== '/api/events') return false
    const now = new Date().toISOString()
    let seq = Number(u.searchParams.get('after') || 0) || 0
    const after = seq
    res.writeHead(200, {
      'Content-Type': 'text/event-stream; charset=utf-8',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
    })
    const frame = (kind, content, s) =>
      `event: session\ndata: ${JSON.stringify({
        id: s,
        type: 'session',
        ts: Date.now(),
        payload: {
          Kind: kind,
          Payload: kind === 'user/message' ? { Content: content } : { Content: content, ToolCalls: [] },
          Seq: s,
          TS: now,
        },
      })}\n\n`

    // 首连(after=0)按真实后端口径先给 baseline:窗口边界 + 是否还有更早的
    let buf = ''
    if (after === 0) {
      const total = SEED_TURNS * 2
      buf += `event: baseline\ndata: ${JSON.stringify({ count: total, has_more: false, window: total, from: 1, to: total })}\n\n`
      for (let i = 0; i < SEED_TURNS; i++) {
        buf += frame('user/message', `u${i}`, ++seq)
        buf += frame('assistant/message', `a${i} ` + 'A'.repeat(80), ++seq)
      }
      state.seededAt = Date.now()
    }
    res.write(buf)

    // 稳态:一条一条来(每条 = 一个浏览器任务 + 一次 rAF 批处理)
    let sent = 0
    const timer = setInterval(() => {
      if (sent++ >= STEADY_FRAMES) {
        clearInterval(timer)
        return
      }
      res.write(frame(sent % 2 ? 'user/message' : 'assistant/message', `s${sent} ` + 'B'.repeat(80), ++seq))
    }, STEADY_INTERVAL_MS)
    req.on('close', () => clearInterval(timer))
    state.streams = (state.streams || 0) + 1
    return true
  }
}

function percentile(xs, p) {
  const a = [...xs].sort((x, y) => x - y)
  return a[Math.min(a.length - 1, Math.floor((p / 100) * a.length))]
}

after(async () => {
  await browser?.close()
  server?.close()
})

test(`稳态逐帧追加:400 条窗口下 p95 帧间隔 ≤ 3 帧(${STEADY_FRAMES} 帧)`, { skip: skip || undefined }, async (t) => {
  if (!browser) {
    let chromium
    try {
      ;({ chromium } = await import('playwright-core'))
    } catch {
      const why = '未安装 playwright-core(零新增依赖口径:只用已有 devDependency)'
      if (REQUIRE) assert.fail(why)
      t.skip(why)
      return
    }
    let launched
    try {
      launched = await launchBrowser(chromium)
    } catch (e) {
      const why = `启动浏览器失败:${String(e.message).split('\n')[0]}`
      if (REQUIRE) assert.fail(why)
      t.skip(why)
      return
    }
    browser = launched.browser
    console.log(`  性能脚手架浏览器:${launched.how}`)
    const state = {}
    server = await startStatic(DIST, sseHook(state))
    baseURL = `http://127.0.0.1:${server.address().port}/`
  }

  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 }, reducedMotion: 'no-preference' })
  const page = await ctx.newPage()
  const stub = makeStub(true, false, false, false, false, 'finance', 0, false, true, 0)
  try {
    // 采集器:每帧记 [距上一帧 ms, 当前 .msg 条数]。必须在文档脚本前装,否则漏掉前面几帧。
    await page.addInitScript(() => {
      window.__gahPerf = true // 打开 App 侧的批处理探针(见 src/perfprobe.ts)
      window.__frames = []
      let last = performance.now()
      const tick = (t) => {
        window.__frames.push([Math.round(t - last), document.querySelectorAll('.msg').length])
        last = t
        requestAnimationFrame(tick)
      }
      requestAnimationFrame(tick)
    })
    // 除 /api/events 之外都走共享桩;/api/events 放行到真 http(route.continue)才能流式
    await page.route('**/api/**', (route) => {
      if (new URL(route.request().url()).pathname === '/api/events') return route.continue()
      return stub(route)
    })

    await page.goto(baseURL, { waitUntil: 'load' })
    // 窗口灌满(400 条)后才开始统计;steady 段每 32ms 一帧,120 帧 ≈ 4s
    await page.waitForFunction(() => document.querySelectorAll('.msg').length >= 400, { timeout: 30_000 })
    await page.waitForTimeout(STEADY_FRAMES * STEADY_INTERVAL_MS + 1500)

    const patched = await page.evaluate(() => window.__gahPatchMs || [])

    const frames = await page.evaluate(() => window.__frames)
    const full = frames.filter(([, n]) => n >= 400)
    assert.ok(full.length > 20, `稳态样本太少(${full.length} 帧),测量本身失效`)
    const deltas = full.map(([d]) => d)
    const p50 = percentile(deltas, 50)
    const p95 = percentile(deltas, 95)
    const worst = Math.max(...deltas)
    // 批处理 + patch 的真实耗时(探针口径):帧间隔只在一帧装不下时才动,这一档靠它看
    const tail = patched.slice(Math.floor(patched.length / 2))
    const bp50 = tail.length ? percentile(tail, 50) : -1
    const bp95 = tail.length ? percentile(tail, 95) : -1
    console.log(
      `  稳态追加(${full.length} 帧样本,窗口 400 条):` +
        `帧间距 p50 ${p50}ms / p95 ${p95}ms / 最差 ${worst}ms;` +
        `批处理+patch(后一半样本)p50 ${bp50}ms / p95 ${bp95}ms / n=${tail.length}`,
    )
    assert.ok(p95 <= 33, `稳态帧间距 p95 = ${p95}ms(上限 33ms = 2 帧);分布=${JSON.stringify(deltas.slice(0, 40))}`)
    assert.ok(
      bp50 > 0 && bp50 <= 8,
      `批处理+patch p50 = ${bp50}ms(上限 8ms;基线 3ms)—— 逐帧追加的主线程成本回退了;` +
        `先看是不是又加了每行每次渲染都跑的 Intl/正则/字符串拼接(见 src/perfprobe.ts 头注)。样本=${JSON.stringify(tail.slice(0, 40))}`,
    )
  } catch (e) {
    throw e
  } finally {
    await ctx.close()
  }
})
