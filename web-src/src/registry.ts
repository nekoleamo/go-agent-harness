// UI 槽位注册表(契约 v1,冻结签名):四槽位 stream/input/statusbar/confirm。
// M7.2 起:外部 UI 插件经 manifest 声明覆盖槽位,defineAsyncComponent 动态导入;
// 本注册表为宿主默认实现(优先级 0;插件以更高 priority 覆盖,同优先级安装序后者胜)。
// 契约:槽位名 kebab-case 跨档不变;渲染层禁止 v-html(Vue 模板转义天然保证)。
import type { Component } from 'vue'
import type { StateView, ConfirmRequest } from './types'
import type { Msg } from './sse'

// 槽位 Props(宿主注入,契约 v1;UI 插件组件必须兼容):
// stream    → { frames: Msg[]; metas: MetaLine[]; running: boolean }
//             (frames 是宿主消费引擎产出的展示消息 Msg,非原始 SessionEvent;
//              按 SessionEvent 实现槽位的插件会渲染空白——v1.1 已对齐)
// input     → { disabled: boolean; onSubmit(text: string): boolean | void; disabledHint?: string }
//             (onSubmit 返回 false = 未受理(断连/提交失败)→ 输入框保留草稿与附件;
//              S-P1-3;旧插件不返回值仍按已受理处理)
// statusbar → { state: StateView }
// confirm   → { request: ConfirmRequest | null; onAnswer(ok: boolean): void }
export interface MetaLine {
  kind: 'command' | 'error' | 'summary' | 'status'
  text: string
}
export interface StreamProps {
  frames: Msg[]
  metas: MetaLine[]
  running: boolean
}
export interface InputProps {
  disabled: boolean
  // onSubmit 返回 false = 未受理(断连/上游失败):输入框保留草稿与附件供重发(S-P1-3)。
  onSubmit: (text: string, attachments: string[]) => unknown
  // v1.1 可选扩展:真实运行状态(state.thinking/sandbox 驱动档位标签与循环);未实现不传也可
  state?: StateView
  // v1.2 可选扩展:禁用原因文案(如断连),覆盖默认的「回合进行中…」占位(S-P1-3)
  disabledHint?: string
}
export interface StatusbarProps {
  state: StateView
  // v1.2 可选扩展:链路三态(S-P1-3);旧插件不声明即可(attrs 透传无害,仅失去连接指示)
  conn?: 'open' | 'reconnecting' | 'offline'
}
export interface ConfirmProps {
  request: ConfirmRequest | null
  onAnswer: (ok: boolean) => void
}

export type SlotName = 'stream' | 'input' | 'statusbar' | 'confirm'

export interface SlotRegistration {
  component: Component
  priority: number // 越大越优先(插件覆盖宿主默认);默认 10
}

const slots = new Map<SlotName, { reg: SlotRegistration; order: number }>()
let installOrder = 0

// 宿主默认实现(内置组件注册;外部插件覆盖经 registerSlot)
export function registerDefault(name: SlotName, component: Component): void {
  const reg: SlotRegistration = { component, priority: 0 }
  if (!slots.has(name)) slots.set(name, { reg, order: installOrder++ })
  else slots.get(name)!.reg = reg
}

// UI 插件入口(manifest 声明覆盖槽位时调用;同名槽位按 priority 降序,同优先级后注册者胜)
export function registerSlot(name: SlotName, reg: SlotRegistration): void {
  const existing = slots.get(name)
  if (!existing) {
    slots.set(name, { reg, order: installOrder++ })
    return
  }
  const { reg: cur, order } = existing
  if (reg.priority > cur.priority || (reg.priority === cur.priority && installOrder > order)) {
    slots.set(name, { reg, order: installOrder++ })
  }
}

// 取槽位当前生效组件(无注册返回 null;宿主回退默认渲染)
export function slotComponent(name: SlotName): Component | null {
  return slots.get(name)?.reg.component ?? null
}

// —— v2 扩展点(B5):设置面板区段 / 侧栏动作 / 附加面板。多实例追加语义(v1 四槽位覆盖语义不变),
// 每插件以其 id 为 key 注册一个条目;同参数追加、priority 排序(降序,同优先级后注册者先)。
export type ExtensionName = 'settings-section' | 'sidebar-action' | 'extra-panel'

export interface ExtensionReg {
  key: string // 唯一键(插件 id)
  component: Component
  priority: number // 排序权重(降序;默认 10)
  title?: string // 面板标题/动作文案(宿主渲染)
}

type ExtMap = Map<string, ExtensionReg>
const sections = new Map<string, ExtensionReg>() // 设置面板区段(每插件一节)
const actions = new Map<string, ExtensionReg>() // 侧栏动作(每插件一条)
const panels = new Map<string, ExtensionReg>() // 附加面板(每插件一个可开抽屉)
let extOrder = 0

// extAdd 追加扩展条目(同 key 覆盖;priority 同时参与排序)。
function extAdd(m: ExtMap, key: string, reg: Omit<ExtensionReg, 'key'>): void {
  m.set(key, { key, component: reg.component, priority: reg.priority || 10, title: reg.title })
  extOrder++
}

// extSorted 转排序数组(priority 降序,同优先级注册晚者前)。
function extSorted(m: ExtMap): ExtensionReg[] {
  const arr = [...m.values()]
  arr.sort((a, b) => b.priority - a.priority)
  return arr
}

export function registerSettingSection(key: string, reg: Omit<ExtensionReg, 'key'>): void {
  extAdd(sections, key, reg)
}
export function registerSidebarAction(key: string, reg: Omit<ExtensionReg, 'key'>): void {
  extAdd(actions, key, reg)
}
export function registerExtraPanel(key: string, reg: Omit<ExtensionReg, 'key'>): void {
  extAdd(panels, key, reg)
}

export function settingSections(): ExtensionReg[] {
  return extSorted(sections)
}
export function sidebarActions(): ExtensionReg[] {
  return extSorted(actions)
}
export function extraPanels(): ExtensionReg[] {
  return extSorted(panels)
}
export function extraPanel(key: string): ExtensionReg | null {
  return panels.get(key) ?? null
}

// extSnapshot 扩展点快照(调试/文档/测试断言)。
export function extSnapshot(): Record<ExtensionName, string[]> {
  const names = (m: ExtMap) => [...m.values()].map((e) => e.key)
  return { 'settings-section': names(sections), 'sidebar-action': names(actions), 'extra-panel': names(panels) }
}

// 槽位契约快照(供 M7.2 文档/调试/测试断言)
export function slotSnapshot(): Record<SlotName, string> {
  const out = {} as Record<SlotName, string>
  for (const n of ['stream', 'input', 'statusbar', 'confirm'] as SlotName[]) {
    out[n] = slots.get(n)?.reg.component.name ?? '(none)'
  }
  return out
}
