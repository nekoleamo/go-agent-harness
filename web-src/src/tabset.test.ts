// 页签容器单测(骨架批):这里钉的是**隔离**与**关闭语义**,不是像素。
//
// 之所以先钉这几条:页签化最贵的代价就是"状态串台" —— A 的消息混进 B、游标互相顶掉、
// 草稿丢字。骨架阶段没有 UI,一旦这些纯逻辑成立,后面加页签条只是把同一份状态画出来。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { TabSet } from './tabset.ts'

test('每会话一份状态,互不串台', () => {
  const tabs = new TabSet<{ msgs: string[] }>()
  const a = tabs.ensure('sA', () => ({ msgs: [] }), '甲')
  const b = tabs.ensure('sB', () => ({ msgs: [] }), '乙')
  a.state.msgs.push('A1')
  b.state.msgs.push('B1')
  assert.deepEqual(a.state.msgs, ['A1'])
  assert.deepEqual(b.state.msgs, ['B1'], 'B 不该看到 A 的消息')
  // 再次 ensure 同 id ⇒ 同一份状态(不能悄悄新建把内容丢了)
  assert.equal(tabs.ensure('sA', () => ({ msgs: [] })).state, a.state)
})

test('切换页签不动任何状态(滚动/草稿/未读各归各)', () => {
  const tabs = new TabSet<number>()
  tabs.ensure('sA', () => 1)
  const b = tabs.ensure('sB', () => 2)
  b.state = 22
  b.draft = '写了一半'
  b.scrollTop = 480
  b.atBottom = false
  tabs.activate('sA')
  assert.equal(tabs.activeId(), 'sA')
  assert.equal(tabs.get('sB')!.draft, '写了一半', '切走不应丢草稿')
  assert.equal(tabs.get('sB')!.scrollTop, 480, '切走不应重置滚动位置')
  tabs.activate('sB')
  assert.equal(tabs.activeId(), 'sB')
  assert.equal(tabs.activeTab()!.state, 22)
})

test('切回来即已读(未读标记只在没看的时候有效)', () => {
  const tabs = new TabSet<number>()
  tabs.ensure('sA', () => 1)
  tabs.activate('sA')
  tabs.markUnread('sA') // 本会话正在看 ⇒ 不该被标成未读
  assert.equal(tabs.get('sA')!.unread, false)
  tabs.markUnread('sB') // 不存在的页签:不凭空造
  assert.equal(tabs.has('sB'), false)
  tabs.ensure('sB', () => 2)
  tabs.markUnread('sB')
  assert.equal(tabs.get('sB')!.unread, true)
  tabs.activate('sB')
  assert.equal(tabs.get('sB')!.unread, false, '切回来就看到了')
})

test('关页签:激活页签落到左边那个,后台跑的要说清是后台', () => {
  const tabs = new TabSet<number>()
  tabs.ensure('sA', () => 1)
  tabs.ensure('sB', () => 2)
  tabs.ensure('sC', () => 3)
  tabs.activate('sB')
  tabs.markRunning(['sB'], 'sA') // sB 在跑
  const out = tabs.close('sB')
  assert.ok(out.closed)
  assert.equal(out.closed!.id, 'sB')
  assert.equal(out.nextActive, 'sA', '关中间那个应落到左边(与浏览器页签一致)')
  assert.equal(out.wasRunning, true, '关掉在跑的页签要能让文案说"后台继续跑"')
  assert.deepEqual(tabs.ids(), ['sA', 'sC'])
})

test('关页签:关的是非激活页签,激活不动', () => {
  const tabs = new TabSet<number>()
  tabs.ensure('sA', () => 1)
  tabs.ensure('sB', () => 2)
  tabs.activate('sA')
  const out = tabs.close('sB')
  assert.equal(out.nextActive, 'sA')
  assert.equal(tabs.activeId(), 'sA')
})

test('关掉最后一个页签:没有下一个,激活置空', () => {
  const tabs = new TabSet<number>()
  tabs.ensure('sA', () => 1)
  tabs.activate('sA')
  const out = tabs.close('sA')
  assert.equal(out.nextActive, '')
  assert.equal(tabs.activeId(), '')
  assert.equal(tabs.activeTab(), undefined)
  assert.equal(out.wasRunning, false)
})

test('关不存在的页签是 no-op(不吞错也不改状态)', () => {
  const tabs = new TabSet<number>()
  tabs.ensure('sA', () => 1)
  tabs.activate('sA')
  const out = tabs.close('zzz')
  assert.equal(out.closed, null)
  assert.equal(tabs.activeId(), 'sA')
  assert.equal(tabs.size(), 1)
})

test('页签上限:到顶就说满,并给出还能开几个', () => {
  const tabs = new TabSet<number>(2)
  tabs.ensure('sA', () => 1)
  tabs.ensure('sB', () => 2)
  assert.equal(tabs.atLimit(), true)
  assert.equal(tabs.room(), 0)
  // 上限只挡"新建",不挡"切回"已存在的页签
  assert.equal(tabs.activate('sA'), true)
  tabs.ensure('sC', () => 3) // 调用方据此禁用新建;容器本身不静默拒绝
  assert.equal(tabs.size(), 3)
})

