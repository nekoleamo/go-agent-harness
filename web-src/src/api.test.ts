// api.upload 的拆包回归测试。
// 线上响应是 {ok, attachments:[视图]} 而不是单个视图 —— 这条错位正是 Windows 真机
// 「附件不可用: (空路径)」的成因(前端把包装层当视图用,拿到 undefined 的 url,
// 提交时 JSON.stringify 把 undefined 变 null,服务端解成空串)。
// 后端契约在 web/server.go handleAttachments,这里只盯前端的拆包与失败路径。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { api } from './api.ts'

// stubFetch 用一次性桩替换 globalThis.fetch,返回还原函数。
function stubFetch(handler: (url: string, init?: RequestInit) => Response): () => void {
  const orig = globalThis.fetch
  globalThis.fetch = ((url: unknown, init?: RequestInit) =>
    Promise.resolve(handler(String(url), init))) as typeof fetch
  return () => {
    globalThis.fetch = orig
  }
}

const view = {
  name: 'a.txt',
  path: '/home/u/gah/attachments/20260916-120000/a.txt',
  url: '/attachments/20260916-120000/a.txt',
  size: 3,
}

test('upload:拆开 {ok, attachments:[视图]} 并返回其中的视图', async () => {
  let seen = ''
  const restore = stubFetch((url, init) => {
    seen = url
    assert.equal(init?.method, 'POST')
    return new Response(JSON.stringify({ ok: true, attachments: [view] }), { status: 200 })
  })
  try {
    const v = await api.upload(new File(['abc'], 'a.txt', { type: 'text/plain' }))
    assert.equal(seen, '/api/attachments')
    assert.equal(v.url, view.url)
    assert.equal(v.path, view.path)
  } finally {
    restore()
  }
})

test('upload:响应没有视图或缺 url 时显式抛错,不退化成提交阶段才失败', async () => {
  const restore = stubFetch(() => new Response(JSON.stringify({ ok: true, attachments: [] }), { status: 200 }))
  try {
    await assert.rejects(() => api.upload(new File(['abc'], 'a.txt')), /附件上传响应异常/)
  } finally {
    restore()
  }
})

test('upload:非 2xx 透出服务端错误文本(便于用户看到真实原因)', async () => {
  const restore = stubFetch(() => new Response('附件类型不允许: application/x-msdownload', { status: 400 }))
  try {
    await assert.rejects(() => api.upload(new File([], 'x.exe')), /HTTP 400: 附件类型不允许/)
  } finally {
    restore()
  }
})
