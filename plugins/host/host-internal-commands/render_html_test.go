// B2 /export html 测试:事件流 → 自包含 HTML(user/think/assistant 拼合/tool 块/转义)。
package hostintcmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestRenderSessionHTML(t *testing.T) {
	payloads := []sdk.SessionEvent{
		{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "问 <x> 题"}},
		{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Thinking: "先想"}},
		{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Thinking: "后答"}},
		{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Delta: "正文一"}},
		{Kind: sdk.EventAssistantChunk, Payload: sdk.LLMStreamEvent{Delta: "正文二"}},
		{Kind: sdk.EventToolCall, Payload: sdk.ToolCallEvent{ID: "1", Name: "bash", Arguments: "echo hi"}},
		{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "1", Name: "bash", Content: "hi\n"}},
		{Kind: sdk.EventTurnEnd, Payload: "done"},
	}
	out := renderSessionHTML(payloads)
	// 完整文档结构
	if !strings.HasPrefix(out, "<!DOCTYPE html>") || !strings.HasSuffix(out, "</html>\n") {
		t.Fatalf("文档结构不完整: %q", out[:80])
	}
	// user 块(含转义:尖括号不破坏结构)
	if !strings.Contains(out, `class="msg user"`) || !strings.Contains(out, "问 &lt;x&gt; 题") {
		t.Fatalf("user 块应分类+转义: %q", out)
	}
	// thinking 块(灰斜体 class;两块独立)
	if n := strings.Count(out, `class="msg think"`); n != 2 {
		t.Fatalf("think 块应 2(逐增量独立): %d", n)
	}
	// assistant chunk 拼合为一个连续块
	if n := strings.Count(out, `class="msg assistant"`); n != 1 {
		t.Fatalf("assistant 增量应拼合 1 块: %d", n)
	}
	if !strings.Contains(out, "正文一正文二") {
		t.Fatalf("assistant 增量应拼接: %q", out)
	}
	// tool 调用与结果块
	if !strings.Contains(out, `class="msg tool"`) || !strings.Contains(out, "bash") || !strings.Contains(out, "echo hi") {
		t.Fatalf("tool 调用块缺失: %q", out)
	}
	if !strings.Contains(out, "tool ok") || !strings.Contains(out, "hi") {
		t.Fatalf("tool 结果块缺失(ok 类): %q", out)
	}
	// 轮次分隔 hr
	if !strings.Contains(out, "<hr>") {
		t.Fatalf("turn 分隔缺失: %q", out)
	}
}

func TestRenderSessionHTMLErrorAndSkip(t *testing.T) {
	payloads := []sdk.SessionEvent{
		{Kind: sdk.EventToolResult, Payload: sdk.ToolResultEvent{CallID: "2", Name: "read", Content: "", Error: "缺失"}},
		{Kind: sdk.EventAgentError, Payload: errors.New("agent 报错")},
	}
	out := renderSessionHTML(payloads)
	if !strings.Contains(out, "tool err") || !strings.Contains(out, "缺失") {
		t.Fatalf("失败结果应 err 类: %q", out)
	}
	if !strings.Contains(out, `class="msg err"`) || !strings.Contains(out, "agent 报错") {
		t.Fatalf("agent error(字符串 payload)应 err 块: %q", out)
	}
	// 空事件流也产完整文档
	if out := renderSessionHTML(nil); !strings.HasPrefix(out, "<!DOCTYPE html>") {
		t.Fatalf("空流应完整文档: %q", out)
	}
}
