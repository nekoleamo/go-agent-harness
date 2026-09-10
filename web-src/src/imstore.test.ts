// IM 状态通道单测(E-E;node --test)。
// 覆盖:SSE 帧 → 模块级状态(upsert 语义)、连接判定、相位文案(徽标/连接卡共用)。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { connected, imStatus, phaseLabel, upsertIMStatus } from './imstore.ts'
import type { IMConnectStatus } from './types.ts'

test('upsertIMStatus:写入相位并可判定已连接', () => {
  upsertIMStatus({ channel: 'wechat', phase: 'waiting_scan' } as IMConnectStatus)
  assert.equal(imStatus.value?.phase, 'waiting_scan')
  assert.equal(connected(), false)
  upsertIMStatus({ channel: 'wechat', phase: 'done', account: '…1234' } as IMConnectStatus)
  assert.equal(connected(), true)
  assert.equal(imStatus.value?.account, '…1234')
})

test('upsertIMStatus:null 不覆盖既有状态(离线/未装配不闪空)', () => {
  const before = imStatus.value
  upsertIMStatus(null)
  assert.equal(imStatus.value, before)
})

test('phaseLabel 覆盖全部相位与未知值', () => {
  const cases: Record<string, string> = {
    done: '已连接',
    waiting_scan: '待扫码',
    scanned: '已扫码',
    expired_refresh: '刷新二维码',
    validating: '校验中',
    failed: '失败',
    idle: '未连接',
    '': '未连接',
  }
  for (const [phase, want] of Object.entries(cases)) {
    assert.equal(phaseLabel(phase), want, `phase=${phase}`)
  }
  assert.equal(phaseLabel(undefined), '未连接')
})
