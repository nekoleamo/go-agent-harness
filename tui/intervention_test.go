// 「介入信号」的用例(D3 · 2026-10-21):待审批/待答/出错收成一处,且按"卡人的优先"排序。
package tui

import (
	"strings"
	"testing"
)

// TestInterventionKinds 三态各自的呈现(措辞与 Web 端同款)。
func TestInterventionKinds(t *testing.T) {
	strip := func(s *State) string { return stripColor(interventionItem(s)) }

	// 无介入 ⇒ 空串(不占位)
	if got := strip(&State{}); got != "" {
		t.Fatalf("无介入时应为空串: %q", got)
	}
	// 待审批:**带按键提示**(此前确认弹层只有会话流里的一行提示,用户不知道要按 y/n)
	appr := strip(&State{PendingConfirm: "要执行 rm -rf 吗"})
	if !strings.Contains(appr, "待审批") {
		t.Fatalf("应有待审批段: %q", appr)
	}
	if !strings.Contains(appr, "y/n") {
		t.Fatalf("待审批应带按键提示(y/n): %q", appr)
	}
	// 待答:1 条不带数字,2 条带数字,作答中换后缀(与原 questions 段同款措辞)
	one := strip(&State{Questions: []PendingQ{{ID: "q1"}}})
	if !strings.Contains(one, "❓ 待答") || strings.Contains(one, "待答 1") {
		t.Fatalf("1 条待答的呈现不对: %q", one)
	}
	two := strip(&State{Questions: []PendingQ{{ID: "q1"}, {ID: "q2"}}})
	if !strings.Contains(two, "❓ 待答 2") {
		t.Fatalf("2 条待答应带数字: %q", two)
	}
	ans := strip(&State{Questions: []PendingQ{{ID: "q1"}}, Answering: true})
	if !strings.Contains(ans, "Esc 退出作答") {
		t.Fatalf("作答中应提示 Esc 退出: %q", ans)
	}
	// 出错:只放"出错"(全文在错误横幅里,状态栏不重复)
	errOut := strip(&State{Error: "连接失败: dial tcp: timeout"})
	if !strings.Contains(errOut, "出错") {
		t.Fatalf("应有出错段: %q", errOut)
	}
	if strings.Contains(errOut, "timeout") {
		t.Fatalf("错误全文不该在状态栏重复(横幅里已有): %q", errOut)
	}
}

// TestInterventionPriority 优先级:待审批 > 待答 > 出错。
//
// 为什么:待审批/待答会把回合**卡住**(不答就收不了尾),出错只是"上一轮没成"。
//
// **反向验证**:把 interventionOf 的分支顺序改回(出错优先),本用例必须红。
func TestInterventionPriority(t *testing.T) {
	all := &State{
		PendingConfirm: "确认?",
		Questions:      []PendingQ{{ID: "q1"}},
		Error:          "炸了",
	}
	kind, _ := interventionOf(all)
	if kind != interveneApprove {
		t.Fatalf("同时有三种时应先报待审批: %q", kind)
	}
	kind, _ = interventionOf(&State{Questions: []PendingQ{{ID: "q1"}}, Error: "炸了"})
	if kind != interveneQuestion {
		t.Fatalf("待答应先于出错: %q", kind)
	}
	kind, _ = interventionOf(&State{Error: "炸了"})
	if kind != interveneError {
		t.Fatalf("只有出错时应报出错: %q", kind)
	}
}

// TestInterveneInDefaultStatusline 介入项进默认状态栏(置首),且待答**只出现一次**
// (原 questions 段并入 ⇒ 同一件事不在一条栏里出现两遍)。
func TestInterveneInDefaultStatusline(t *testing.T) {
	s := &State{Questions: []PendingQ{{ID: "q1"}, {ID: "q2"}}}
	out := stripColor(renderStatusLine(s, 200))
	if n := strings.Count(out, "待答"); n != 1 {
		t.Fatalf("待答应只出现一次(questions 已并入 intervene): %d 次: %q", n, out)
	}
	if !strings.Contains(out, "待答 2") {
		t.Fatalf("待答 2 应在状态栏可见: %q", out)
	}
}
