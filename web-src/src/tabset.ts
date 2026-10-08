// 页签状态容器(骨架;纯逻辑,零运行时依赖 —— 与 session-parallel / frame-routing 同理由:
// api.ts / transport.ts 被 Node 以 .ts 直载,视图逻辑得住自己的模块)。
//
// 为什么是**泛型**容器而不是直接管 StreamModel:直接管就得 import './sse',而这个模块
// 要同时能被浏览器构建与 Node 直载,两种 import 风格不能共存(sse.ts 是被 .ts 直载的)。
// 泛型把"每会话一份状态"这件事与"状态里装什么"解耦 —— 容器在本文件,StreamModel 在调用方。
//
// 本批(骨架)只做:per-会话 状态隔离 + 页签顺序/上限 + 关闭时的归属裁决。
// 页签条的绘制与切换交互在下一批,那时才会往 App.vue 里接 UI。

/** 页签除状态本体外的元信息(纯展示与交互所需;渲染归组件)。 */
export interface TabMeta {
  /** 会话 id(主会话也可能非空:归一后的对外 id,与后端口径一致)。 */
  id: string
  /** 页签标题(会话名/短 id;由调用方算,这里只存)。 */
  title: string
  /** 该会话显式设置的角色 id(空 = 跟随全局;页签徽标据此显示,只显示"自己设过的")。 */
  role?: string
  /**
   * 该会话有**会话级覆盖**(模型/思考/沙箱/审批里至少一项没跟随全局)。
   * 空 = 跟随全局。页签条据此画一枚方块标记(第一百三十四批 · 设置作用域收敛)。
   *
   * 与 role 分开表达:角色是全局概念(「这个会话用了哪个角色」由 @ 徽标说),
   * 这里只管**会话自己压过了全局默认**这件事 —— 两者成因不同,处置也不同。
   */
  custom?: boolean
  /** 该会话此刻有回合在跑(后端 running_sessions / 自己的 status 帧)。 */
  running: boolean
  /** 有新活动而用户没在看(切回来时清)—— 隐藏页签完成时靠它提示。 */
  unread: boolean
  /** 输入框草稿:切页签不得丢用户没发出去的话。 */
  draft: string
  /** 滚动位置/是否贴底:切回来应回到原处,而不是跳到最新。 */
  scrollTop: number
  atBottom: boolean
}

/** 一个页签 = 元信息 + 状态本体(状态类型由调用方给)。 */
export interface Tab<T> extends TabMeta {
  state: T
}

/** 关掉一个页签时该做什么(调用方在这里关连接/停监听)。 */
export interface CloseOutcome<T> {
  /** 被丢掉的页签(调用方负责释放其资源:关 SSE 连接等)。 */
  closed: Tab<T> | null
  /** 关闭后应当激活的页签(关掉的若是当前页签)。 */
  nextActive: string
  /** 被关的页签当时是否在跑回合(调用方据此提示"后台继续跑"而不是说"已停止")。 */
  wasRunning: boolean
}

export const DEFAULT_TAB_LIMIT = 8

export class TabSet<T> {
  private tabs = new Map<string, Tab<T>>()
  /** 页签顺序(打开顺序);删除用 splice 保序,不重排。 */
  private order: string[] = []
  private active = ''
  private limit: number

  constructor(limit: number = DEFAULT_TAB_LIMIT) {
    this.limit = limit
  }

  /** 当前激活页签 id(空 = 还没有页签)。 */
  activeId(): string {
    return this.active
  }

  /** 页签顺序副本(按打开顺序)。 */
  ids(): string[] {
    return this.order.slice()
  }

  size(): number {
    return this.order.length
  }

  has(id: string): boolean {
    return this.tabs.has(id || 'main')
  }

  /** 取页签(不存在 = undefined)。 */
  get(id: string): Tab<T> | undefined {
    return this.tabs.get(id || 'main')
  }

  /** 当前页签(没有则 undefined —— 不要隐式造一个:那是"静默改状态")。 */
  activeTab(): Tab<T> | undefined {
    return this.active ? this.tabs.get(this.active) : undefined
  }

  /** 取不到就造一个(首次打开某个会话时用;make 决定状态本体的初始值)。 */
  ensure(id: string, make: () => T, title = ''): Tab<T> {
    const k = id || 'main'
    let t = this.tabs.get(k)
    if (!t) {
      t = { id: k, title: title || k, running: false, unread: false, draft: '', scrollTop: 0, atBottom: true, state: make() }
      this.tabs.set(k, t)
      this.order.push(k)
    }
    if (title) t.title = title
    return t
  }

  /** 换掉某页签的状态本体(重放/清空会话流时用;不动顺序与元信息)。 */
  replaceState(id: string, state: T): Tab<T> {
    const t = this.ensure(id, () => state)
    t.state = state
    return t
  }

