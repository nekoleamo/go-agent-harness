// 首启引导判定单测(E-E;node --test,零新增依赖)。
// 覆盖 G-E4-R 的三种入口/关闭与连接态,防止重构把「只在桌面壳提示」规则改坏。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { GUIDE_ID_DESKTOP_IM, shouldShowGuide } from './guides.ts'

const base = { shell: 'desktop', dismissed: [] as string[] | null, specAvailable: true, phase: null as string | null }

test('桌面壳 + 有渠道 + 未连接 → 提示', () => {
  assert.equal(shouldShowGuide(base), true)
  assert.equal(shouldShowGuide({ ...base, phase: 'waiting_scan' }), true)
  assert.equal(shouldShowGuide({ ...base, phase: 'failed' }), true)
})

test('浏览器直连(无 shell 参数)不提示', () => {
  assert.equal(shouldShowGuide({ ...base, shell: null }), false)
  assert.equal(shouldShowGuide({ ...base, shell: 'web' }), false)
})

test('无 IM 渠道(未装配)不提示', () => {
  assert.equal(shouldShowGuide({ ...base, specAvailable: false }), false)
})

test('已连接不提示', () => {
  assert.equal(shouldShowGuide({ ...base, phase: 'done' }), false)
})

test('「不再提示」后不出现(跨端偏好)', () => {
  assert.equal(shouldShowGuide({ ...base, dismissed: [GUIDE_ID_DESKTOP_IM] }), false)
  assert.equal(shouldShowGuide({ ...base, dismissed: ['other'] }), true)
})

test('偏好不可得(null)按未关闭处理;相位不可得按未连接处理', () => {
  assert.equal(shouldShowGuide({ ...base, dismissed: null }), true)
  assert.equal(shouldShowGuide({ ...base, phase: null }), true)
})

test('组合:浏览器 + 已关闭 + 已连接 均不提示(各条件独立)', () => {
  assert.equal(
    shouldShowGuide({ shell: null, dismissed: [GUIDE_ID_DESKTOP_IM], specAvailable: false, phase: 'done' }),
    false,
  )
})
