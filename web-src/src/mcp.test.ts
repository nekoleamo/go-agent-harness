import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  MCP_MODE_DIRECT,
  MCP_MODE_SEARCH,
  canEdit,
  draftFrom,
  draftKey,
  fmtCommand,
  modeHint,
  modeLabel,
  normalizeCommand,
  searchSummary,
  isRemote,
  serverStateLabel,
  sourceLabel,
  toSavePayload,
  validateDraft,
  viewNotices,
} from './mcp.ts'
import type { McpServer, McpView } from './types.ts'

function srv(p: Partial<McpServer>): McpServer {
  return { name: 'alpha', command: 'npx', enabled: true, mode: MCP_MODE_DIRECT, loaded: true, tools: 3, source: 'file', ...p }
}
function view(p: Partial<McpView>): McpView {
  return { path: '/d/gah-data/config/mcp.yaml', servers: [], reload_available: true, plugin_loaded: true, ...p }
}

test('fmtCommand 拼接命令与参数,含空格加引号', () => {
  assert.equal(fmtCommand({ command: 'npx', args: ['-y', '@x/y'] }), 'npx -y @x/y')
  assert.equal(fmtCommand({ command: '/bin/sh', args: [] }), '/bin/sh')
  assert.equal(fmtCommand({ command: '/bin/x', args: ['a b'] }), '/bin/x "a b"')
  assert.equal(fmtCommand({ command: '', args: ['x'] }), 'x')
})

test('normalizeCommand 压缩空白', () => {
  assert.equal(normalizeCommand('  npx   -y   @x/y  '), 'npx -y @x/y')
  assert.equal(normalizeCommand('   '), '')
})

test('draftFrom 往返:args 拼回命令行,模式缺省为 direct', () => {
  const d = draftFrom(srv({ command: 'npx', args: ['-y', '@x/y'], mode: '' }))
  assert.deepEqual(d, { name: 'alpha', command: 'npx -y @x/y', enabled: true, mode: MCP_MODE_DIRECT })
})

test('canEdit:env 来源只读,文件来源可编辑', () => {
  assert.equal(canEdit(srv({ source: 'env' })), false)
  assert.equal(canEdit(srv({ source: 'file' })), true)
})

test('validateDraft 拦空命令/坏名称/重名/坏模式', () => {
  assert.equal(validateDraft([{ name: 'a', command: 'npx -y x', enabled: true, mode: MCP_MODE_DIRECT }]), '')
  assert.match(validateDraft([{ name: 'a', command: '  ', enabled: true, mode: MCP_MODE_DIRECT }]), /缺少启动命令/)
  assert.match(validateDraft([{ name: 'a b', command: 'x', enabled: true, mode: MCP_MODE_DIRECT }]), /只能用字母/)
  assert.match(
    validateDraft([
      { name: 'a', command: 'x', enabled: true, mode: MCP_MODE_DIRECT },
      { name: 'a', command: 'y', enabled: true, mode: MCP_MODE_DIRECT },
    ]),
    /重复/,
  )
  assert.match(validateDraft([{ name: 'a', command: 'x', enabled: true, mode: 'nope' }]), /模式非法/)
  // 留空名称(单 server 兼容)只允许一条
  assert.match(
    validateDraft([
      { name: '', command: 'x', enabled: true, mode: MCP_MODE_DIRECT },
      { name: '', command: 'y', enabled: true, mode: MCP_MODE_DIRECT },
    ]),
    /重复/,
  )
})

test('draftKey 脏检测:空白/引号差异不算改动', () => {
  const a = draftKey([{ name: ' a ', command: 'npx   -y', enabled: true, mode: MCP_MODE_DIRECT }])
  const b = draftKey([{ name: 'a', command: 'npx -y', enabled: true, mode: MCP_MODE_DIRECT }])
  assert.equal(a, b)
  const c = draftKey([{ name: 'a', command: 'npx -y', enabled: false, mode: MCP_MODE_DIRECT }])
  assert.notEqual(a, c)
})

test('serverStateLabel 覆盖停用/未连上/可用三态', () => {
  assert.equal(serverStateLabel({ enabled: false, loaded: true, tools: 3 }), '已停用')
  assert.equal(serverStateLabel({ enabled: true, loaded: false, tools: 0 }), '未连接或元工具未注册')
  assert.equal(serverStateLabel({ enabled: true, loaded: true, tools: 24 }), '24 个工具可用')
})

