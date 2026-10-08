// 渲染取件层单测(批二):LRU 淘汰与访问序、并发闸封顶与排队、缓存命中不再发请求、
// 失败不入缓存、哈希键的区分度。纯逻辑直跑(node --test,无 DOM)。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { DocRenderClient, Gate, LruCache, textKey } from './docfetch.ts'

test('textKey:同文本同键,异文本异键,长度参与', () => {
  assert.equal(textKey('abc'), textKey('abc'))
  assert.notEqual(textKey('abc'), textKey('abd'))
  // 经典 FNV-1a 碰撞对('a' 与 'b' 在某长度下同哈希)—— 长度前缀把它拆开
  assert.notEqual(textKey('a'), textKey('b'))
  assert.match(textKey(''), /^0:/)
})

test('LruCache:超容量淘汰最早的,命中会刷新访问序', () => {
  const c = new LruCache<number>(3)
  c.set('a', 1)
  c.set('b', 2)
  c.set('c', 3)
  assert.equal(c.get('a'), 1) // 访问序变为 b,c,a
  c.set('d', 4) // 容量满,淘汰最早的 b
  assert.equal(c.get('b'), undefined, 'b 应被淘汰')
  assert.equal(c.get('a'), 1, 'a 被访问过,不该淘汰')
  assert.equal(c.size, 3)
})

test('LruCache:重复 set 同键不增长', () => {
  const c = new LruCache<number>(2)
  c.set('a', 1)
  c.set('a', 2)
  assert.equal(c.size, 1)
  assert.equal(c.get('a'), 2)
})

test('Gate:同时在飞不超过 limit,其余排队且都会跑完', async () => {
  const g = new Gate(2)
  let started = 0
  let maxActive = 0
  const mk = (ms: number) => async () => {
    started++
    maxActive = Math.max(maxActive, g.inFlight)
    await new Promise((r) => setTimeout(r, ms))
    return started
  }
  const rs = await Promise.all([g.run(mk(10)), g.run(mk(10)), g.run(mk(1)), g.run(mk(1)), g.run(mk(1))])
  assert.equal(started, 5, '五个都要跑完')
  assert.equal(rs.length, 5)
  assert.ok(maxActive <= 2, `同时在飞不得超过 2,实测 ${maxActive}`)
  assert.equal(g.inFlight, 0, '跑完归零')
  assert.equal(g.waiting, 0, '队列清空')
})

test('Gate:失败照样放行(闸不许吞错也不许卡死后续)', async () => {
  const g = new Gate(1)
  const bad = g.run(async () => {
    throw new Error('boom')
  })
  await assert.rejects(bad, /boom/)
  assert.equal(await g.run(async () => 'ok'), 'ok', '前一个失败不卡住后续')
})

test('DocRenderClient:同文本只发一次请求', async () => {
  let calls = 0
  const c = new DocRenderClient(async () => {
    calls++
    return { blocks: ['b'] }
  })
  await c.render('hello')
  await c.render('hello')
  await c.render('hello')
  assert.equal(calls, 1, '三次调用只发一次请求')
})

test('DocRenderClient:并发同文本只发一次(等闸期间被别人渲染好)', async () => {
  let calls = 0
  const c = new DocRenderClient(
    async () => {
      calls++
      await new Promise((r) => setTimeout(r, 5))
      return { blocks: ['b'] }
    },
    { concurrency: 1 },
  )
  await Promise.all([c.render('x'), c.render('x'), c.render('x')])
  assert.equal(calls, 1, '并发同文本不能各发一次')
})

test('DocRenderClient:失败不入缓存(下一轮还能重试)', async () => {
  let calls = 0
  const c = new DocRenderClient(async () => {
    calls++
    if (calls === 1) throw new Error('暂时失败')
    return { blocks: ['b'] }
  })
  await assert.rejects(c.render('t'), /暂时失败/)
  const r = await c.render('t')
  assert.deepEqual(r.blocks, ['b'])
  assert.equal(calls, 2)
  assert.equal(c.stats().size, 1, '成功那次才入缓存')
})

test('DocRenderClient:超缓存容量后旧条目被淘汰(会重新发请求)', async () => {
  let calls = 0
  const c = new DocRenderClient(
    async () => {
      calls++
      return { blocks: [] }
    },
    { cacheSize: 2 },
  )
  await c.render('1')
  await c.render('2')
  await c.render('3') // 淘汰 '1'
  assert.equal(calls, 3)
  await c.render('1') // 被淘汰 ⇒ 重新发
  assert.equal(calls, 4)
})