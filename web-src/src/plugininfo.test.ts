// UI 插件完整性展示逻辑单测(node --test 直跑,自包含无运行时依赖)。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { SHORT_HASH_LEN, digestLine, digestRows, scopeLabel, shortHash, toDigest, trustNoteOf } from './plugininfo.ts'

test('shortHash 截断前缀,空值原样返回空', () => {
  const full = 'a'.repeat(64)
  assert.equal(shortHash(full), 'a'.repeat(SHORT_HASH_LEN))
  assert.equal(shortHash('abc'), 'abc')
  assert.equal(shortHash(''), '')
  assert.equal(shortHash(undefined), '')
  assert.equal(shortHash('  '), '')
})

test('scopeLabel 只说人话:full 不显示,降级如实标注,未知值原样透出', () => {
  assert.equal(scopeLabel('full'), '')
  assert.equal(scopeLabel(undefined), '') // 后端省略 = full
  assert.equal(scopeLabel('entry'), '仅入口 + manifest')
  assert.equal(scopeLabel('none'), '无可校验产物')
  assert.equal(scopeLabel('weird'), 'weird')
})

test('digestLine 完整摘要:短哈希 + 无降级说明', () => {
  const line = digestLine({ id: 'demo', version: '1.0.0', sha256: 'b'.repeat(64), hashScope: 'full' })
  assert.equal(line, `demo 1.0.0 · sha256 ${'b'.repeat(SHORT_HASH_LEN)}`)
})

test('digestLine 降级:标注覆盖范围与原因(不把部分覆盖说成完整)', () => {
  const line = digestLine({
    id: 'demo',
    sha256: 'c'.repeat(64),
    hashScope: 'entry',
    hashNote: '产物共 9.0 MiB,超出摘要预算 4.0 MiB:仅摘要入口产物与 manifest',
  })
  assert.match(line, /仅入口 \+ manifest/)
  assert.match(line, /超出摘要预算/)
})

test('digestLine 无哈希 = 明说无法校验(不留空,避免被当成没问题)', () => {
  assert.equal(digestLine({ id: 'hollow' }), 'hollow · 无法校验')
  assert.equal(digestLine({ id: 'hollow', hashScope: 'none', hashNote: '产物目录无文件' }), 'hollow · 无法校验:产物目录无文件')
})

test('trustNoteOf 取后端下发的信任模型文案;无插件/无文案 → 空串', () => {
  assert.equal(trustNoteOf([]), '')
  assert.equal(trustNoteOf([{ id: 'a' }]), '')
  assert.equal(trustNoteOf([{ id: 'a' }, { id: 'b', trustNote: '同源同权限' }]), '同源同权限')
})

test('digestRows 过滤无效行(无 id / 空 id)', () => {
  const rows = digestRows([
    { id: 'a' },
    { id: '' } as unknown as { id: string },
    { id: '  ' },
    undefined as unknown as { id: string },
    { id: 'b' },
  ])
  assert.deepEqual(rows.map((r) => r.id), ['a', 'b'])
})

test('toDigest 边界映射:snake_case API 字段必须翻成展示字段(否则覆盖范围/降级原因静默消失)', () => {
  const d = toDigest({
    id: ' demo ',
    version: '1.2.3',
    sha256: 'd'.repeat(64),
    hash_scope: 'entry',
    hash_note: '产物共 9.0 MiB,超出摘要预算 4.0 MiB:仅摘要入口产物与 manifest',
    trusted: true,
    trust_note: '同源同权限',
  })
  assert.deepEqual(d, {
    id: 'demo',
    version: '1.2.3',
    sha256: 'd'.repeat(64),
    hashScope: 'entry',
    hashNote: '产物共 9.0 MiB,超出摘要预算 4.0 MiB:仅摘要入口产物与 manifest',
    trusted: true,
    trustNote: '同源同权限',
  })
  // 端到端串起来:映射后的行必须带出覆盖范围与原因(不是只显示一个裸哈希)
  const line = digestLine(toDigest({ id: 'demo', sha256: 'e'.repeat(64), hash_scope: 'entry', hash_note: '超预算' }))
  assert.match(line, /仅入口 \+ manifest/)
  assert.match(line, /超预算/)
})

test('toDigest 空值:无 id → 被 digestRows 过滤;无哈希 → 如实说无法校验', () => {
  assert.deepEqual(digestRows([toDigest({}), toDigest({ id: 'a' })]).map((r) => r.id), ['a'])
  assert.equal(digestLine(toDigest({ id: 'b', hash_scope: 'none', hash_note: '产物目录无文件' })), 'b · 无法校验:产物目录无文件')
})
