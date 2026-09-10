// 首启引导判定(E-E 抽取):把「是否展示引导」从 App.vue 的副作用代码里提成纯函数,
// 便于零依赖单测(node --test)。行为与 G-E4-R 交付一致。
export const GUIDE_ID_DESKTOP_IM = 'desktop-im'

export interface GuideInput {
  /** URL ?shell= 的值(桌面壳 = 'desktop';浏览器直连 = null) */
  shell: string | null
  /** /api/guides 返回的已关闭引导(不可得 = null,按「未关闭」处理) */
  dismissed: string[] | null
  /** IM 渠道是否已装配(/api/im/connect/spec 非 503) */
  specAvailable: boolean
  /** 当前连接相位(不可得 = null;done = 已连接) */
  phase: string | null
}

// shouldShowGuide 仅当:桌面壳 + 有 IM 渠道 + 未连接 + 未被「不再提示」。
// 任一未知条件(相位不可得、偏好不可得)按「提示一次」处理(引导本身可关闭,宁提示不漏)。
export function shouldShowGuide(i: GuideInput): boolean {
  if (i.shell !== 'desktop') return false
  if (!i.specAvailable) return false
  if (i.dismissed?.includes(GUIDE_ID_DESKTOP_IM)) return false
  if (i.phase === 'done') return false
  return true
}
