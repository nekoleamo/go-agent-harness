// turns.ts 单测:取消判定必须「宁可多显示一条,不可把真故障吞掉」。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { isUserCanceled } from './turns.ts'

test('识别 context canceled 与包装形态', () => {
  assert.equal(isUserCanceled('context canceled'), true)
  assert.equal(isUserCanceled('approval: 危险命令未执行(等待确认时被停止): 回合已中止(用户按了停止)'), true)
  assert.equal(isUserCanceled('策略 guard: web_fetch 未执行: 回合已中止'), true)
  assert.equal(isUserCanceled('回合已取消'), true)
})

test('普通错误不被吞掉', () => {
  assert.equal(isUserCanceled('搜索服务返回 402'), false)
  assert.equal(isUserCanceled('blocked: 沙箱拒绝写 workspace 外路径'), false)
  assert.equal(isUserCanceled('connection refused'), false)
  // 订阅/凭据语境里的 cancel 词不该命中(只认 canceled 与中文两种说法)
  assert.equal(isUserCanceled('cron job cancel failed'), false)
  assert.equal(isUserCanceled(''), false)
})