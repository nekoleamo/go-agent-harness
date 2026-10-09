// 会话视图缓存(第一百三十九批):**按会话 id** 记住「这个会话的流长什么样」。
//
// 为何不靠页签自带的那份(TabSet.state):那份的命是**页签**,不是会话 ——
// ① 关掉页签就连同流一起丢,下次从侧栏点开同一个会话又是「空白 → 尾窗重放 → 贴底」(实测闪一下);
// ② 从侧栏点开历史会话每次都先 ensure 成新页签 ⇒ 永远走「新会话」路径,缓存等于没有。
// 缓存的生命周期跟着**会话**走,与页签开合解耦:开过就留着,切回来直接接着看。
//
// 缓存里放什么(全都得放,少一样就是半拉子切换):
//   model 流 msgs + 窗口状态(from/hasMore/trimmed)/ pending 增量
//   traj  轨迹聚合(不回它,轨迹视图切回来就是空白)
//   changes 变更捕获(同上)
//   metas 前端侧产生的行(TPS 速记 / 命令回显 / 错误)—— 它们**不在会话账本里**,
//         不缓存就等于切一次丢一次(第一三八批刚加的 TPS 行首当其冲)
//   scrollTop / atBottom 滚动位置(用户读历史时被拽走是真打断)
//
// 存的是**响应式代理本身**(model.value 等),不是裸对象:换回去时赋值同一份代理 ⇒
// 身份不变、不重新包一层、缓存期间通过代理的写入都落在同一份数据上。
//
// LRU:访问即续命(Map 的插入序即最近使用序,命中后 delete+set 移到尾部),
// 超限丢最久没用的那份。默认上限 = 页签上限(8):同一量级,内存有界。
import type { StreamModel } from './sse'
import type { TrajModel } from './traj'
import type { ChangesModel } from './changes'
import type { MetaLine } from './registry'

// SESSION_CACHE_LIMIT 缓存里最多留几个会话的视图。与页签上限同量级(内存有界),
// 不从 tabset.ts 取常量而在这里另写一个:tsconfig 的 allowImportingTsExtensions=false
// 不允许裸模块带 .ts 后缀,而不带后缀的**值导入**会让本模块无法被 node --test 直接跑
// (单测在 sessioncache.test.ts)。两者相等由那条单测钉住,漂了就红。
export const SESSION_CACHE_LIMIT = 8

export interface SessionView {
  model: StreamModel
  traj: TrajModel
  changes: ChangesModel
  metas: MetaLine[]
  scrollTop: number
  atBottom: boolean
}

/**
 * switchPlan 切会话时的取数决策:命中缓存该保留什么、未命中该丢什么。
 *
 * 为何要抽成显式结构而不是一个裸布尔:裸布尔**被写反过一次**(命中缓存却当成“未命中”去清流,
 * 于是切回来先被清空再重放 —— 正是用户报的“闪一下”);后来又漏了一次:
 * metas 原来在 rebuild 里无条件清,把刚由缓存装回的 TPS 速记/命令回显又抹掉了。
 * 现在三件事由**同一个来源**给出(rebuild 直接吃这个结构),想只改一半而两一半同步不了就难了。
 * 极向与“三者同进同出”由 sessioncache.test.ts 钉住。
 */
export interface SwitchPlan {
  /** 保留当前流与游标(只补差集),命中缓存时为真。 */
  keepStream: boolean
  /** 清续传游标(与 keepStream 相反:模型不在了,游标就没意义)。 */
  clearCursor: boolean
  /** 清前端侧 meta 行(TPS 速记/命令回显/错误)—— 与流同生死。 */
  resetMetas: boolean
}

export function switchPlan(hasCache: boolean): SwitchPlan {
  return { keepStream: hasCache, clearCursor: !hasCache, resetMetas: !hasCache }
}

/** 淘汰回调(调用方据此释放外部资源,比如连带清掉游标)。 */
export type EvictFn = (id: string, view: SessionView) => void

function key(id: string): string {
  return id || 'main'
}

export class SessionCache {
  private views = new Map<string, SessionView>()
  private limit: number
  private onEvict: EvictFn | null = null

  constructor(limit: number = SESSION_CACHE_LIMIT) {
    this.limit = Math.max(1, limit)
  }

  /** 淘汰回调;设上之后超限丢缓存会通知(用于连带清续传游标)。 */
  setEvict(fn: EvictFn): void {
    this.onEvict = fn
  }

  /** 取并续命(未命中 = 这个会话没开过)。 */
  get(id: string): SessionView | undefined {
    const k = key(id)
    const v = this.views.get(k)
    if (!v) return undefined
    this.views.delete(k)
    this.views.set(k, v) // 移到尾部 = 最近使用
    return v
  }

  /** 存入并续命;超限淘汰最久没用的。 */
  put(id: string, view: SessionView): void {
    const k = key(id)
    this.views.delete(k)
    this.views.set(k, view)
    while (this.views.size > this.limit) {
      const oldest = this.views.keys().next()
      if (oldest.done) break
      const drop = this.views.get(oldest.value)!
      this.views.delete(oldest.value)
      this.onEvict?.(oldest.value, drop)
    }
  }

  /** 丢弃某会话的缓存(会话被删时调:留着就是给一个不存在的会话占内存)。 */
  drop(id: string): void {
    this.views.delete(key(id))
  }

  /**
   * 改名(占位页 'main' 改绑到真实会话 id 时用)。
   *
   * 为何必须跟:缓存键是**会话 id**,而页签首帧只知道占位 'main' —— 不跟着改,
   * 那份缓存就永远取不到了(切到真实 id 的页签时命中不了,白缓存)。
   * 目标键已有缓存时**保留既有的**(它更新鲜),把 from 那份丢掉。
   */
  rename(from: string, to: string): void {
    const a = key(from)
    const b = key(to)
    if (a === b) return
    const v = this.views.get(a)
    if (!v) return
    this.views.delete(a)
    if (!this.views.has(b)) this.views.set(b, v)
  }

  ids(): string[] {
    return [...this.views.keys()]
  }

  size(): number {
    return this.views.size
  }
}