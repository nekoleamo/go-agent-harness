// Web 交互断言(2026-11-09,「自动化优先」第①批):把 A-2 里靠**手点一次**才成立的
// 用户路径改成真浏览器断言。
//
// 为什么从审批弹层开始:它是 Web 侧唯一「答错就等于丢了用户决定」的交互 ——
//   ① 点允许/拒绝必须真的发出对应的 POST(不是只把弹层关掉);
//   ② 提交失败**不许关弹层**(关掉 = 用户以为答过了,而回合会一直阻塞到超时);
// 这两条原来只写在文档的判据里,没有任何自动化碰过。纯函数用例(intervention.test.ts)
// 测的是"四态派生规则",订阅名写错或 api 调用接错依然全绿。
//
// 跑法:`cd web-src && npm run test:interaction`(需先有 web/dist;与布局护栏同一套基座)。
// 依赖:本机 Chrome/Chromium;找不到就跳过(CI 用 GAH_LAYOUT_REQUIRE=1 变红灯)。
import { after, test } from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { startStatic, makeStub, launchBrowser } from './harness.mjs'

const WEB_SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const DIST = path.resolve(WEB_SRC, '..', 'web', 'dist')
const REQUIRE = process.env.GAH_LAYOUT_REQUIRE === '1'

let server = null
let browser = null
let skip = false
let skipWhy = ''

function unavailable(why) {
  skip = true
  skipWhy = why
  if (REQUIRE) throw new Error(`GAH_LAYOUT_REQUIRE=1 但交互护栏跑不了:${why}`)
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
    console.log(`  交互护栏浏览器:${browser.version()}(${r.how})`)
  } catch (e) {
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

// openPage 打开一页(无额外帧):`running` = 桩按「回合进行中」下发状态。
async function openPage({ running = false } = {}) {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  const stub = makeStub(true, false, running)
  const page = await ctx.newPage()
  await page.addInitScript(() => {
    window.localStorage.setItem('gah.dock', JSON.stringify({ left: 0, right: 0 }))
    window.sessionStorage.setItem('gah.onboard.auto', '1')
  })
  await page.route('**/api/**', stub)
  await page.goto(baseURL(), { waitUntil: 'load' })
  await page.waitForSelector('.sidebar .panel, .sidebar .handle', { timeout: 10_000 })
  await page.waitForSelector('.session-item', { timeout: 10_000 })
  return { ctx, page, stub }
}

// openConfirm 打开一页并送一帧 confirm(审批)到 DOM。
// approve 返回值记录「点完之后桩收到了什么」,在用例里断言。
async function openConfirm({ confirmStatus = 200 } = {}) {
  const ctx = await browser.newContext({ viewport: { width: 1200, height: 800 } })
  const seen = []
  const frames = [{ event: 'confirm', payload: { id: 'c1', prompt: 'run shell: rm -rf /tmp/build' } }]
  const stub = makeStub(true, false, false, false, false, 'finance', 0, false, true, 1, false, false, '', frames)
  const page = await ctx.newPage()
  await page.addInitScript(() => {
    window.localStorage.setItem('gah.dock', JSON.stringify({ left: 0, right: 0 }))
    window.sessionStorage.setItem('gah.onboard.auto', '1')
  })
  // /api/confirm 归本用例(桩原先没有这条路由):记录请求,并可按用例要求失败。
  await page.route('**/api/**', async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === '/api/confirm') {
      seen.push({ method: route.request().method(), body: route.request().postData() })
      if (confirmStatus !== 200) {
        return route.fulfill({
          status: confirmStatus,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'boom' }),
        })
      }
      return route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
    }
    return stub(route)
  })
  await page.goto(baseURL(), { waitUntil: 'load' })
  await page.waitForSelector('.sidebar .panel, .sidebar .handle', { timeout: 10_000 })
  await page.waitForSelector('.dialog .allow', { timeout: 10_000 })
  return { ctx, page, seen }
}

