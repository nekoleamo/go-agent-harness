// 重载后折叠水位的精确恢复(2026-10-21 批次 E 修的真缺陷)。
//
// 原缺陷:重载时水位取"最后一条摘要事件的**下标**",而摘要事件是追加在日志末尾的
// ⇒ 它的下标比真实折叠水位**靠后** ⇒ 两者之间那段**没被折叠**的事件被水位跳过。
// 实测:8 条消息折到第 5 条后重载,投影只剩一条摘要 —— 最后 3 条(用户最新提问在内)
// 凭空消失 ⇒ 重启后模型看不到用户最后问了什么。
package sessionlog

import (
	"path/filepath"
	"strings"
	"testing"

	tokencompress "github.com/nekoleamo/go-agent-harness/plugins/host/token-compress"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestReloadKeepsUnfoldedTail 重载后投影应与重载前**一致**(不多不少)。
//
// **反向验证**:把 recoverWatermark 改回"返回最后一条摘要事件的下标",本用例必须红。
func TestReloadKeepsUnfoldedTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	l := newLog(dir)
	l.SetPath(path)
	l.RegisterCompressor(300, &tokencompress.Engine{})
	for i := 1; i <= 8; i++ {
		_ = l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{
			Content: "用户第" + strings.Repeat("问", 40) + string(rune('0'+i))}})
		_ = l.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{
			Content: "助手第" + strings.Repeat("答", 40) + string(rune('0'+i))}})
	}
	// 手动折叠(立即发生,不等投影超限)
	if _, folded, err := l.Compact("x"); err != nil || folded <= 0 {
		t.Fatalf("折叠应发生: folded=%d err=%v", folded, err)
	}
	if err := l.Flush(); err != nil {
		t.Fatal(err)
	}
	before := userTexts(l.DeriveMessages())

	// 重新加载(模拟重启/切换会话)
	l2 := newLog(dir)
	l2.SetPath(path)
	if err := l2.Load(path); err != nil {
		t.Fatal(err)
	}
	l2.RegisterCompressor(300, &tokencompress.Engine{})
	after := userTexts(l2.DeriveMessages())

	if len(before) == 0 {
		t.Fatalf("重载前应有用户消息: %+v", before)
	}
	if len(after) != len(before) {
		t.Fatalf("重载后用户消息数应与重载前一致: %d vs %d\nbefore=%v\nafter=%v", len(after), len(before), before, after)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("重载后第 %d 条用户消息应一致: %q vs %q", i, before[i], after[i])
		}
	}
	// 末尾那条(用户最新提问)必须在
	if !strings.HasSuffix(after[len(after)-1], "8") {
		t.Fatalf("重载后应保留用户最新提问: %q", after[len(after)-1])
	}
}

// userTexts 取投影里的用户消息文本。
func userTexts(msgs []sdk.LLMMessage) []string {
	var out []string
	for _, m := range msgs {
		if m.Role == sdk.RoleUser {
			out = append(out, m.Content)
		}
	}
	return out
}

// TestRecoverWatermarkPrefersUntil recoverWatermark 优先用摘要载荷里的 Until;
// 旧日志(纯字符串载荷)退回"摘要事件下标"。
func TestRecoverWatermarkPrefersUntil(t *testing.T) {
	evs := []sdk.SessionEvent{
		{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "问"}},
		{Kind: sdk.EventSummary, Payload: sdk.RollingSummary{Text: "摘要", Until: 0}},
		{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "答"}},
	}
	if got := recoverWatermark(evs); got != 0 {
		t.Fatalf("应取摘要载荷的 Until: %d", got)
	}
	// 旧日志:没有 Until ⇒ 退回摘要事件下标
	old := []sdk.SessionEvent{
		{Kind: sdk.EventSummary, Payload: "旧摘要"},
		{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "问"}},
	}
	if got := recoverWatermark(old); got != 0 {
		t.Fatalf("旧日志应退回摘要事件下标: %d", got)
	}
	if got := recoverWatermark(nil); got != -1 {
		t.Fatalf("无摘要应返回 -1: %d", got)
	}
}
