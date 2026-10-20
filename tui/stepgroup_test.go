package tui

// 工具分组(C3)的用例。
//
// 要钉的是:
//  1. 头行报结论(工具数/耗时/失败数),且**只给成组的步**(≥2 个工具)——
//     单工具的步不添噪声;
//  2. 工具行**默认可见**(验收用例条目 41 要求屏幕上一直看得见调用行:
//     「⚙ shell(echo 甲四十一)」里的命令文本)⇒ 折叠是手动动作;
//  3. 手动折叠只藏**工具行**:助手正文与工具执行期间的 meta 行永远可见;
//  4. 头行在步的**最前** ⇒ 既有行的下标不变(展开态/实时输出不会张冠李戴)。

import (
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func stepStartEv(ts time.Time) *sdk.SessionEvent {
	return &sdk.SessionEvent{Kind: sdk.EventStepStart, TS: ts}
}

func stepEndEv(ts time.Time) *sdk.SessionEvent {
	return &sdk.SessionEvent{Kind: sdk.EventStepEnd, TS: ts}
}

func assistantEv(text string) *sdk.SessionEvent {
	return &sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: text}}
}

// twoToolStep 造一个"一步两个工具"的会话流(成组 ⇒ 有头行)。
func twoToolStep(t *testing.T) *State {
	t.Helper()
	s := &State{}
	t0 := time.Now()
	s.ApplySessionEvent(stepStartEv(t0))
	s.ApplySessionEvent(toolCallEv("c1", "shell"))
	s.ApplySessionEvent(toolResultEv("c1", "shell"))
	s.ApplySessionEvent(toolCallEv("c2", "shell"))
	s.ApplySessionEvent(toolResultEv("c2", "shell"))
	s.ApplySessionEvent(stepEndEv(t0.Add(1500 * time.Millisecond)))
	return s
}

// TestStepGroupHeaderReportsOutcome 步结束后头行报结论(工具数/耗时/失败数)。
func TestStepGroupHeaderReportsOutcome(t *testing.T) {
	s := twoToolStep(t)
	rows := flattenViewLines(s, 100)
	if len(rows) == 0 {
		t.Fatal("应有头行")
	}
	head := rows[0].text
	if !strings.Contains(head, "第 1 步") || !strings.Contains(head, "2 个工具") {
		t.Fatalf("头行应报步序与工具数,got %q", head)
	}
	if !strings.Contains(head, "1.5s") {
		t.Fatalf("头行应报耗时,got %q", head)
	}
}

// TestStepGroupDefaultVisible 工具行**默认可见**(验收用例条目 41 的要求)。
func TestStepGroupDefaultVisible(t *testing.T) {
	s := twoToolStep(t)
	out := renderedText(s)
	if !strings.Contains(out, "shell") {
		t.Fatalf("工具行默认应可见,got %q", out)
	}
	if !strings.Contains(out, "第 1 步") {
		t.Fatalf("成组的步应有头行,got %q", out)
	}
}

// TestStepGroupManualToggle 点头行能手动折叠/展开。
func TestStepGroupManualToggle(t *testing.T) {
	s := twoToolStep(t)
	head := 0 // 头行是第一行
	if !s.ToggleStep(head) {
		t.Fatal("头行应可切换折叠")
	}
	out := renderedText(s)
	if strings.Contains(out, "shell") {
		t.Fatalf("折叠后工具行应收起,got %q", out)
	}
	if !strings.Contains(out, "第 1 步") {
		t.Fatalf("折叠后头行仍在(报结论),got %q", out)
	}
	if !s.ToggleStep(head) {
		t.Fatal("应能再展开")
	}
	if out2 := renderedText(s); !strings.Contains(out2, "shell") {
		t.Fatalf("展开后应看到工具行,got %q", out2)
	}
}

// TestStepGroupKeepsAssistantText 折叠只藏工具行:助手正文永远可见。
func TestStepGroupKeepsAssistantText(t *testing.T) {
	s := &State{}
	t0 := time.Now()
	s.ApplySessionEvent(stepStartEv(t0))
	s.ApplySessionEvent(assistantEv("这是模型的答复"))
	s.ApplySessionEvent(toolCallEv("c1", "shell"))
	s.ApplySessionEvent(toolResultEv("c1", "shell"))
	s.ApplySessionEvent(toolCallEv("c2", "shell"))
	s.ApplySessionEvent(toolResultEv("c2", "shell"))
	s.ApplySessionEvent(stepEndEv(t0.Add(time.Second)))
	s.ToggleStep(0)

	out := renderedText(s)
	if !strings.Contains(out, "这是模型的答复") {
		t.Fatalf("折叠不应藏起助手正文,got %q", out)
	}
	if strings.Contains(out, "shell") {
		t.Fatalf("工具行应收起,got %q", out)
	}
}

// TestStepWithoutToolsHidesHeader 没有工具的步:头行不显示(不添噪声)。
func TestStepWithoutToolsHidesHeader(t *testing.T) {
	s := &State{}
	t0 := time.Now()
	s.ApplySessionEvent(stepStartEv(t0))
	s.ApplySessionEvent(assistantEv("答复"))
	s.ApplySessionEvent(stepEndEv(t0.Add(time.Second)))

	out := renderedText(s)
	if strings.Contains(out, "第 1 步") {
		t.Fatalf("无工具的步不该显示头行,got %q", out)
	}
	if !strings.Contains(out, "答复") {
		t.Fatalf("正文应照常显示,got %q", out)
	}
}

