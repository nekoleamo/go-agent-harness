// 会话流的行交织:把前端侧产生的 meta 行织回消息流的正确位置(纯函数,可测)。
//
// 为何要有这一步(2026-10-03 实机反馈):meta(命令回显/回合错误/审批未应答)原先被
// StreamView 统一渲染在**所有消息之后** —— 回合被停止时弹出的报错就永远钉在最底下,
// 用户继续对话后,新回答只能显示在那条报错的上方,读起来像“报错出现在回答之后”。
//
// 定位用 MetaLine.after(产生时的最后一条消息 seq),不用时间戳:不依赖前后端时钟同源。
// 插入点是「严格在 after 之后」:after=1 的 meta 落在 seq=1 那条**之后**,不是之前。
// 前提是 metas 已按产生顺序排列(App.pushMeta 只 push 不排序),故同 after 的连续一段
// 可以一次性插入;剩余的尾部 meta(会话流为空时产生的)落在末尾。
import type { MetaLine } from './registry'
import type { Msg } from './sse'

export type Row =
  | { t: 'msg'; m: Msg }
  | { t: 'meta'; mm: MetaLine }

/** 交织后的渲染序列。 */
export function rowsOf(frames: Msg[], metas: MetaLine[]): Row[] {
  if (!metas.length) return frames.map((m) => ({ t: 'msg', m }) as Row)
  const out: Row[] = []
  let i = 0
  for (const f of frames) {
    while (i < metas.length && metas[i].after < f.seq) out.push({ t: 'meta', mm: metas[i++] })
    out.push({ t: 'msg', m: f })
  }
  while (i < metas.length) out.push({ t: 'meta', mm: metas[i++] })
  return out
}