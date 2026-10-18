// 多会话并行视图的**纯逻辑**(第一百零三批):从组件里抽出来单测,理由同上 ——
// api.ts / transport.ts 都被 Node 以 .ts 直载,不能有运行时相对 import,于是视图逻辑
// 必须住在自己的模块里。
//
// 这里只放「能从数据算出什么」的纯函数;真正的 DOM 渲染归布局护栏(真浏览器)。

/** 会话是否在跑(后端 running_sessions 里按 id 找)。主会话也用自己的 id。 */
export function sessionRunning(running: string[] | undefined, id: string): boolean {
  return (running || []).includes(id || '')
}

/**
 * 本窗口会话的运行标记文案。
 *
 * **步数只对本窗口可见**:事件连接按会话过滤(`?session=`),别的会话在跑到第几步
 * 收不到帧。所以对别的会话只说「运行中」,不编造步数 —— 假步数比没有步数更糟
 * (用户会以为它卡住了)。
 */
export function runLabel(running: string[] | undefined, id: string, curId: string, steps: number): string {
  if (!sessionRunning(running, id)) return ''
  const isCur = (id || '') === (curId || '')
  if (isCur && steps > 0) return `运行中 · 第 ${steps} 步`
  return '运行中'
}

/** 别的会话(不含本会话)正在跑的数量;本会话在跑时不提示(底栏已经写着"运行中")。 */
export function othersRunning(running: string[] | undefined, curRunning: boolean): number {
  if (curRunning) return 0
  return (running || []).length
}

/**
 * 会话列表里「有对话」的判据(后端 SessionInfo.Empty 的消费方)。
 *
 * 为什么前端还要判一次:空会话在后端**默认已被过滤**,但当前打开的那个是例外
 * (侧栏要靠它高亮"我在这儿")。而「一条对话都没有的当前会话」被点击切换没有意义
 * (就是现在这个),所以交互上按空会话处理。
 *
 * 注意 Empty 是 omitempty:旧后端/无该字段时一律当"有对话"(宁可多列一条,
 * 也不要因为字段缺失把历史会话从列表里抹掉)。
 */
export function hasConversation(s: { Empty?: boolean } | null | undefined): boolean {
  return !s?.Empty
}