test('modeLabel/modeHint/sourceLabel', () => {
  assert.equal(modeLabel(MCP_MODE_SEARCH), '按需检索')
  assert.equal(modeLabel(''), '全量注册')
  assert.match(modeHint(MCP_MODE_SEARCH), /mcp_search/)
  // search 模式不再是「按工具名设不了审批」的代价(NOND-M1-3b:审批会作用到真实工具)
  assert.match(modeHint(MCP_MODE_SEARCH), /审批规则照样生效/)
  assert.doesNotMatch(modeHint(MCP_MODE_SEARCH), /不能按单个工具名/)
  assert.match(modeHint(MCP_MODE_DIRECT), /全部工具/)
  assert.equal(sourceLabel('env'), '环境变量')
  assert.equal(sourceLabel('file'), '配置文件')
  assert.equal(sourceLabel(undefined), '')
})

test('viewNotices:重载失败/配置告警/插件未运行', () => {
  assert.deepEqual(viewNotices(view({})), [])
  assert.deepEqual(viewNotices(view({ reload_err: '重启失败' })), ['重启失败'])
  assert.deepEqual(viewNotices(view({ notes: ['alpha: 命令为空'] })), ['alpha: 命令为空'])
  const notLoaded = viewNotices(view({ servers: [srv({})], plugin_loaded: false }))
  assert.equal(notLoaded.length, 1)
  assert.match(notLoaded[0], /MCP 插件未在运行/)
  // 无条目时不提示插件问题(还没配任何 server)
  assert.deepEqual(viewNotices(view({ plugin_loaded: false })), [])
})

test('searchSummary 只统计检索模式条目', () => {
  assert.equal(searchSummary(view({ servers: [srv({})] })), '')
  const v = view({
    servers: [srv({ name: 'a', mode: MCP_MODE_SEARCH, tools: 20 }), srv({ name: 'b', mode: MCP_MODE_SEARCH, tools: 4 }), srv({})],
  })
  assert.match(searchSummary(v), /共 24 个工具/)
})

// 远程 MCP server(transport: http)在设置面板里的形状(2026-10-23)。
//
// 为什么要有这组:http 传输逻辑与配置层早就交付并有用例,但**面板的形状里没有它** ——
// `McpDraft` 只有 name/command/enabled/mode。于是一个手写在 mcp.yaml 里的 http 条目
// 进面板就变成「名字 + 空命令」,而 `validateDraft` 会以「第 N 项缺少启动命令」把**整份
// 保存**拦下来:用户明明只想改另一条 stdio server 的名字,却被告知一个他根本没编辑过的
// 行缺命令。症状是「面板存不了,而且说的不是我想改的那行」。
//
// 反向验证:去掉 `isRemote` 分支(命令照旧必填)⇒ 第一条用例红。


test('http 条目不该被「缺少启动命令」拦下(它本来就没有命令)', () => {
  const view = {
    servers: [
      { name: 'local', command: 'deja', enabled: true, mode: 'direct', source: 'file', loaded: true, tools: 3 },
      { name: 'remote', command: '', enabled: true, mode: 'search', source: 'file', transport: 'http', url: 'https://mcp.example.com/rpc', loaded: true, tools: 5 },
    ],
  } as unknown as McpView
  const rows = view.servers.filter(canEdit).map(draftFrom)
  assert.equal(validateDraft(rows), '', '有 http 条目时整份保存不该被拦')
  // 反向判据:真·空命令的 stdio 条目仍然要拦(别把校验整体削弱了)
  const bad = [{ name: 'oops', command: '', enabled: true, mode: MCP_MODE_DIRECT, transport: '', url: '' }]
  assert.match(validateDraft(bad), /缺少启动命令/)
})

test('http 条目只读展示 url,且保存时仍带着它(不带 = 被后端当成删除)', () => {
  const remote = { name: 'remote', command: '', enabled: true, mode: MCP_MODE_DIRECT, transport: 'http', url: 'https://mcp.example.com/rpc' }
  assert.equal(isRemote(remote), true)
  // 保存形状里**保留**这一行:handleMCPSave 是「整份列表覆写」,漏掉同名项 = 删掉它
  // (而它的 url/headers 只在盘上,删了就再也找不回来)。
  const payload = toSavePayload([remote])
  assert.equal(payload.length, 1)
  assert.equal(payload[0].name, 'remote')
})
