// intervention.go:「现在轮到人了吗」的**单一信号**(D3 · 2026-10-21)。
//
// 背景:待审批(确认弹层)、待答提问(问题栈)、出错(错误横幅)此前分散在三处,
// 用户跑长任务时切走一会儿回来,不知道"现在轮到我了没"—— 三处各看一眼才能拼出结论。
//
// 这里把三态收成一处(状态栏首项;Web 端同款见 web-src/src/intervention.ts):
//
//	待审批 > 待回答 > 出错
//
// 为什么是这个优先级:三者都要人,但**待审批/待回答会把回合卡住**(不答就收不了尾),
// 出错只是"上一轮没成"(人看完再决定要不要重发)⇒ 卡人的排前面。
//
// 「跑完了」这一态**只在 Web 端有**:它的语义是"你不在的时候它跑完了"(浏览器切标签页
// 的情形);TUI 里用户就在终端前,回合收尾由状态栏的「空闲」表达(不重复占位)。
package tui

import "strconv"

// 介入态的 kind(与 Web 端同名同义;对外只有展示,没有契约依赖)。
const (
	interveneNone     = ""
	interveneApprove  = "approve"  // 待审批
	interveneQuestion = "question" // 待回答
	interveneError    = "error"    // 出错
)

// interventionItem 介入信号(状态栏首项;无介入时返回空串 = 不占位)。
//
// 措辞与 Web 端保持一致(两端各写一份实现,但文案与优先级同款;
// 跨端一致性靠用例钉住,不靠"两边恰好一样")。
func interventionItem(s *State) string {
	kind, text := interventionOf(s)
	switch kind {
	case interveneApprove:
		// 确认弹层此前把提示写进了会话流,但**按键提示**没有(用户不知道要按 y/n)——
		// 这里补上(条 31 的 pty 实测:"既看不到在确认什么、也不知道要按 y/n")。
		//
		// 措辞要**短**:状态栏是 80 列的一行,左区还要留给 state/queue/dock 这些实时项;
		// 长了会把右区(工作区/沙箱/审批)挤出屏(几何用例就靠"工作区:"还在屏内守着)。
		return styleBusy.Render("⏸ 待审批(y/n)")
	case interveneQuestion:
		return styleBusy.Render(text)
	case interveneError:
		return styleError.Render(text)
	}
	return ""
}

// interventionOf 派生介入态(纯函数;可单测)。
//
// 返回 (kind, 文案);无介入 ⇒ ("", "")。
func interventionOf(s *State) (string, string) {
	if s.PendingConfirm != "" {
		return interveneApprove, "⏸ 待审批"
	}
	if n := len(s.Questions); n > 0 {
		hint := "❓ 待答"
		if n > 1 {
			hint = "❓ 待答 " + strconv.Itoa(n)
		}
		if s.Answering {
			hint += "(Esc 退出作答)"
		} else {
			hint += "(/answer 作答)"
		}
		return interveneQuestion, hint
	}
	if s.Error != "" {
		// 只放"出错"两字:错误全文已经在上方的**错误横幅**里(那才是给读全文的地方),
		// 状态栏重复一遍只会把这条窄栏撑爆。
		return interveneError, "⛔ 出错"
	}
	return interveneNone, ""
}
