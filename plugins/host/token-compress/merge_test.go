package tokencompress

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestMergeSummaryKeepsNewSegment 摘要合并:**新折进来的那段必须在**。
//
// 这是 2026-10-20 优化批次 A2 修的真缺陷:原实现 `clip(merged, budget/2)`
// 取**头部** ⇒ 摘要一旦长到预算一半,此后每次折叠新增的段直接消失,
// 摘要永久停在最老的内容上(静默偏离)。
//
// **反向验证**:把 mergeSummary 改回 `clip(merged, budget/2)`,本用例必须红。
func TestMergeSummaryKeepsNewSegment(t *testing.T) {
	budget := 200                   // limit = 100
	old := strings.Repeat("旧", 400) // 远超 limit
	seg := "最新一段:改了 foo.go"

	got := mergeSummary(old, seg, budget)
	if !strings.Contains(got, "最新一段") {
		t.Fatalf("新折进来的段被丢了: %q", got)
	}
	// 长度仍在预算一半内
	if n := len([]rune(got)); n > budget/2 {
		t.Fatalf("合并后应限长 %d,实际 %d", budget/2, n)
	}
	// 省略处有标记(免得模型误以为摘要是完整的)
	if !strings.Contains(got, "已省略") {
		t.Fatalf("中间省略处应有标记: %q", got)
	}
}

// TestMergeSummaryPinsHead 累计摘要的**头部**要钉住(最初的任务陈述不该被滑掉)。
func TestMergeSummaryPinsHead(t *testing.T) {
	budget := 200
	old := "最初的任务:实现 X" + strings.Repeat("旧", 400)
	got := mergeSummary(old, "新段", budget)
	if !strings.Contains(got, "最初的任务") {
		t.Fatalf("累计摘要头部应保留: %q", got)
	}
	if !strings.Contains(got, "新段") {
		t.Fatalf("新段也应保留: %q", got)
	}
}

// TestMergeSummaryShortEnough 不超预算时原样拼接(不裁、不加标记)。
func TestMergeSummaryShortEnough(t *testing.T) {
	got := mergeSummary("abc", "def", 100)
	if got != "abcdef" {
		t.Fatalf("未超预算应原样拼接: %q", got)
	}
}

// TestMergeSummaryZeroBudget 预算为 0(压缩关闭)时不裁剪。
func TestMergeSummaryZeroBudget(t *testing.T) {
	got := mergeSummary("abc", "def", 0)
	if got != "abcdef" {
		t.Fatalf("budget=0 不应裁剪: %q", got)
	}
}

// TestRollingSummaryKeepsRecent 集成:多轮折叠后,摘要里能看到**最近**折进去的内容。
//
// 走真实折叠路径(Engine.Fold),而不是只测纯函数 —— 纯函数对了但调用方用错,
// 一样是坏的(这正是本仓"修完必须反向验证"的纪律)。
func TestRollingSummaryKeepsRecent(t *testing.T) {
	// 12 轮,每轮内容带可识别标记(标记放在开头,保证不被 summarizeEvents 的截断切掉)。
	var evs []sdk.SessionEvent
	for i := 1; i <= 12; i++ {
		mark := "R" + itoa(i)
		evs = append(evs,
			sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "用户" + mark + strings.Repeat("问", 40)}},
			sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "助手" + mark + strings.Repeat("答", 40)}},
			sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c" + itoa(i), Name: "shell", Content: "输出" + mark + strings.Repeat("o", 30)}})
	}
	var last string
	e := &Engine{}
	budget := 400 // limit=200 ⇒ 会折叠多轮
	for iter := 0; iter < 10; iter++ {
		prev := last
		e.Fold(evs, -1, budget, func(s sdk.RollingSummary) { last = s.Text })
		if last == prev {
			break
		}
	}
	if last == "" {
		t.Fatal("应有摘要")
	}
	// 摘要里应能看到**最后折进去**的那轮(而不是永远停在最早那轮)。
	if !strings.Contains(last, "R12") && !strings.Contains(last, "R11") {
		t.Fatalf("摘要应含最近轮次的痕迹(R11/R12): %q", last)
	}
	// 长度受限
	if n := len([]rune(last)); n > budget/2 {
		t.Fatalf("摘要应限长 %d,实际 %d", budget/2, n)
	}
}
