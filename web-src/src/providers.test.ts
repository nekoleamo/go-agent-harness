// W3 首启引导纯逻辑单测(node --test;预设表自洽 + 探测错误翻译)。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { PROVIDER_PRESETS, explainProbeError, presetByName } from './providers.ts'

test('预设表自洽:名称唯一、base_url 为 http(s) 且不带尾斜杠、文案齐全', () => {
  const names = new Set<string>()
  for (const p of PROVIDER_PRESETS) {
    assert.ok(p.name.length > 0 && p.label.length > 0 && p.note.length > 0, `字段缺失: ${JSON.stringify(p)}`)
    assert.ok(!names.has(p.name), `名称重复: ${p.name}`)
    names.add(p.name)
    assert.match(p.base_url, /^https?:\/\//, `base_url 需 http(s): ${p.name}`)
    assert.ok(!p.base_url.endsWith('/'), `base_url 不应带尾斜杠: ${p.name}`)
  }
})

test('预设表不硬编码模型名(模型名会过期,保存后从实时列表里选)', () => {
  for (const p of PROVIDER_PRESETS) {
    assert.ok(!('model' in p), `预设不应带 model: ${p.name}`)
  }
})

test('本地端点(Ollama)标记为免 Key,其余预设需要 Key', () => {
  const ollama = presetByName('ollama')
  assert.equal(ollama?.key_optional, true)
  for (const p of PROVIDER_PRESETS) {
    if (p.name !== 'ollama') assert.ok(!p.key_optional, `只有本地端点免 Key: ${p.name}`)
  }
})

test('presetByName 命中与未命中', () => {
  assert.equal(presetByName('deepseek')?.base_url, 'https://api.deepseek.com/v1')
  assert.equal(presetByName('不存在'), undefined)
})

test('explainProbeError 按状态码/网络层字样给人话', () => {
  const cases: Array<[string, string]> = [
    ['HTTP 401 Unauthorized: invalid api key', '401'],
    ['403 Forbidden', '403'],
    ['404 Not Found', '404'],
    ['HTTP 429 too many requests', '429'],
    ['dial tcp: lookup api.xx.cn: no such host', '解析'],
    ['Get "http://localhost:11434/v1/models": dial tcp 127.0.0.1:11434: connect: connection refused', '没启动'],
    ['context deadline exceeded', '超时'],
    ['x509: certificate signed by unknown authority', '证书'],
    ['unsupported protocol scheme ""', 'http://'],
  ]
  for (const [raw, want] of cases) {
    const got = explainProbeError(raw)
    assert.ok(got.includes(want), `${raw} → ${got}(应含 ${want})`)
  }
})

test('explainProbeError 空值/未知错误回落到通用提示', () => {
  assert.ok(explainProbeError(undefined).includes('没有返回模型列表'))
  assert.ok(explainProbeError('').includes('没有返回模型列表'))
  assert.ok(explainProbeError('   ').includes('没有返回模型列表'))
  assert.ok(explainProbeError('something completely different 500').includes('不可达'))
})

// —— default_model(2026-10-06)——
// 原则:预设默认**不写死模型名**(模型名几个月一变,写死必然过期);唯一的例外是
// openrouter/free —— 官方维护的自动路由 id,不随免费清单变动而失效。
test('预设默认不写死模型名,唯一例外是 openrouter/free', () => {
  for (const p of PROVIDER_PRESETS) {
    if (p.default_model === undefined) continue
    assert.equal(p.name, 'openrouter', `只有 OpenRouter 允许预填模型:${p.name}`)
    assert.equal(p.default_model, 'openrouter/free')
  }
  // openrouter 之外不得有 default_model(否则这条原则就名存实亡)
  const withModel = PROVIDER_PRESETS.filter((p) => p.default_model)
  assert.equal(withModel.length, 1)
})

// —— OpenCode Go 预设 ——
// 它与 OpenRouter 的差别是**订阅制**且**按请求头接入**(官方要求自定义 UA + 每会话
// 稳定 session id),所以预设必须自带 default_headers,且这些头要能被用户看到并改。
test('OpenCode Go 预置了会话头(网关按头识别,不能藏成隐式行为)', () => {
  const go = presetByName('opencode-go')
  assert.ok(go, '应有 opencode-go 预设')
  assert.equal(go.base_url, 'https://opencode.ai/zen/go/v1')
  assert.ok(go.default_headers, '应预置自定义头')
  assert.match(go.default_headers, /x-opencode-session:\s*\$\{session\}/)
  // 订阅制 ⇒ 不能标成"本地免 Key"
  assert.ok(!go.key_optional, '需要订阅取 key,不该标免 Key')
  // 端点必须是 https
  assert.match(go.base_url, /^https:\/\//)
})

