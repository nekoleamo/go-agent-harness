// UI 插件加载器(M7.2):装配期扫描 /api/ui-plugins 聚合清单,对每个槽位覆盖声明
// 动态导入产物模块并按 priority 注册(registry.ts;同槽位 priority 降序,同优先级后注册者胜)。
// 契约:v1 冻结槽位名;渲染层禁止 v-html(v-html 源码级拒装见 gah -install-ui);
// 插件模块默认导出 Vue 组件(槽位 Props 兼容,见 registry.ts)。
import { ref } from 'vue'
import { registerSlot, registerSettingSection, registerSidebarAction, registerExtraPanel } from './registry'
import type { SlotName } from './registry'
import type { Component } from 'vue'
import { trustNoteOf, digestRows, toDigest } from './plugininfo'
import type { UIPluginDigest } from './plugininfo'

interface SlotDef {
  name: string // v1 四槽位 或 v2 扩展点(settings-section/sidebar-action/extra-panel)
  priority: number
  module: string
  title?: string // v2:面板/动作标题
}

interface UIPlugin {
  id: string
  version: string
  slots: SlotDef[]
  // 信任模型(后端 /api/ui-plugins 下发):UI 插件与宿主同源同权限,
  // 调用方可据此向用户明示"安装即完全信任"。
  trusted?: boolean
  trust_note?: string
  // 产物摘要(R10 ⑤-3):覆盖范围与降级原因随值一起下发,展示层照实转述
  sha256?: string
  hash_scope?: string
  hash_note?: string
}

// uiPluginTrustNote 后端下发的 UI 插件信任模型文案(设置面板照显;空 = 未取到/无插件)。
export const uiPluginTrustNote = ref('')

// uiPluginDigests 已安装 UI 插件的产物摘要行(设置面板显示;空 = 无插件/未取到)。
// 只放展示所需字段:面板不做任何"校验通过/不通过"的判断 —— 值由人拿去与发布方比对。
export const uiPluginDigests = ref<UIPluginDigest[]>([])

// loadUIPlugins 拉取聚合清单并安装覆盖组件(失败静默:默认实现保持)。返回已加载插件数。
export async function loadUIPlugins(): Promise<number> {
  let list: UIPlugin[]
  try {
    const resp = await fetch('/api/ui-plugins')
    if (!resp.ok) return 0
    list = (await resp.json()) as UIPlugin[]
  } catch {
    return 0
  }
  const V1 = ['stream', 'input', 'statusbar', 'confirm'] as const
  const EXT = ['settings-section', 'sidebar-action', 'extra-panel'] as const
  // 边界映射一次到位:API 是 snake_case,展示层是 camelCase(直接混用会静默丢字段)
  const digests = digestRows(list.map(toDigest))
  const note = trustNoteOf(digests)
  if (note) uiPluginTrustNote.value = note
  uiPluginDigests.value = digests
  let loaded = 0
  for (const p of list) {
    for (const slot of p.slots) {
      if (!slot.module) continue
      const name = slot.name
      const v1 = (V1 as readonly string[]).includes(name)
      const ext = (EXT as readonly string[]).includes(name)
      // 槽位名契约校验(未知槽位忽略,不破坏宿主渲染)
      if (!v1 && !ext) continue
      try {
        // 动态导入插件产物(经 server /ui-plugins/ 托管;绝对路径防 vite 产物路径重写)
        const url = `/ui-plugins/${p.id}/${slot.module.replace(/^\.\//, '')}`
        const mod = await import(/* @vite-ignore */ url)
        const comp = (mod.default ?? mod.component) as Component
        if (!comp) continue
        if (v1) {
          registerSlot(name as SlotName, { component: comp, priority: slot.priority || 10 })
        } else if (name === 'settings-section') {
          registerSettingSection(p.id, { component: comp, priority: slot.priority || 10, title: slot.title })
        } else if (name === 'sidebar-action') {
          registerSidebarAction(p.id, { component: comp, priority: slot.priority || 10, title: slot.title })
        } else if (name === 'extra-panel') {
          registerExtraPanel(p.id, { component: comp, priority: slot.priority || 10, title: slot.title })
        }
        loaded++
      } catch (e) {
        // eslint-disable-next-line no-console
        console.warn(`ui-plugin ${p.id}: 加载槽位 ${slot.name} 失败`, e)
      }
    }
  }
  return loaded
}
