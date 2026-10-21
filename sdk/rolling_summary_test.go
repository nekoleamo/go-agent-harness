// 摘要载荷的两种形态与读取口径(2026-10-21 批次 E)。
//
// 摘要载荷历史上有两种形态:旧日志是**纯字符串**,新日志是**结构体**
// (sdk.RollingSummary:文本 + 覆盖水位 + 来源)。读它的地方有 4 处,口径必须收在
// sdk 一处(sdk.SummaryText / sdk.SummaryUntil),否则各自判类型迟早漂移。
package sdk

import "testing"

func TestSummaryTextBothShapes(t *testing.T) {
	if got := SummaryText("旧格式"); got != "旧格式" {
		t.Fatalf("旧字符串载荷: %q", got)
	}
	if got := SummaryText(RollingSummary{Text: "新格式", Until: 3}); got != "新格式" {
		t.Fatalf("新结构体载荷: %q", got)
	}
	if got := SummaryText(nil); got != "" {
		t.Fatalf("未知载荷应为空: %q", got)
	}
	if got := SummaryText(42); got != "" {
		t.Fatalf("未知载荷应为空: %q", got)
	}
}

func TestSummaryUntilBothShapes(t *testing.T) {
	if got := SummaryUntil(RollingSummary{Text: "x", Until: 7}); got != 7 {
		t.Fatalf("新结构体载荷的水位: %d", got)
	}
	if got := SummaryUntil("旧格式"); got != -1 {
		t.Fatalf("旧字符串载荷无水位,应 -1: %d", got)
	}
	if got := SummaryUntil(nil); got != -1 {
		t.Fatalf("未知载荷应 -1: %d", got)
	}
}