test('切换到不存在的页签返回 false(不隐式创建 —— 那是"打开")', () => {
  const tabs = new TabSet<number>()
  tabs.ensure('sA', () => 1)
  assert.equal(tabs.activate('sB'), false)
  assert.equal(tabs.has('sB'), false)
})

test('空 id 归一到 main(与后端 sessionKey 同口径,别出现第三种键)', () => {
  const tabs = new TabSet<number>()
  const m = tabs.ensure('', () => 7)
  assert.equal(m.id, 'main')
  assert.equal(tabs.has('main'), true)
  assert.equal(tabs.get('main'), m)
})

test('运行标记按后端 running_sessions 批量对齐(含主会话归一)', () => {
  const tabs = new TabSet<number>()
  tabs.ensure('', () => 1) // main
  tabs.ensure('sB', () => 2)
  tabs.ensure('sC', () => 3)
  tabs.markRunning(['', 'sB'], 'main')
  assert.equal(tabs.get('main')!.running, true, '主会话在跑:空键映射到 main')
  assert.equal(tabs.get('sB')!.running, true)
  assert.equal(tabs.get('sC')!.running, false)
})

test('重放/清空换状态本体,但不动元信息与顺序', () => {
  const tabs = new TabSet<{ msgs: string[] }>()
  const t = tabs.ensure('sA', () => ({ msgs: ['旧'] }), '甲')
  t.draft = '草稿'
  t.scrollTop = 100
  tabs.replaceState('sA', { msgs: [] })
  assert.deepEqual(tabs.get('sA')!.state.msgs, [])
  assert.equal(tabs.get('sA')!.draft, '草稿', '换状态不该丢草稿')
  assert.equal(tabs.get('sA')!.scrollTop, 100)
  assert.deepEqual(tabs.ids(), ['sA'])
})
// 后端 running_sessions 报的是**会话 id**,主会话页签的键是 'main' —— 两个都要认。
test('主会话在跑:真实 id 能点亮 main 页签(反之不亮)', () => {
  const tabs = new TabSet<number>()
  tabs.ensure('', () => 1) // main 页签
  tabs.ensure('sB', () => 2)
  // 后端口径:主会话在跑时给的是它的真实 id
  tabs.markRunning(['20260107-143022'], '20260107-143022')
  assert.equal(tabs.get('main')!.running, true, '主会话在跑时 main 页签必须亮')
  assert.equal(tabs.get('sB')!.running, false)
  // 只有别的会话在跑时,main 不该被点亮
  tabs.markRunning(['sB'], '20260107-143022')
  assert.equal(tabs.get('main')!.running, false)
  assert.equal(tabs.get('sB')!.running, true)
})

// 首屏占位页改名:不能在首次 state 到达时新建第二个页签(用户会看到两个)。
test('rekey:占位页改绑到真实会话 id,状态与位置都保住', () => {
  const tabs = new TabSet<{ n: number }>()
  const t = tabs.ensure('main', () => ({ n: 1 }), '主会话')
  t.state.n = 42
  tabs.activate('main')
  assert.equal(tabs.rekey('main', '20260107-143022', '143022'), true)
  assert.equal(tabs.has('main'), false, '旧键不该留着(否则会多出一个幽灵页签)')
  assert.equal(tabs.has('20260107-143022'), true)
  assert.equal(tabs.activeId(), '20260107-143022', '改键后激活页签要跟着走')
  assert.deepEqual(tabs.get('20260107-143022')!.state, { n: 42 }, '状态必须保住')
  assert.equal(tabs.get('20260107-143022')!.title, '143022')
  assert.deepEqual(tabs.ids(), ['20260107-143022'], '顺序位置不变')
  assert.equal(tabs.rekey('nope', 'x'), false, '改不存在的页签要返回 false 而不是新建')
})

// 页签集合持久化:编解码是纯函数(浏览器 API 留在 App),所以这里能单测。
import { decodeTabs, encodeTabs } from './tabset.ts'

test('页签集合:编码→解码 往返一致', () => {
  const raw = encodeTabs(['sA', 'sB'], 'sB')
  assert.deepEqual(decodeTabs(raw), { ids: ['sA', 'sB'], active: 'sB' })
})

test('坏数据一律退化成空集合(不能让界面起不来)', () => {
  for (const bad of ['', 'not json', '[]', 'null', '{"ids":"x"}', '{"ids":[1,2]}', '"str"']) {
    assert.deepEqual(decodeTabs(bad), { ids: [], active: '' }, '输入:' + bad)
  }
  assert.deepEqual(decodeTabs(null), { ids: [], active: '' })
})

test('超量与不匹配的激活项按上限与首项兜底', () => {
  const many = encodeTabs(['a', 'b', 'c', 'd'], 'c')
  assert.deepEqual(decodeTabs(many, 2), { ids: ['a', 'b'], active: 'a' }, '超出上限只留前 N,激活落到首个')
  assert.deepEqual(decodeTabs(encodeTabs(['a', 'b'], 'zzz')), { ids: ['a', 'b'], active: 'a' })
  assert.deepEqual(decodeTabs(encodeTabs([], 'a'), 8), { ids: [], active: '' })
})
