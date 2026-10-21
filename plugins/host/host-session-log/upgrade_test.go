// 摘要的**原子替换**(批次 D4):模型摘要是**事后**补写的,补写期间可能又折叠了一次。
// 那时若仍把旧范围的摘要写进去,投影会回退到**更旧**的摘要(丢掉刚折进去的那一段)。
// 乐观锁:只有"当前最新摘要 == 调用方看到的那条"时才替换。
package sessionlog

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestUpgradeSummaryReplacesLatest(t *testing.T) {
	l := newLog("")
	for i := 1; i <= 6; i++ {
		appendTurn(l, i)
	}
	// 造一条摘要事件(相当于折叠落盘)
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: sdk.RollingSummary{
		Text: "抽取式摘要", Until: 3, Source: sdk.SummarySourceExtract}}); err != nil {
		t.Fatal(err)
	}
	ok, err := l.UpgradeSummary("抽取式摘要", "模型摘要", sdk.SummarySourceLLM)
	if err != nil || !ok {
		t.Fatalf("应替换成功: ok=%v err=%v", ok, err)
	}
	text, until := latestSummary(l.events)
	if text != "模型摘要" {
		t.Fatalf("最新摘要应是模型摘要: %q", text)
	}
	if until != 3 {
		t.Fatalf("覆盖水位应继承(替换不改变折了哪些事件): %d", until)
	}
	// 来源应标为 llm
	var src string
	for _, ev := range l.events {
		if ev.Kind == sdk.EventSummary {
			if rs, ok := ev.Payload.(sdk.RollingSummary); ok {
				src = rs.Source
			}
		}
	}
	if src != sdk.SummarySourceLLM {
		t.Fatalf("来源应标为 llm: %q", src)
	}
}

// TestUpgradeSummaryRejectsStaleExpected 期间又折叠了(最新摘要已变)⇒ 拒绝替换。
//
// **反向验证**:把乐观锁判断去掉(直接替换)⇒ 本用例必须红。
func TestUpgradeSummaryRejectsStaleExpected(t *testing.T) {
	l := newLog("")
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: sdk.RollingSummary{
		Text: "旧摘要", Until: 1, Source: sdk.SummarySourceExtract}})
	// 期间又折叠了一次:最新摘要变成别的内容
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: sdk.RollingSummary{
		Text: "更新的摘要", Until: 5, Source: sdk.SummarySourceExtract}})
	ok, err := l.UpgradeSummary("旧摘要", "模型摘要", sdk.SummarySourceLLM)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("最新摘要已变(期间又折叠)⇒ 应拒绝替换")
	}
	text, _ := latestSummary(l.events)
	if text != "更新的摘要" {
		t.Fatalf("摘要不应被改写: %q", text)
	}
	if strings.Contains(text, "模型摘要") {
		t.Fatalf("旧范围的模型摘要不应写进来: %q", text)
	}
}

// TestUpgradeSummaryRejectsEmpty 空文本不写(否则投影会得到一条空摘要)。
func TestUpgradeSummaryRejectsEmpty(t *testing.T) {
	l := newLog("")
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: sdk.RollingSummary{
		Text: "摘要", Until: 1, Source: sdk.SummarySourceExtract}})
	ok, err := l.UpgradeSummary("摘要", "", sdk.SummarySourceLLM)
	if err != nil || ok {
		t.Fatalf("空文本应拒绝: ok=%v err=%v", ok, err)
	}
	text, _ := latestSummary(l.events)
	if text != "摘要" {
		t.Fatalf("摘要不应被改写: %q", text)
	}
}

// TestUpgradeSummaryOldPayloadReplaces 旧日志形态(纯字符串载荷)也能被替换,
// 覆盖水位退回"摘要事件下标"(旧载荷没有 Until)。
func TestUpgradeSummaryOldPayloadReplaces(t *testing.T) {
	l := newLog("")
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: "旧格式摘要"})
	ok, err := l.UpgradeSummary("旧格式摘要", "模型摘要", sdk.SummarySourceLLM)
	if err != nil || !ok {
		t.Fatalf("旧格式载荷也应可替换: ok=%v err=%v", ok, err)
	}
	text, until := latestSummary(l.events)
	if text != "模型摘要" {
		t.Fatalf("最新摘要应是模型摘要: %q", text)
	}
	if until != 0 {
		t.Fatalf("旧载荷无水位时应退回摘要事件下标: %d", until)
	}
}

// TestUpgradeSummaryWithoutAnySummary 没有任何摘要 ⇒ 无可替换(返回 false,不报错)。
func TestUpgradeSummaryWithoutAnySummary(t *testing.T) {
	l := newLog("")
	ok, err := l.UpgradeSummary("x", "y", sdk.SummarySourceLLM)
	if err != nil || ok {
		t.Fatalf("没有摘要时应返回 false: ok=%v err=%v", ok, err)
	}
}

// TestLatestSummarySkipsEmptyText 摘要事件文本为空时跳过(不把空摘要当最新)。
func TestLatestSummarySkipsEmptyText(t *testing.T) {
	evs := []sdk.SessionEvent{
		{Kind: sdk.EventSummary, Payload: sdk.RollingSummary{Text: "有内容", Until: 0}},
		{Kind: sdk.EventSummary, Payload: sdk.RollingSummary{Text: "", Until: 1}},
	}
	text, until := latestSummary(evs)
	if text != "有内容" {
		t.Fatalf("空摘要不应覆盖: %q", text)
	}
	if until != 0 {
		t.Fatalf("水位应取有内容那条: %d", until)
	}
}
