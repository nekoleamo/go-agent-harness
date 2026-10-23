// 累计摘要跨轮累计的回归用例(2026-10-21 批次 E 修的真缺陷)。
//
// 原缺陷:summaryAt 只在「水位以内」找摘要事件,而摘要事件是**追加在日志末尾**的
// (下标 > 水位)⇒ 永远找不到 ⇒ 每次折叠的 oldSummary 都是空 ⇒ 新摘要**不累计**:
// 第二次折叠把第一次折叠的内容整段顶掉,模型视野里只剩最近折进去的那一小块,
// 更早的历史**静默消失**(用户看不出来,只是模型"忘了"前面聊过什么)。
package tokencompress

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// appendMarkedTurn 一轮会话(带可识别标记),标记放在**开头**以便被摘要头部保留。
func appendMarkedTurn(l sdk.SessionLog, round int) {
	mark := fmt.Sprintf("M%02d", round)
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{
		Content: "用户" + mark + strings.Repeat("问", 40)}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{
		Content: "助手" + mark + strings.Repeat("答", 40)}})
	_ = l.Append(sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{
		CallID: "c" + itoa(round), Name: "shell", Content: "输出" + mark + strings.Repeat("o", 30)}})
}

// TestRollingSummaryAccumulatesAcrossTurns 多轮折叠后,投影里的累计摘要应同时含
// **最早**折进去的那一轮与**最近**折进去的那一轮。
//
// 走真实装配路径(host-session-log + token-compress),不是只测纯函数 ——
// 纯函数对了但调用方用错,一样是坏的。
//
// **反向验证**:把 summaryAt 改回"只扫 watermark 以内",本用例必须红。
func TestRollingSummaryAccumulatesAcrossTurns(t *testing.T) {
	l := buildEnv(t, 400)
	for i := 1; i <= 12; i++ {
		appendMarkedTurn(l, i)
		l.DeriveMessages() // 触发压缩检查(agent-loop 每步调用)
	}
	msgs := l.DeriveMessages()
	var summary string
	for _, m := range msgs {
		if m.Role == sdk.RoleSystem && strings.Contains(m.Content, "滚动摘要") {
			summary = m.Content
			break
		}
	}
	if summary == "" {
		t.Fatalf("应有滚动摘要(system 消息): %+v", msgs)
	}
	if !strings.Contains(summary, "M01") {
		t.Fatalf("累计摘要应含**最早**折进去的那轮(M01);丢了它 ⇒ 摘要没累计(旧缺陷):\n%s", summary)
	}
	if !strings.Contains(summary, "M10") && !strings.Contains(summary, "M11") && !strings.Contains(summary, "M12") {
		t.Fatalf("累计摘要应含**最近**折进去的那轮:\n%s", summary)
	}
}

// TestSummaryAtTakesWholeStream summaryAt 取**全流**最后一条非空摘要。
func TestSummaryAtTakesWholeStream(t *testing.T) {
	evs := []sdk.SessionEvent{
		{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "问"}},
		{Kind: sdk.EventSummary, Payload: sdk.RollingSummary{Text: "第一段", Until: 0}},
		{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "答"}},
		{Kind: sdk.EventSummary, Payload: sdk.RollingSummary{Text: "第二段", Until: 2}},
	}
	if got := summaryAt(evs); got != "第二段" {
		t.Fatalf("应取最后一条摘要: %q", got)
	}
	// 旧日志形态(纯字符串载荷)也要认
	old := []sdk.SessionEvent{{Kind: sdk.EventSummary, Payload: "旧格式"}}
	if got := summaryAt(old); got != "旧格式" {
		t.Fatalf("旧格式载荷应可读: %q", got)
	}
	// 没有摘要 ⇒ 空
	if got := summaryAt(evs[:1]); got != "" {
		t.Fatalf("无摘要应为空: %q", got)
	}
}

// TestFoldSummaryCarriesWatermark 折叠产出的摘要事件必须带**覆盖水位**(Until)。
//
// 宿主靠它精确恢复折叠水位:没有它,重载后水位只能取"摘要事件自己的下标",
// 而那比真实折叠水位靠后 ⇒ 折叠点之后那段没被折叠的事件被连带跳过(丢内容)。
func TestFoldSummaryCarriesWatermark(t *testing.T) {
	var evs []sdk.SessionEvent
	for i := 1; i <= 6; i++ {
		evs = append(evs,
			sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "用户问题 " + strings.Repeat("行", 40)}},
			sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "助手回答 " + strings.Repeat("果", 40)}},
			sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "c" + itoa(i), Name: "shell", Content: "输出 " + strings.Repeat("o", 30)}})
	}
	var got []sdk.RollingSummary
	e := &Engine{}
	w := e.Fold(evs, -1, 300, func(s sdk.RollingSummary) { got = append(got, s) })
	if len(got) == 0 {
		t.Fatal("应有摘要")
	}
	last := got[len(got)-1]
	if last.Until != w {
		t.Fatalf("摘要的覆盖水位应等于折叠返回的水位: %d vs %d", last.Until, w)
	}
	if last.Source != sdk.SummarySourceExtract {
		t.Fatalf("摘要来源应标为抽取式: %q", last.Source)
	}
	if last.Text == "" {
		t.Fatal("摘要文本不应为空")
	}
}

// TestFoldSkipsMarkerOnlyBlock 只含标记(turn/start、step/start…)的块**不得落空摘要**。
//
// 为什么(2026-10-23,端到端用例逮到):水位后的第一个事件常常是 turn/start,
// 它 `approxEv` 为 0 ⇒ `chars >= need` 一次都到不了 ⇒ 整块只有标记 ⇒ 落一条
// **空摘要**并推进水位。后果:投影多一条空摘要(噪声)、模型摘要通道拿到
// `Merged:""` 的空范围(乐观锁语义错)、一次折叠迭代白跑。
//
// **反向验证**:把 Fold 里的 `if seg == ""` 分支去掉 ⇒ 本用例红(会看到一条空摘要)。
func TestFoldSkipsMarkerOnlyBlock(t *testing.T) {
	e := &Engine{}
	var got []sdk.RollingSummary
	evs := []sdk.SessionEvent{
		{Kind: sdk.EventTurnStart}, // 标记(零字符)
		{Kind: sdk.EventStepStart}, // 标记(零字符)
		{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "问题甲"}}, // 最后一个用户轮(保留)
	}
	// 预算极小 ⇒ 一定想折;但可折的只有标记块(不得越过最后一个用户轮)
	// 预算 1 字符:内容(3 字符)必然超预算,逼出折叠;可折的却只有那两个标记
	w := e.Fold(evs, -1, 1, func(s sdk.RollingSummary) { got = append(got, s) })
	if len(got) != 0 {
		t.Fatalf("只含标记的块不该落摘要(空摘要会污染投影与乐观锁): %+v", got)
	}
	if w != 1 {
		t.Fatalf("标记块应被跳过(水位推进到最后一个标记),实际水位 %d", w)
	}
}
