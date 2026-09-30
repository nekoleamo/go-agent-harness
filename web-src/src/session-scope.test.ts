import { test } from 'node:test'
import assert from 'node:assert/strict'
import { boundSession, initSessionFromURL, sessionQS, setBoundSession } from './session-scope.ts'

// 多窗口作用域:URL ?session= 决定本窗口看哪个会话;没带 = 主会话(行为与从前一致)。
test('session-scope:未绑定时 query 为空(旧客户端零变化)', () => {
  setBoundSession('')
  assert.equal(boundSession(), '')
  assert.equal(sessionQS(), '')
  assert.equal(sessionQS({ before: 10, limit: 50 }), '?before=10&limit=50')
})

test('session-scope:绑定后所有 query 都带 session', () => {
  setBoundSession('20260930-101010')
  assert.equal(sessionQS(), '?session=20260930-101010')
  assert.equal(sessionQS({ after: 7 }), '?session=20260930-101010&after=7')
  assert.equal(sessionQS({ after: undefined }), '?session=20260930-101010')
})

test('session-scope:从 URL 读取并做 URL 解码往返', () => {
  const g = globalThis as unknown as { window?: { location: { search: string } } }
  const orig = g.window
  g.window = { location: { search: '?session=a-b%20c' } }
  try {
    assert.equal(initSessionFromURL(), 'a-b c')
    assert.equal(sessionQS(), '?session=a-b+c')
  } finally {
    if (orig) g.window = orig
    else delete g.window
    setBoundSession('')
  }
})

test('session-scope:无 window(SSR/单测环境)不炸,回落主会话', () => {
  const g = globalThis as unknown as { window?: unknown }
  const orig = g.window
  delete g.window
  try {
    assert.equal(initSessionFromURL(), '')
  } finally {
    if (orig !== undefined) g.window = orig
  }
})
