// S-P2-1 轻量版:侧栏停靠区(dock,单面板)的布局状态。
//
// 为什么需要它:轨迹 / 变更 / 看板此前只能**替换**会话流(切视图)或开**覆盖式抽屉**
// (任务面板),两种形态都做不到「一边看对话一边看能力区」—— dsh 生态里社区自发做三栏
// workbench,证明这是真实需求。本模块是**单面板停靠**的最小实现:一个可收起的右侧栏,
// 一次显示一个宿主面板,宽度可拖拽并记忆。
//
// 边界(有意不做,见 DESIGN S-P2-1 偏离登记):不做左/右双侧、不做根布局注册表、
// 不做插件自定义停靠布局(那才是 L 尺寸的 v3);插件 extra-panel 仍走既有覆盖式抽屉。
//
// 本模块是**纯函数 + 自包含**(无运行时 import,与 traj.ts/changes.ts/board.ts 同规矩),
// 故可被 `node --test` 直跑:布局状态的全部容错与收敛语义都在这里,组件只管渲染。
export interface DockState {
  /** 是否展开。 */
  open: boolean
  /** 当前停靠的面板 id(关闭时保留,下次打开回到这里)。 */
  panel: string
  /** 宽度(px)。 */
  width: number
}

export interface DockPanel {
  id: string
  label: string
}

/** 默认宽度(px)。 */
export const DOCK_DEFAULT_WIDTH = 380
/** 宽度下限:再窄就放不下 diff 行与卡片。 */
export const DOCK_MIN_WIDTH = 280
/** 宽度上限:超过这个宽度,对话流本身就被挤成一条了。 */
export const DOCK_MAX_WIDTH = 720
/** 主列最小可用宽度:视口不足时优先保对话流(停靠区退让,而不是把对话挤没)。 */
export const DOCK_KEEP_MAIN = 520
/** 视口窄于该值:停靠区退化为**覆盖式抽屉**(不挤压对话流,与窄屏抽屉同款)。 */
export const DOCK_NARROW_WIDTH = 900

/** 宿主内建停靠面板(顺序 = 标签行顺序;与视图循环的四态刻意不重合:轨迹仍只做替换视图)。 */
export const BUILTIN_PANELS: DockPanel[] = [
  { id: 'changes', label: '变更' },
  { id: 'board', label: '看板' },
  { id: 'jobs', label: '任务' },
]

const STORAGE_KEY = 'gah.dock'

/** newDock 默认停靠状态:收起、第一个内建面板、默认宽度(没有任何持久化时的基线)。 */
export function newDock(): DockState {
  return { open: false, panel: BUILTIN_PANELS[0].id, width: DOCK_DEFAULT_WIDTH }
}

/** panelOf 按 id 取面板定义(未知返回 null:调用方据此回落,不静默显示空白面板)。 */
export function panelOf(id: string): DockPanel | null {
  return BUILTIN_PANELS.find((p) => p.id === id) ?? null
}

/** panelLabel 面板中文名(未知 id 返回空串:标签行只渲染已知面板)。 */
export function panelLabel(id: string): string {
  return panelOf(id)?.label ?? ''
}

/**
 * clampDockWidth 宽度收敛:先夹进 [MIN, MAX],再让位给对话流(视口 - 主列最窄需求)。
 * 视口极小时不返回负数或超过上限的值(下限优先,保证停靠区仍可用)。
 */
export function clampDockWidth(px: number, viewport = 0): number {
  // 非有限值不能直接写进样式(NaNpx / Infinitypx):NaN 视为「没有值」回落默认,
  // 无穷大按方向夹到边界(JSON 里的 1e999 会被解析成 Infinity,不该变成默认宽度)。
  let want = DOCK_DEFAULT_WIDTH
  if (typeof px === 'number' && !Number.isNaN(px)) {
    want = Number.isFinite(px) ? Math.round(px) : px > 0 ? DOCK_MAX_WIDTH : DOCK_MIN_WIDTH
  }
  let hi = DOCK_MAX_WIDTH
  if (viewport > 0) hi = Math.min(hi, Math.max(DOCK_MIN_WIDTH, viewport - DOCK_KEEP_MAIN))
  return Math.min(Math.max(want, DOCK_MIN_WIDTH), hi)
}

/** isNarrow 窄屏判定(停靠区改走覆盖式抽屉;边界值取「小于」,900 本身仍并排)。 */
export function isNarrow(viewport: number): boolean {
  return Number.isFinite(viewport) && viewport > 0 && viewport < DOCK_NARROW_WIDTH
}

/** toggleDock 收起/展开(保留面板与宽度:再次打开回到用户上次看的那一屏)。 */
export function toggleDock(s: DockState): DockState {
  return { ...s, open: !s.open }
}

/** openDockPanel 打开并切到指定面板(未知 id 原样返回:不把状态改成无效面板)。 */
export function openDockPanel(s: DockState, id: string): DockState {
  if (!panelOf(id)) return s
  return { ...s, open: true, panel: id }
}

/** closeDock 收起(保留面板与宽度,与 toggleDock 的关闭路径同语义)。 */
export function closeDock(s: DockState): DockState {
  return { ...s, open: false }
}

/** persistKey 持久化键(组件用它读写 localStorage;键名集中在这里便于测试与迁移)。 */
export function persistKey(): string {
  return STORAGE_KEY
}

/**
 * parseDock 解析持久化布局:**逐字段容错**,坏一处不连坐。
 * 坏 JSON / 非对象 / 未知面板 / 宽度越界 / open 非布尔 各自回落,最终仍是可用状态。
 */
export function parseDock(raw: string | null | undefined): DockState {
  const base = newDock()
  if (!raw) return base
  let obj: unknown
  try {
    obj = JSON.parse(raw)
  } catch {
    return base
  }
  if (!obj || typeof obj !== 'object') return base
  const rec = obj as Record<string, unknown>
  const panel = typeof rec.panel === 'string' && panelOf(rec.panel) ? rec.panel : base.panel
  const width = clampDockWidth(typeof rec.width === 'number' ? rec.width : base.width)
  const open = typeof rec.open === 'boolean' ? rec.open : base.open
  return { open, panel, width }
}

/** serializeDock 序列化(只写三个字段:持久化形状稳定,不夹带组件状态)。 */
export function serializeDock(s: DockState): string {
  return JSON.stringify({ open: s.open, panel: s.panel, width: clampDockWidth(s.width) })
}
