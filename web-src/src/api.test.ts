// api.upload 的拆包回归测试。
// 线上响应是 {ok, attachments:[视图]} 而不是单个视图 —— 这条错位正是 Windows 真机
// 「附件不可用: (空路径)」的成因(前端把包装层当视图用,拿到 undefined 的 url,
// 提交时 JSON.stringify 把 undefined 变 null,服务端解成空串)。
// 后端契约在 web/server.go handleAttachments,这里只盯前端的拆包与失败路径。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { api, rolePackDownloadUrl, rolePackName, sessionExportName, sessionExportUrl } from './api.ts'

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

// 会话导出地址:两种格式与 id 空/需转义 的口径(侧栏导出菜单与桌面壳保存共用同一构造)。
// 角色包(第九十三批):导入必须走 multipart,且**覆盖必须是显式的** ——
// 默认不带 overwrite,后端才会对同名目标拒绝;不带 as 时不发空查询串。
test('rolePackImport:multipart 上传,缺省不带 overwrite/as', async () => {
  let url = ''
  let init: RequestInit | undefined
  const restore = stubFetch((u, i) => {
    url = u
    init = i
    return new Response(JSON.stringify({ ok: true, result: { id: 'finance', skills: [] } }), { status: 200 })
  })
  try {
    const file = new File(['PK'], 'gah-role-finance.zip', { type: 'application/zip' })
    await api.rolePackImport(file)
    assert.equal(url, '/api/rolepack')
    assert.equal(init?.method, 'POST')
    assert.ok(init?.body instanceof FormData, '必须是 FormData(JSON 传不了二进制)')
    const fd = init?.body as FormData
    assert.equal(fd.get('file')?.name ?? (fd.get('file') as File).name, 'gah-role-finance.zip')
  } finally {
    restore()
  }
})

test('rolePackImport:as 与 overwrite 走查询串(overwrite 只在确认后带)', async () => {
  const seen: string[] = []
  const restore = stubFetch((u) => {
    seen.push(u)
    return new Response(JSON.stringify({ ok: true, result: { id: 'copy', skills: [] } }), { status: 200 })
  })
  try {
    const file = new File(['PK'], 'p.zip')
    await api.rolePackImport(file, { as: 'copy' })
    await api.rolePackImport(file, { overwrite: true })
    await api.rolePackImport(file, { as: 'copy', overwrite: true })
    assert.deepEqual(seen, [
      '/api/rolepack?as=copy',
      '/api/rolepack?overwrite=1',
      '/api/rolepack?as=copy&overwrite=1',
    ])
  } finally {
    restore()
  }
})

test('rolePackImport:失败时把后端文案原样抛出(面板要用它判断"已存在")', async () => {
  const restore = stubFetch(() => new Response('角色 finance 已存在:要覆盖请加 force', { status: 400 }))
  try {
    await assert.rejects(() => api.rolePackImport(new File(['PK'], 'p.zip')), /角色 finance 已存在/)
  } finally {
    restore()
  }
})

test('rolePackDownloadUrl / rolePackName:与后端文件名同口径', () => {
  assert.equal(rolePackDownloadUrl('finance'), '/api/rolepack/finance')
  assert.equal(rolePackDownloadUrl('a b'), '/api/rolepack/a%20b')
  assert.equal(rolePackName('finance'), 'gah-role-finance.zip')
})

test('sessionExportUrl:缺省 jsonl,format=html 才带查询参数', () => {
  assert.equal(sessionExportUrl('abc', 'jsonl'), '/api/sessions/abc/export')
  assert.equal(sessionExportUrl('abc'), '/api/sessions/abc/export')
  assert.equal(sessionExportUrl('abc', 'html'), '/api/sessions/abc/export?format=html')
})

test('sessionExportUrl:id 空 = 主会话,特殊字符转义', () => {
  assert.equal(sessionExportUrl('', 'html'), '/api/sessions/export?format=html')
  assert.equal(sessionExportUrl('', 'jsonl'), '/api/sessions/export')
  assert.equal(sessionExportUrl('a/b c', 'jsonl'), '/api/sessions/a%2Fb%20c/export')
})

test('sessionExportName:与后端 Content-Disposition 同名', () => {
  assert.equal(sessionExportName('s1', 'html'), 'session-s1.html')
  assert.equal(sessionExportName('', 'jsonl'), 'session-main.jsonl')
})
