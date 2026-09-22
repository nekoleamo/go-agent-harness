// 渲染器共享包自测:结构/分类/转义/增量拼合。
// (插件侧 render_html_test.go 走的是 /export 命令链,不能给本包计覆盖率 —— 覆盖率按
// 「被测包」计,依赖包不算,所以这里必须有自己的用例。)
package sessionhtml

import (
	"errors"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestRenderStructureAndEscape(t *testing.T) {
	evs := []sdk.SessionEvent{
		{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "问 <x> 题"}},
		{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Thinking: "先想"}},
		{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Delta: "正文一"}},
		{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Delta: "正文二"}},
		{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "重复最终消息"}},
		{Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{Name: "bash", Arguments: "echo hi"}},
		{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{Name: "bash", Content: "hi\n"}},
		{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{Name: "read", Error: "缺失"}},
		{Kind: sdk.EventAgentError, Payload: errors.New("炸了")},
		{Kind: sdk.EventTurnEnd, Payload: "done"},
	}
	out := Render(evs)
	if !strings.HasPrefix(out, "<!DOCTYPE html>") || !strings.HasSuffix(out, "</html>\n") {
		t.Fatalf("文档结构不完整: %q", out[:60])
	}
	if !strings.Contains(out, "问 &lt;x&gt; 题") {
		t.Fatalf("user 内容应转义: %q", out)
	}
	if n := strings.Count(out, `class="msg think"`); n != 1 {
		t.Fatalf("think 块应 1,得 %d", n)
	}
	if n := strings.Count(out, `class="msg assistant"`); n != 1 {
		t.Fatalf("assistant 增量应拼合成 1 块,得 %d", n)
	}
	if !strings.Contains(out, "正文一正文二") {
		t.Fatalf("assistant 增量应拼接: %q", out)
	}
	if strings.Contains(out, "重复最终消息") {
		t.Fatalf("chunk 段落之后的 AssistantMessage 应跳过(防重复): %q", out)
	}
	if !strings.Contains(out, "tool ok") || !strings.Contains(out, "tool err") || !strings.Contains(out, "缺失") {
		t.Fatalf("工具结果 ok/err 分类缺失: %q", out)
	}
	if !strings.Contains(out, `class="msg err"`) || !strings.Contains(out, "炸了") {
		t.Fatalf("agent error 块缺失: %q", out)
	}
	if !strings.Contains(out, "<hr>") {
		t.Fatalf("轮次分隔缺失: %q", out)
	}
}

func TestRenderEdges(t *testing.T) {
	// 空流:完整文档(不是空串)
	if out := Render(nil); !strings.HasPrefix(out, "<!DOCTYPE html>") {
		t.Fatalf("空流应完整文档: %q", out)
	}
	// 载荷类型不匹配(未还原的 map):跳过而不 panic,chunk 段保持闭合
	out := Render([]sdk.SessionEvent{
		{Kind: sdk.EventUserMessage, Payload: map[string]any{"Content": "raw"}},
		{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Delta: "一"}},
		{Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{Name: "x"}},
	})
	if strings.Contains(out, "raw") {
		t.Fatalf("未还原载荷应跳过: %q", out)
	}
	if !strings.Contains(out, "一</div>") {
		t.Fatalf("工具块前应闭合 assistant 增量段: %q", out)
	}
	// chunk 段未闭合(流中途结束):收尾仍闭合 div
	out = Render([]sdk.SessionEvent{{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Delta: "半"}}})
	if !strings.Contains(out, "半</div>") {
		t.Fatalf("流尾应闭合增量段: %q", out)
	}
}