  /**
   * 把一个页签改绑到另一个会话 id(保留状态与位置)。
   *
   * 什么时候需要:首屏先落在"主会话"占位页上,等第一次 state 快照才知道真实的当前
   * 会话 id(启动即新建会话,主会话通常有个真 id)。那时不该新建第二个页签(用户会看到
   * 两个),而该把占位页改名过去。
   * 返回 false = from 不存在(不改任何东西)。
   */
  rekey(from: string, to: string, title = ''): boolean {
    const a = from || 'main'
    const b = to || 'main'
    if (a === b) return this.tabs.has(a)
    const t = this.tabs.get(a)
    if (!t) return false
    this.tabs.delete(a)
    const i = this.order.indexOf(a)
    if (i >= 0) this.order[i] = b
    t.id = b
    if (title) t.title = title
    this.tabs.set(b, t)
    if (this.active === a) this.active = b
    return true
  }

  /** 激活某页签;不存在时返回 false(不隐式创建 —— 那是"打开",不是"切换")。 */
  activate(id: string): boolean {
    const k = id || 'main'
    if (!this.tabs.has(k)) return false
    this.active = k
    const t = this.tabs.get(k)!
    t.unread = false // 切回来即已读
    return true
  }

  /**
   * 关掉一个页签。返回被丢掉的页签与下一个该激活的页签。
   * 关掉**正在跑**的页签不阻止(回合属于后端会话,不属于窗口 —— 桌面壳关窗口也是这个语义),
   * 但 wasRunning 会告诉调用方"它是后台继续跑的",文案不能说成"已停止"。
   */
  close(id: string): CloseOutcome<T> {
    const k = id || 'main'
    const t = this.tabs.get(k)
    if (!t) return { closed: null, nextActive: this.active, wasRunning: false }
    this.tabs.delete(k)
    const i = this.order.indexOf(k)
    if (i >= 0) this.order.splice(i, 1)
    let nextActive = this.active
    if (this.active === k) {
      // 激活哪个:优先左边那个(与浏览器的页签行为一致),没有就退到右边那个。
      const left = i > 0 ? this.order[i - 1] : undefined
      const right = this.order[i] // splice 之后 = 原右边那个
      nextActive = left ?? right ?? ''
      this.active = nextActive
      if (nextActive) {
        const nt = this.tabs.get(nextActive)
        if (nt) nt.unread = false
      }
    }
    return { closed: t, nextActive, wasRunning: t.running }
  }

  /** 还能再开几个(上限由构造参数给;0 = 到顶)。 */
  room(): number {
    return Math.max(0, this.limit - this.order.length)
  }

  /** 到顶了没有(调用方据此禁用"新建页签"并说明原因)。 */
  atLimit(): boolean {
    return this.room() === 0
  }

  /**
   * 批量对齐运行标记(后端 running_sessions → 各页签)。
   *
   * mainId 是主会话的真实 id:后端报的是**会话 id**(为了前端能对到会话列表),
   * 而主会话页签的键是 'main'(与后端 sessionKey 同口径)—— 两者都要认,否则
   * 主会话在跑时页签不亮(与 running_sessions 当初那同一个坑的反面)。
   */
  markRunning(runningIds: readonly string[], mainId: string): void {
    const ids = runningIds || []
    const set = new Set(ids.map((x) => x || 'main'))
    if (mainId && ids.includes(mainId)) set.add('main')
    for (const t of this.tabs.values()) t.running = set.has(t.id)
  }

  /** 给非当前页签记未读(隐藏页签完成/有待办时)。 */
  markUnread(id: string): void {
    const t = this.tabs.get(id || 'main')
    if (t && t.id !== this.active) t.unread = true
  }
}
/**
 * 页签集合的编解码(存 sessionStorage;纯函数所以能单测 —— 浏览器 API 留在调用方)。
 *
 * 存的是**视图状态**(这个窗口开着哪几个页签、当前看哪个),不是会话数据:
 * 流内容由各页签重连后按自己的游标补齐,所以这里一个字节的内容都不存。
 */
export function encodeTabs(ids: string[], active: string): string {
  return JSON.stringify({ ids, active })
}

/**
 * 解回页签集合。任何坏形状都退化成"空集合"(调用方按单页签默认走):
 * 持久化数据是旧版本/半写/被人手改过的都可能,**不能让界面起不来**。
 * 超量的只取前 limit 个 —— 上限是运行时口径,不因为存了什么就放开。
 */
export function decodeTabs(raw: string | null, limit: number = DEFAULT_TAB_LIMIT): { ids: string[]; active: string } {
  if (!raw) return { ids: [], active: '' }
  let parsed: unknown
  try {
    parsed = JSON.parse(raw)
  } catch {
    return { ids: [], active: '' }
  }
  // 解析成功不等于形状对:'null' / '42' / '"s"' 都过得了 JSON.parse。
  if (!parsed || typeof parsed !== 'object') return { ids: [], active: '' }
  const o = parsed as { ids?: unknown; active?: unknown }
  const ids = Array.isArray(o.ids) ? (o.ids as unknown[]).filter((x): x is string => typeof x === 'string' && x !== '') : []
  const clipped = ids.slice(0, Math.max(0, limit))
  const active = typeof o.active === 'string' ? o.active : ''
  return { ids: clipped, active: clipped.includes(active) ? active : clipped[0] ?? '' }
}
