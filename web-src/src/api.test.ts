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

// —— 错误体契约(第九十六批):后端两种形状并存,前端必须都读成人话 ——
// 背景:/api/models 这类未实现端点的 501 从前是纯文本,前端只能把原文当消息;
// 后端改成 JSON {error} 后,若前端不认识该形状,就会把 {"error":"…"} 原样甩给用户。
test('req:非 2xx 的 JSON {error} 读成人话,不透出 JSON 原文', async () => {
  const restore = stubFetch(
    () => new Response(JSON.stringify({ error: '当前适配器不支持列举模型(可手动设置): 列举接口未实现' }), { status: 501 }),
  )
  try {
    await assert.rejects(
      () => api.models(),
      (e: Error) => {
        assert.match(e.message, /^HTTP 501: 当前适配器不支持列举模型/)
        assert.ok(!e.message.includes('{'), `不该把 JSON 原文透出: ${e.message}`)
        return true
      },
    )
  } finally {
    restore()
  }
})

test('req:纯文本错误体仍原样透出(存量 http.Error 端点)', async () => {
  const restore = stubFetch(() => new Response('文档预览未装配(缺 ctx.doc / host-docview)', { status: 503 }))
  try {
    await assert.rejects(() => api.models(), /HTTP 503: 文档预览未装配/)
  } finally {
    restore()
  }
})

test('req:成功码却不是 JSON 时说清形状(而不是 Unexpected token)', async () => {
  const restore = stubFetch(() => new Response('<!DOCTYPE html><html><body>proxy error</body></html>', { status: 200 }))
  try {
    await assert.rejects(
      () => api.models(),
      (e: Error) => {
        assert.match(e.message, /HTTP 200: 响应不是 JSON/)
        assert.ok(/proxy error/.test(e.message), `应带原文片段便于排查: ${e.message}`)
        assert.ok(!/Unexpected token/.test(e.message))
        return true
      },
    )
  } finally {
    restore()
  }
})

// 会话作用域(多窗口):绑定后所有作用域请求都带 session=;未绑定时 query 为空。
// 这是「每个窗口看各自会话」的前端半边:api.ts 与 transport.ts 各自持有绑定
// (那两条路都被 Node 测试以 .ts 直载,不能互相 import),由 main.ts 同时注入。
test('api:bindSession 之后读侧请求带 session', async () => {
  api.bindSession('20260930-101010')
  const calls: string[] = []
  const orig = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    calls.push(String(input))
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
  try {
    await api.state()
    await api.sessionEvents(0, 50)
    assert.ok(calls[0].includes('session=20260930-101010'), 'state 应带 session: ' + calls[0])
    assert.ok(calls[1].includes('session=20260930-101010'), 'sessionEvents 应带 session: ' + calls[1])
    assert.ok(calls[1].includes('before=0') && calls[1].includes('limit=50'), '分页参数不得丢: ' + calls[1])
  } finally {
    globalThis.fetch = orig
    api.bindSession('')
  }
})

test('api:未绑定时 query 为空(旧客户端行为不变)', async () => {
  api.bindSession('')
  const calls: string[] = []
  const orig = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    calls.push(String(input))
    return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
  try {
    await api.state()
    assert.equal(calls[0], '/api/state')
  } finally {
    globalThis.fetch = orig
  }
})
