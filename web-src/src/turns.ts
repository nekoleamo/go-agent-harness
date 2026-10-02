// 「回合被用户自己按停止结束」的判定(纯函数,可测)。
//
// 为什么需要它:用户按「停止」后,后端会如实地把三件事当作**错误/失败**回传 ——
// 等待中的审批弹层被取消(context canceled)、正在跑的工具被取消、
// `/api/input` 的回合返回 context.Canceled。它们对应用户自己的一个动作,
// 逐条弹红字既吵又误导(看着像崩了),而真正的回合取消已经在会话流里有一行
// 「⟳ 回合已取消」。故按错误文本在这里**收口**:认得出就静默,认不出照常报错
// (宁可多显示一条,不可把真故障吞掉)。
//
// 判定只认后端 Go error 的标准文本:context.Canceled 的 Error() 是
// "context canceled",可能被包装成 "…: context canceled"。

// CANCELED_TOKENS 取消文本的特征词(全小写)。
// 只收这三个:canceled / 已中止 / 已停止 —— 全是取消的近义说法,
// 刻意不收 "cancel"(凭据/订阅语境里也含它,会误吞真错误)。
const CANCELED_TOKENS = ['context canceled', '回合已中止', '回合已取消', '已停止']

/** 该错误文本是否表示「用户按了停止」,应当静默处理而不是报错。 */
export function isUserCanceled(text: string): boolean {
  const t = (text || '').toLowerCase()
  if (!t) return false
  return CANCELED_TOKENS.some((k) => t.includes(k))
}