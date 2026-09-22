// A-5#124 浏览器**系统通知**能力(按来源降级;口见 docs/VERIFY.md 该条)。
//
// 口径:localhost 下才可授权系统通知;经 **LAN IP + http** 访问时降级为页内提示
// (不申请权限、不报错)。页内提示始终由 ToastStack 承担(与来源无关,见 notices.ts),
// 本模块只决定"要不要 / 能不能"弹系统通知。
//
// 三条纪律:
//   - **非本机来源永不调用 requestPermission**:不弹权限条、不写 denied,静默降级;
//   - **只在用户手势里申请权限**(浏览器策略:无手势的自动请求会被静默拒,等于把权限钉死);
//   - **一次会话只申请一次**,被拒后不再打扰(结果只留内存,不持久化 —— 下次启动尊重浏览器现状)。
//
// 零运行时依赖:宿主能力(Notification)经依赖注入,便于单测与无 Notification 环境。
import type { Notice } from './types'

// 本机来源字面量(与 web 侧 hostAllowed 的回环集合同口径)。
const LOCAL_HOSTS = new Set(['localhost', '127.0.0.1', '::1', '[::1]'])

// isLocalHost 是否本机来源:只有本机来源才允许申请系统通知。
// `*.localhost`(RFC 6761 保留域,浏览器也解析到回环)一并算本机。
export function isLocalHost(hostname: string): boolean {
  const h = (hostname || '').toLowerCase()
  return LOCAL_HOSTS.has(h) || h.endsWith('.localhost')
}

// wantsSystemNotify 是否值得打扰人:与 TUI / 桌面壳同口径 —— 只有 warn/error 弹系统通知,
// info 只进页内 toast(否则通知退化成噪音)。
export function wantsSystemNotify(level: string | undefined): boolean {
  return level === 'warn' || level === 'error'
}

// NotificationLike 浏览器 Notification 静态面(只取用到的两个成员)。
export interface NotificationLike {
  permission: string
  requestPermission(): Promise<string>
}

export interface NotifierDeps {
  hostname: string
  api?: NotificationLike | undefined
  ctor?: ((title: string, opts?: { body?: string }) => unknown) | undefined
}

export interface Notifier {
  // 首次用户手势时调用:本机来源才申请权限,且整个会话只申请一次。
  maybeRequest(): void
  // 收到一条提示时调用:满足全部条件才真弹系统通知;返回 1/0(便于确定性断言)。
  fire(n: Notice): number
  // 弹一条**非提示流**的系统通知(回合结束 / 需要人确认)。
  // 与 fire 的差别:不按级别过滤 —— 它不是 warn/error。权限与来源判定同 fire。
  // 调用纪律:只在页面**不可见**时调 —— 用户正看着界面时弹系统通知纯属噪音。
  fireEvent(title: string, body?: string): number
  // 当前是否真的会弹系统通知(诊断/测试用)。
  enabled(): boolean
}

// createNotifier 造一个通知器。无 Notification 能力(旧浏览器 / http 非安全上下文)时
// 一切调用都是**安全空操作**:不申请、不弹、不抛错(降级为页内提示)。
export function createNotifier(deps: NotifierDeps): Notifier {
  let asked = false
  let refused = false
  // 本会话内拿到的授权(不依赖宿主 `permission` 立即回写 —— 有的实现异步更新)。
  let granted = false
  const supported = (): boolean => !!deps.api && !!deps.ctor

  const enabled = (): boolean =>
    supported() && !refused && isLocalHost(deps.hostname) && (granted || deps.api!.permission === 'granted')

  const maybeRequest = (): void => {
    if (!supported() || asked || !isLocalHost(deps.hostname)) return
    asked = true
    const perm = deps.api!.permission
    if (perm === 'granted') {
      granted = true
      return
    }
    if (perm === 'denied') {
      refused = true // 用户此前已拒绝:不再弹权限条打扰
      return
    }
    void Promise.resolve(deps.api!.requestPermission())
      .then((p) => {
        if (p === 'granted') granted = true
        else refused = true
      })
      .catch(() => {
        refused = true
      })
  }

  // emit 真正弹一条系统通知(权限/来源判定已在调用点过完)。失败不影响页内提示。
  const emit = (title: string, body?: string): number => {
    try {
      deps.ctor!(title, body ? { body } : undefined)
      return 1
    } catch {
      return 0
    }
  }

  const fire = (n: Notice): number => {
    if (!wantsSystemNotify(n.level) || !enabled()) return 0
    return emit(n.title, n.body || undefined)
  }

  const fireEvent = (title: string, body?: string): number => {
    if (!enabled()) return 0
    return emit(title, body)
  }

  return { maybeRequest, fire, fireEvent, enabled }
}
