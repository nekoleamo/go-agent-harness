#!/usr/bin/env node
// 把设置面板的样式表改成「一份前缀化全局表」(2026-10-24,项②)。
//
// 背景:12 个分区子组件各自以 `<style scoped src="../settings/settings-panel.css">` 引用同一份
// 样式表 ⇒ 每个组件带自己的 scope hash 把 126 条规则**各编译一遍**(单 chunk 184 KB 未压缩 /
// 11 KB gzip,单份约 13.9 KB)。Vue 的 scoped 是按模板绑定的,没法跨组件共享编译结果。
//
// 做法:父组件里**一次非 scoped 导入**这份表,所有规则挂 `.panel` 前缀。为什么特异性不变:
// scoped 编出来的 `.row[data-v-x]` 是 (0,2,0),`.panel .row` 也是 (0,2,0) ⇒ 与其它样式表的
// 层叠关系不变(这就是"零视觉回归"能成立的前提,判据见 test/style-baseline.json)。
//
// **只有 .panel 自身与它祖先(.mask)不能加前缀** —— 加了 `.panel .panel` 永远不自命中。
// 其余规则即便落在父模板元素上也能命中(它们是 .panel 的后代),所以这里不做"外壳/段内"的
// 类名分类:那个分类会随模板演化漂,而"根与祖先"是稳定的两条。
//
// 幂等:已经带 `.panel ` 前缀的选择器不再重复加。
//
// 用法:
//   node scripts/ns-settings-css.mjs            # 就地改写
//   node scripts/ns-settings-css.mjs --dry      # 只报告分类结果(不写文件)

import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const CSS = join(root, 'src/components/settings/settings-panel.css')
// 作用域类:**必须唯一**。
//
// 为什么要一个专用类而不是直接复用 .panel:`.panel` 这个名字在别处也在用
// (`Sidebar.vue` 的抽屉面板也叫 .panel)。把这份表全局化后,一条裸 `.panel { position:absolute;
// width:min(640px…); background:… }` 会命中侧栏抽屉 —— 2026-10-24 第一次改造就是这么把
// 「检测器自检 / 介入条」两条几何用例弄红的(它们的判据本身没错,错的是作用域)。
const SCOPE = '.gah-settings'
const SCOPE_MASK = '.gah-settings-mask'
// 这两条特殊:根元素自身与它的祖先(祖先不能写成 `.gah-settings X`,否则永不自命中)
const SELF = new Set([SCOPE, SCOPE_MASK])

export function classNames(selector) {
  return [...selector.matchAll(/\.(-?[A-Za-z_][\w-]*)/g)].map((m) => m[1])
}

// rewriteSelector 单个选择器:带 SHELL 的原样;已前缀的原样;其余加一次前缀。
export function rewriteSelector(sel) {
  const s = sel.trim()
  if (!s) return sel
  if (s === '.mask') return SCOPE_MASK
  if (s === '.panel') return SCOPE
  if (s.startsWith(SCOPE)) return s
  return `${SCOPE} ${s}`
}

// rewriteSelectorList 逗号列表逐个处理(选择器内不含 :is()/:not() 的逗号 —— 本表已核查)。
export function rewriteSelectorList(prelude) {
  return prelude
    .split(',')
    .map((part) => {
      const lead = part.match(/^\s*/)[0]
      const trail = part.match(/\s*$/)[0]
      return lead + rewriteSelector(part.trim()) + trail
    })
    .join(',')
}

// rewriteCss 逐块改写:跳过注释与 @media 条件,只改规则选择器。
export function rewriteCss(css) {
  let out = ''
  let i = 0
  while (i < css.length) {
    if (css.startsWith('/*', i)) {
      const j = css.indexOf('*/', i + 2)
      const end = j < 0 ? css.length : j + 2
      out += css.slice(i, end)
      i = end
      continue
    }
    const b = css.indexOf('{', i)
    const e = css.indexOf('}', i)
    if (e >= 0 && (b < 0 || e < b)) {
      out += css.slice(i, e + 1)
      i = e + 1
      continue
    }
    if (b < 0) {
      out += css.slice(i)
      break
    }
    let prelude = css.slice(i, b)
    i = b + 1
    // 注释可能夹在"上一条规则"与"下一条选择器"之间(本表就有:动效说明写在 .mask 之前)。
    // 必须先把注释提到规则外,否则它会被当成选择器的一部分 ⇒ `.panel /* … */ .mask`
    // 让 .mask 被错误前缀化(遮罩样式整体失效)。这是本脚本最容易犯的一件事。
    const comments = prelude.match(/\/\*[\s\S]*?\*\//g)
    if (comments) {
      prelude = prelude.replace(/\/\*[\s\S]*?\*\//g, '')
      out += comments.join('')
    }
    const t = prelude.trim()
    if (t.startsWith('@')) {
      out += prelude + '{' // @media/@supports:条件原样,内部规则继续按规则处理
      continue
    }
    out += rewriteSelectorList(prelude) + '{'
  }
  return out
}

const dry = process.argv.includes('--dry')
const src = readFileSync(CSS, 'utf8')
const out = rewriteCss(src)
const before = [...src.matchAll(/^[^{}\n][^{}]*\{/gm)].length
const scoped = [...out.matchAll(/^\.gah-settings /gm)].length
if (dry) {
  const untouched = [...out.matchAll(/^([^@{}\n][^{}]*)\{/gm)]
    .map((m) => m[1].trim())
    .filter((s) => !s.startsWith(`${SCOPE} `))
  console.log(`规则前 ${before} 条;前缀化后 ${scoped} 条;未加前缀(应为根/祖先/无类):`)
  for (const s of untouched) console.log('  ' + s)
} else {
  // 已经前缀化的文件再跑一次:输出应与输入相同(幂等)
  writeFileSync(CSS, out)
  console.log(`已改写 ${CSS}:${scoped} 条规则挂 ${SCOPE} 前缀`)
}