// 审批弹层的两个按钮必须发出 **不同的** 决定 —— 按钮接错(两个都传 true)在界面上看不出。
for (const c of [
  { name: '允许', sel: '.dialog .allow', ok: true },
  { name: '拒绝', sel: '.dialog .deny', ok: false },
]) {
  test(`审批:点「${c.name}」发 /api/confirm 且弹层关闭`, { skip: skip && skipWhy }, async () => {
    const { ctx, page, seen } = await openConfirm()
    try {
      await page.click(c.sel)
      await page.waitForSelector('.dialog .allow', { state: 'detached', timeout: 5000 })
      assert.equal(seen.length, 1, `应恰好提交一次,实际 ${JSON.stringify(seen)}`)
      assert.equal(seen[0].method, 'POST')
      const body = JSON.parse(seen[0].body || '{}')
      assert.equal(body.id, 'c1', `审批 id 必须回传原请求的 id:${seen[0].body}`)
      assert.equal(body.ok, c.ok, `「${c.name}」应提交 ok=${c.ok}:${seen[0].body}`)
    } finally {
      await ctx.close()
    }
  })
}

// 提交失败必须**如实说未送达**、且不自动重试(重试等于替用户重复一个可能已生效的决定)。
//
// 这里**不断言"弹层还在"**:本桩的 SSE 是一次性静态 body,连接一结束前端就重连,
// 而桩会在重连时把同一帧 confirm 再发一遍 ⇒ 弹层总会"重新出现",那条断言在错误实现下
// 也会绿(实测:把 `confirm.value = null` 提到 try 之前,弹层断言仍通过)。
// 反向验证靠的是下面两条:错误实现下 catch 不执行 ⇒ 没有「未送达」这一行。
test('审批:提交失败如实提示未送达且不重试', { skip: skip && skipWhy }, async () => {
  const { ctx, page, seen } = await openConfirm({ confirmStatus: 503 })
  try {
    await page.click('.dialog .allow')
    // 先等请求真的发出去(否则可能断言在"还没点"的时刻)
    for (let i = 0; i < 40 && seen.length === 0; i++) await page.waitForTimeout(50)
    assert.equal(seen.length, 1, '失败路径也必须真的发出请求')
    // 给 catch 一点时间把提示推进会话流
    await page.waitForTimeout(300)
    assert.equal(seen.length, 1, '失败后不该自动重试(重试会重复一个可能已生效的决定)')
    // 会话流里要有一行说明(比文案不比字形:CJK 字体缺失时 textContent 仍在)
    const meta = await page.evaluate(() => document.body.textContent || '')
    assert.ok(meta.includes('审批应答未送达'), `应提示未送达,实际片段:${meta.slice(-300)}`)
  } finally {
    await ctx.close()
  }
})

// 侧栏会话项的**三档视觉**(A-2 #63 的"hover/选中区分"):常态 < 悬停 < 选中,两两不同。
//
// 为什么值得断言:F1 那次把"选中"做成卡片(填充+描边+左边条)、"悬停"只换底色 ——
// 两者若退化成同一个色,整页看上去仍然"正常",只有用起来才发现分不清当前会话。
// 判据取 computed background-color,不比具体色值:换主题不该假红。
test('侧栏:常态/悬停/选中三档背景各不相同', { skip: skip && skipWhy }, async () => {
  const { ctx, page } = await openPage()
  try {
    const idx = 1 // 0 可能是当前会话(带 .cur),取 1 拿常态
    const bg = (i) =>
      page.evaluate((n) => {
        const el = document.querySelectorAll('.session-item')[n]
        return el ? getComputedStyle(el).backgroundColor : null
      }, i)
    const base = await bg(idx)
    assert.ok(base, '应有第二个会话项')
    await page.hover(`.session-item >> nth=${idx}`)
    await page.waitForTimeout(250) // 底色过渡 0.15s
    const hover = await bg(idx)
    assert.notEqual(hover, base, `悬停底色应与常态不同(都是 ${base})`)
    const cur = await page.evaluate(() => {
      const el = document.querySelector('.session-item.cur')
      return el ? getComputedStyle(el).backgroundColor : null
    })
    if (cur) {
      assert.notEqual(cur, hover, `选中底色应与悬停不同(都是 ${cur})`)
      assert.notEqual(cur, base, `选中底色应与常态不同(都是 ${cur})`)
      // 选中项必须真的有填充(不是透明的"看着像选中")
      assert.ok(!/rgba\(0, 0, 0, 0\)|transparent/.test(cur), `选中项应有填充:${cur}`)
    }
  } finally {
    await ctx.close()
  }
})
