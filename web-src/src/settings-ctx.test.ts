// 设置面板拆分(D1)的一致性护栏。
//
// 12 个分区子组件通过 provide/inject 共享父层的同一批 ref(D1:状态留父层)。
// 这套机制最大的风险是**静默漂移**:父层新增了一个顶层变量/import,子组件要用它,
// 但忘了重跑 gen-settings-ctx.mjs ⇒ 接口里没有这个名字 ⇒ vue-tsc 会在子组件里报错(好);
// 反过来,父层删了某个名字而 ctx 里还留着 ⇒ 子组件 inject 到 undefined(坏,而且静默)。
//
// 所以这里直接跑生成器的 --check 形态:与人工手改完全同源,不一致就红。
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { readFileSync, readdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')

test('settings-ctx 生成器与实际父组件一致(新增顶层变量/import 后要重跑)', () => {
  let out = ''
  try {
    out = execFileSync('node', [join(root, 'scripts/gen-settings-ctx.mjs'), '--check'], { encoding: 'utf8' })
  } catch (e) {
    assert.fail(`设置面板上下文已过期:\n${e.stdout || ''}${e.stderr || ''}`)
  }
  assert.match(out, /一致/)
})

test('每个分区子组件都通过 inject 取父层状态(没有偷偷自带一份 state)', () => {
  const dir = join(root, 'src/components/settings')
  const files = readdirSync(dir).filter((f) => /^Settings.*Section\.vue$/.test(f))
  assert.ok(files.length >= 10, `分区子组件应有 12 个,实得 ${files.length}`)
  for (const f of files) {
    const src = readFileSync(join(dir, f), 'utf8')
    assert.match(src, /inject\(SETTINGS_CTX\)/, `${f} 应通过 SETTINGS_CTX 取状态`)
    // 允许的局部声明只有三类:import / 类型断言辅助(模板里箭头函数参数要具体类型)/
    // computed(派生值,不是第二份状态)。若这里出现 ref()/reactive(),说明该段偷偷复制了
    // 状态 —— 两份真相必然漂。
    // 注意:只看**代码行**(先剥注释)—— 注释里写 `ref(` 不该误伤。
    const code = src.replace(/<!--[\s\S]*?-->/g, '').replace(/\/\*[\s\S]*?\*\//g, '').replace(/^\s*\/\/.*$/gm, '')
    assert.doesNotMatch(code, /\b(ref|reactive)\s*[(<]/, `${f} 不应自带状态(ref/reactive):状态留父层`)
  }
})

test('父面板只保留外壳(分区内容都已搬走)', () => {
  const src = readFileSync(join(root, 'src/components/SettingsPanel.vue'), 'utf8')
  const tpl = src.slice(src.indexOf('<template>'), src.lastIndexOf('</template>'))
  // data-sec 的静态分区应已全部搬走;只剩动态扩展点(:data-sec="'ext-' + s.key")
  const staticSecs = [...tpl.matchAll(/data-sec="([a-z-]+)"/g)].map((m) => m[1])
  assert.deepEqual(staticSecs, [], `父模板仍有未拆分区: ${staticSecs.join(', ')}`)
  // 外壳要素还在(类名可能多值:例如 .mask 同时带唯一作用域类 .gah-settings-mask)
  for (const cls of ['mask', 'panel', 'nav', 'body', 'gah-settings', 'gah-settings-mask']) {
    assert.match(tpl, new RegExp(`class="[^"]*\\b${cls}\\b`), `外壳缺少 .${cls}`)
  }
  // 样式作用域:一份非 scoped 表(前缀化),不是每个组件各编一份 scoped
  const css = readFileSync(join(root, 'src/components/settings/settings-panel.css'), 'utf8')
  assert.match(css, /^\.gah-settings /m, '设置面板样式应挂 .gah-settings 作用域')
  assert.doesNotMatch(
    readFileSync(join(root, 'src/components/SettingsPanel.vue'), 'utf8'),
    /<style scoped src="\.\/settings\/settings-panel\.css">/,
    '设置面板样式应由父组件一次性非 scoped 导入(不是 scoped 编译)',
  )
})