// TestSingleToolStepHidesHeader 单工具的步:头行不显示(没有"成组"可言)。
func TestSingleToolStepHidesHeader(t *testing.T) {
	s := &State{}
	t0 := time.Now()
	s.ApplySessionEvent(stepStartEv(t0))
	s.ApplySessionEvent(toolCallEv("c1", "shell"))
	s.ApplySessionEvent(toolResultEv("c1", "shell"))
	s.ApplySessionEvent(stepEndEv(t0.Add(time.Second)))

	out := renderedText(s)
	if strings.Contains(out, "第 1 步") {
		t.Fatalf("单工具的步不该显示头行,got %q", out)
	}
	if !strings.Contains(out, "shell") {
		t.Fatalf("工具行应照常显示,got %q", out)
	}
}

// TestStepGroupReportsFailure 失败的工具在头行里报出来(折叠态也知道有没有出事)。
func TestStepGroupReportsFailure(t *testing.T) {
	s := &State{}
	t0 := time.Now()
	s.ApplySessionEvent(stepStartEv(t0))
	s.ApplySessionEvent(toolCallEv("c1", "shell"))
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c1", Name: "shell", Error: "boom"}})
	s.ApplySessionEvent(toolCallEv("c2", "shell"))
	s.ApplySessionEvent(toolResultEv("c2", "shell"))
	s.ApplySessionEvent(stepEndEv(t0.Add(time.Second)))

	out := renderedText(s)
	if !strings.Contains(out, "1 个失败") {
		t.Fatalf("头行应报失败数,got %q", out)
	}
}

// TestStepGroupKeepsLineIndexes 头行建在步的最前 ⇒ 既有行的下标不变。
//
// 为什么重要:会话流的展开态/实时输出都按**行下标**索引 ——
// 中途插一行会让那些下标错位(展开态张冠李戴、实时输出写到别的工具行上)。
func TestStepGroupKeepsLineIndexes(t *testing.T) {
	s := &State{}
	s.ApplySessionEvent(&sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "问题"}})
	before := len(s.Lines)
	t0 := time.Now()
	s.ApplySessionEvent(stepStartEv(t0))
	s.ApplySessionEvent(toolCallEv("c1", "shell"))
	s.ApplySessionEvent(toolResultEv("c1", "shell"))
	s.ApplySessionEvent(stepEndEv(t0.Add(time.Second)))

	// 头行插在用户消息之后 ⇒ 用户消息的下标仍是 0
	if s.Lines[0].Text != "问题" {
		t.Fatalf("头行不应插到已有行之前,got %q", s.Lines[0].Text)
	}
	if len(s.Lines) != before+3 {
		t.Fatalf("应只新增 3 行(头行+调用+结果),got %d", len(s.Lines)-before)
	}
	// 工具行紧跟在头行之后(下标 2/3),没被顶掉
	if !strings.Contains(s.Lines[1].Text, "第 1 步") {
		t.Fatalf("第 1 行应是头行,got %q", s.Lines[1].Text)
	}
	if !strings.Contains(s.Lines[2].Text, "shell") {
		t.Fatalf("工具调用行应在头行之后,got %q", s.Lines[2].Text)
	}
}

// TestStepGroupKeepsMetaLinesInBetween 工具执行期间的 **meta 行**不能被折叠带走。
//
// 复现过一次真缺陷(2026-10-21):分组原按"行号区间"收起,把工具执行期间的
// meta 行(「❓ 提问已结束(其它渠道作答或已取消)」)一起收了 ——
// 用户刚看到的关键提示在折叠瞬间消失,跨渠道验收用例直接红。
// 修法:分组只记**工具行**的行号,不按区间收。
func TestStepGroupKeepsMetaLinesInBetween(t *testing.T) {
	s := &State{}
	t0 := time.Now()
	s.ApplySessionEvent(stepStartEv(t0))
	s.ApplySessionEvent(toolCallEv("c1", "shell"))
	// 工具执行期间插一条 meta 行(多端并存提示走的就是这条路)
	s.Lines = append(s.Lines, Line{Kind: "meta", Text: "❓ 提问已结束(其它渠道作答或已取消);待答 0 条"})
	s.ApplySessionEvent(toolResultEv("c1", "shell"))
	s.ApplySessionEvent(toolCallEv("c2", "shell"))
	s.ApplySessionEvent(toolResultEv("c2", "shell"))
	s.ApplySessionEvent(stepEndEv(t0.Add(time.Second)))
	s.ToggleStep(0)

	out := renderedText(s)
	if !strings.Contains(out, "提问已结束") {
		t.Fatalf("折叠不应带走工具执行期间的 meta 行,got %q", out)
	}
	if strings.Contains(out, "shell") {
		t.Fatalf("工具行仍应收起,got %q", out)
	}
}

// TestSearchHitInCollapsedGroupExpands 搜索命中落在折叠中的工具组 ⇒ 先展开再跳。
//
// 为什么:折叠起来的行根本不在屏上,"跳到命中处"等于没跳(用户以为功能坏了)。
func TestSearchHitInCollapsedGroupExpands(t *testing.T) {
	s := twoToolStep(t)
	head := s.stepHeadOf(2) // 第一个工具调用行
	if head < 0 {
		t.Fatalf("工具行应属于某个分组")
	}
	s.ToggleStep(head) // 手动折叠
	if s.stepExpanded(head) {
		t.Fatal("应已折叠")
	}
	// 命中行在折叠里 ⇒ 定位前得展开(否则跳了也看不见)
	if s.stepHeadOf(2) != head {
		t.Fatal("应仍能定位分组")
	}
	s.ToggleStep(head)
	out := renderedText(s)
	if !strings.Contains(out, "shell") {
		t.Fatalf("展开后应能看到命中行,got %q", out)
	}
}
