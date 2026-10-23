// 工具结果分流后的**投影不变量**(2026-10-23 第一百六十五批)。
//
// 钉的是这件事:分流把一条工具结果拆成两个面(Content = 模型视图,Data = 完整原文)。
// 一旦 Data 漏进模型投影,分流就白做了 —— 上下文照旧被撑爆,而且**日志里看着一切正常**
// (Data 本来就在日志里,是合法的)。这条不变量只有投影这一层能守,界面/导出都测不出来。
package sessionlog

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestDeriveMessagesExcludesToolData(t *testing.T) {
	l := newTestLog(t, "")
	full := strings.Repeat("原始输出行\n", 1000)
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "跑一下"}}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{ID: "c1", Name: "shell", Arguments: `{"command":"cat big.log"}`}}); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(sdk.SessionEvent{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{
		CallID: "c1", Name: "shell", Content: "头…[省略 9000 字符]…尾", Data: full, Summary: "输出 1000 行",
	}}); err != nil {
		t.Fatal(err)
	}

	msgs := l.DeriveMessages()
	var tool *sdk.LLMMessage
	for i := range msgs {
		if msgs[i].Role == sdk.RoleTool {
			tool = &msgs[i]
		}
	}
	if tool == nil {
		t.Fatal("投影里应有工具消息")
	}
	if tool.Content != "头…[省略 9000 字符]…尾" {
		// 只打长度与前 24 字:泄露原文时 tens KB 的 got 会把测试输出淹没。
		t.Fatalf("模型必须只看到 Content(瘦身版), got %d 字符: %q…", len([]rune(tool.Content)), firstRunes(tool.Content, 24))
	}
	if strings.Contains(tool.Content, "原始输出行") {
		t.Fatalf("Data 漏进了模型投影(%d 字符)—— 分流等于没做", len([]rune(tool.Content)))
	}
	// 反过来:原文必须仍在**日志**里(界面靠它展开)。漏了这条,分流就变成"直接丢内容"。
	if got := l.Replay(); len(got) != 3 {
		t.Fatalf("日志应保留 3 条事件, got %d", len(got))
	} else if r, ok := got[2].Payload.(sdk.ToolResultEvent); !ok || r.Data != full {
		t.Fatal("原文必须随日志保留(界面/导出靠它)")
	}
}

// firstRunes 取前 n 个 rune(失败信息用:原文可能几十 KB)。
func firstRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
