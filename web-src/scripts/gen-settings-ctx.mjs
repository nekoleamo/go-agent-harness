#!/usr/bin/env node
// 生成设置面板的跨段上下文类型与 provide 列表(单一事实源)。
//
// 为什么要有这个脚本:设置面板有 12 个分区子组件,它们通过 provide/inject 共享父层的
// 同一批 ref / 常量(D1:状态留父层)。接口与 provide 对象必须与父组件的**实际顶层名字**一致,
// 否则子组件拿到 undefined —— 而模板渲染 undefined 是静默的(不报错、只是那一块空白)。
// 于是把"提取父层名字"这件事机械化:新增顶层变量 / import 后重跑本脚本即可。
//
// 用法:node scripts/gen-settings-ctx.mjs [--check]
//   --check  只校验(不一致则 exit 1,不写文件);CI / 单测用这个形态。
//
// 一致性由 src/settings-ctx.test.ts 守护(它直接比对本脚本的提取结果与 ctx.ts)。

import { readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const PANEL = join(root, 'src/components/SettingsPanel.vue')
const CTX = join(root, 'src/components/settings/ctx.ts')

// 父层顶层名字:声明 + 具名 import(排除 type-only)+ props/emit(defineProps/defineEmits 产物)
export function panelNames(source) {
  const start = source.indexOf('<script setup lang="ts">')
  const end = source.indexOf('</script>', start)
  const js = source.slice(start, end)
  const names = new Set()
  for (const m of js.matchAll(/^(?:const|let|function|async function)\s+([A-Za-z_$][\w$]*)/gm)) names.add(m[1])
  for (const m of js.matchAll(/^const\s*\{([^}]+)\}\s*=/gm)) {
    for (const part of m[1].split(',')) {
      const alias = part.split(':').pop().trim()
      if (alias) names.add(alias)
    }
  }
  for (const m of js.matchAll(/import\s*\{([^}]*)\}\s*from/gs)) {
    for (const raw of m[1].split(',')) {
      const part = raw.trim()
      if (!part || part.startsWith('type ')) continue
      const alias = part.split(' as ').pop().trim()
      if (alias) names.add(alias)
    }
  }
  names.add('props')
  names.add('emit')
  return [...names].sort()
}

function renderCtx(names) {
  return `// 设置面板的跨段上下文(provide/inject)。
//
// 为什么用 inject 而不是逐项 props:设置面板有 12 个分区、350+ 个顶层状态/函数/常量,而
// “状态留在父层”是既定契约(D1:先只拆文件不拆逻辑)。逐项 props 会写出比模板还长的清单,
// 每加一个字段要改三处(父传、子声明、子使用),必然漂。
//
// 类型面:SettingsCtx 由父组件实际顶层名字 + 具名 import 生成(any),子组件解构时**拼错名字
// 会让 vue-tsc 报错** —— 这正是故意不用 \`Record<string, any>\` 的原因(那个拼错只会静默渲染
// undefined)。本文件由 \`node scripts/gen-settings-ctx.mjs\` 生成,勿手改;
// 一致性由 src/settings-ctx.test.ts 守护。
import type { InjectionKey } from "vue";

export interface SettingsCtx {
${names.map((n) => `  ${n}: any`).join('\n')}
}

export const SETTINGS_CTX: InjectionKey<SettingsCtx> = Symbol("gah.settings-ctx");
`
}

function renderProvide(names) {
  return `// —— 跨段上下文(D1:按分区拆子组件;状态仍留在父层)——
// 子组件用 \`const { x } = inject(SETTINGS_CTX) as SettingsCtx\` 直接拿到**同一批 ref / 常量**,
// 既不复制状态也不引入第二份真相。列表由 scripts/gen-settings-ctx.mjs 生成(勿手改)。
provide(SETTINGS_CTX, {
  ${names.join(',\n  ')},
} as SettingsCtx);`
}

export function patchPanel(source, names) {
  const block = renderProvide(names)
  const re = /\/\/ —— 跨段上下文[\s\S]*?\} as SettingsCtx\);/
  if (!re.test(source)) throw new Error('SettingsPanel.vue 缺少 provide 块(先手工加一次再跑本脚本)')
  return source.replace(re, block)
}

const check = process.argv.includes('--check')
const panelSrc = readFileSync(PANEL, 'utf8')
const names = panelNames(panelSrc)
const wantCtx = renderCtx(names)
const wantPanel = patchPanel(panelSrc, names)

if (check) {
  const problems = []
  if (readFileSync(CTX, 'utf8') !== wantCtx) problems.push('src/components/settings/ctx.ts 与父组件顶层名字不一致')
  if (panelSrc !== wantPanel) problems.push('SettingsPanel.vue 的 provide 列表与顶层名字不一致')
  if (problems.length) {
    console.error(`设置面板上下文过期(${problems.length} 处):\n  ` + problems.join('\n  '))
    console.error('修法:node scripts/gen-settings-ctx.mjs')
    process.exit(1)
  }
  console.log(`设置面板上下文一致(${names.length} 个名字)`)
} else {
  writeFileSync(CTX, wantCtx)
  if (panelSrc !== wantPanel) writeFileSync(PANEL, wantPanel)
  console.log(`已生成 ctx.ts 与 provide 列表(${names.length} 个名字)`)
}
