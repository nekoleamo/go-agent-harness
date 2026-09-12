// 定时计划 UI 纯逻辑单测(node --test):cron 形状校验 + 中文时间回显。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { cronShapeError, fmtAbs, fmtNextRun, fmtRel, isUnsetTime, statusLabel } from './schedule.ts'

test('cronShapeError 放行合法形状(含区间/步长/列表/三字母)', () => {
  for (const ok of ['0 8 * * *', '*/5 * * * *', '0 0 1,15 * *', '0 9-18 * * 1-5', '30 6 * jan mon', '0 0 * * 7']) {
    assert.equal(cronShapeError(ok), '', `应放行: ${ok}`)
  }
})

test('cronShapeError 缺段/多段给人话(不吞错)', () => {
  assert.ok(cronShapeError('').includes('请填写'))
  assert.ok(cronShapeError('   ').includes('请填写'))
  assert.ok(cronShapeError('0 8 * *').includes('收到 4 段'))
  assert.ok(cronShapeError('0 8 * * * *').includes('收到 6 段'))
  assert.ok(cronShapeError('0 8 * *').includes('0 8 * * *'))
})

test('cronShapeError 非法字符给出段名', () => {
  const e = cronShapeError('0 8 x * *')
  assert.ok(e.includes('第 3 段'), e)
  assert.ok(e.includes('日'), e)
  assert.ok(cronShapeError('0 8 * * monday').includes('非法字符'))
  assert.ok(cronShapeError('0 8 * * 1,,2').includes('非法字符'))
  assert.ok(cronShapeError('0 8 * * 1/').includes('非法字符'))
  assert.ok(cronShapeError('0 8 1-2-3 * *').includes('非法字符'))
})

test('isUnsetTime 识别空值/Go 零时刻/坏串', () => {
  assert.equal(isUnsetTime(undefined), true)
  assert.equal(isUnsetTime(''), true)
  assert.equal(isUnsetTime('0001-01-01T00:00:00Z'), true)
  assert.equal(isUnsetTime('不是时间'), true)
  assert.equal(isUnsetTime('2026-11-15T08:00:00Z'), false)
})

test('fmtNextRun 无排期原因;有排期给绝对 + 相对', () => {
  const now = new Date(2026, 10, 14, 20, 0, 0) // 2026-11-14 20:00 本地
  assert.equal(fmtNextRun(undefined, now), '未排期')
  assert.equal(fmtNextRun('0001-01-01T00:00:00Z', now), '未排期')

  const today = new Date(2026, 10, 14, 21, 30, 0).toISOString()
  assert.equal(fmtNextRun(today, now), '今天 21:30(2 小时后)')

  const tomorrow = new Date(2026, 10, 15, 8, 0, 0).toISOString()
  assert.equal(fmtNextRun(tomorrow, now), '明天 08:00(12 小时后)')

  const far = new Date(2026, 11, 20, 8, 0, 0).toISOString()
  assert.equal(fmtNextRun(far, now), '2026-12-20 08:00(36 天后)') // 35.5 天,四舍五入
})

test('fmtNextRun 已到点/已过点不显示为未来', () => {
  const now = new Date(2026, 10, 14, 20, 0, 0)
  const soon = new Date(now.getTime() + 20 * 1000).toISOString()
  assert.ok(fmtNextRun(soon, now).includes('即将触发'), fmtNextRun(soon, now))
  const past = new Date(now.getTime() - 3600 * 1000).toISOString()
  assert.ok(fmtNextRun(past, now).includes('前'), fmtNextRun(past, now))
})

test('fmtAbs/fmtRel 跨天与分钟/小时/天档', () => {
  const now = new Date(2026, 10, 14, 20, 0, 0)
  const y = new Date(2026, 10, 13, 8, 30, 0).toISOString()
  assert.equal(fmtAbs(y, now), '昨天 08:30')
  assert.equal(fmtRel(new Date(now.getTime() - 30 * 1000).toISOString(), now), '刚刚')
  assert.equal(fmtRel(new Date(now.getTime() + 5 * 60000).toISOString(), now), '5 分钟后')
  assert.equal(fmtRel(new Date(now.getTime() + 3 * 3600000).toISOString(), now), '3 小时后')
  assert.equal(fmtRel(new Date(now.getTime() + 3 * 86400000).toISOString(), now), '3 天后')
})

test('statusLabel 覆盖四种终态', () => {
  assert.equal(statusLabel('ok'), '成功')
  assert.equal(statusLabel('failed'), '失败')
  assert.equal(statusLabel('skipped'), '跳过')
  assert.equal(statusLabel(''), '未运行')
  assert.equal(statusLabel(undefined), '未运行')
})
