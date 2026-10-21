// intervention.ts:「现在轮到人了吗」的**单一信号**(D3 · 2026-10-21)。
//
// 背景:待审批(ConfirmDialog)、待答提问(QuestionDialog/角标)、出错(meta 行)、
// 「你不在时它跑完了」(回底胶囊)此前分散在四处。用户切到别的标签页再回来,
// 提示可能已经消失(toast 会自动消失),于是只剩"它好像没理我"的错觉。
//
// 这里把四态收成**一条常驻的顶部条**(与 TUI 端同款语义,见 tui/intervention.go):
//
//	待审批 > 待回答 > 出错 > 跑完了
//
// 为什么是这个优先级:前三态都要人动手,且**待审批/待答会把回合卡住**(不答就收不了尾);
// 「跑完了」只是"有东西你没看",不卡人 ⇒ 排最后。
//
// 零新后端契约:四态全部从**前端已有的状态**派生(确认请求/提问/meta 错误行/滚动位置)。

export type InterventionKind = 'approve' | 'question' | 'error' | 'done' | ''

export interface Intervention {
  kind: InterventionKind
  text: string
  // action 点击条子时的动作(由宿主接到具体行为:打开弹层/滚到出错处/回到最新)。
  action: 'approve' | 'answer' | 'error' | 'latest' | ''
}

export interface InterventionInput {
  // pendingConfirm 有待裁决的确认请求(agent 的审批)。
  pendingConfirm: boolean
  // pendingQuestion 有待答提问(不论弹层是否收起)。
  pendingQuestion: boolean
  // lastError 最近一次错误文本(空 = 无)。**只取最近一条 meta**:新消息来了就不再是"当前错误"。
  lastError: string
  // running 回合运行中(运行中不报"跑完了")。
  running: boolean
  // unreadBelow 视口下方还有没看过的内容(上滚读历史时新内容到达)。
  unreadBelow: boolean
}

// interventionOf 派生介入态(纯函数;无介入 ⇒ 空态)。
export function interventionOf(inp: InterventionInput): Intervention {
  if (inp.pendingConfirm) {
    return { kind: 'approve', text: '⏸ 待审批 · 回合在等你确认这次操作', action: 'approve' }
  }
  if (inp.pendingQuestion) {
    return { kind: 'question', text: '❓ 待回答 · 有问题等你答', action: 'answer' }
  }
  if (inp.lastError) {
    return { kind: 'error', text: '⛔ 出错 · ' + clip(inp.lastError, 60), action: 'error' }
  }
  if (!inp.running && inp.unreadBelow) {
    return { kind: 'done', text: '✔ 跑完了 · 有新内容在下面', action: 'latest' }
  }
  return { kind: '', text: '', action: '' }
}

// clip 截断(按字符数;超长加省略号)。
function clip(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, Math.max(0, n - 1)) + '…'
}
