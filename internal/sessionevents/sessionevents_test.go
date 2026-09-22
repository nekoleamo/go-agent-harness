// 编解码共享包自测:载荷还原(全 Kind + 容错分支)与只读读取(坏行/空行/缺文件)。
package sessionevents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestNormalizePayloadByKind(t *testing.T) {
	cases := []struct {
		kind string
		want any
	}{
		{sdk.EventUserMessage, sdk.UserMessage{}},
		{sdk.EventAssistantChunk, sdk.LLMStreamEvent{}},
		{sdk.EventAssistantMessage, sdk.AssistantMessage{}},
		{sdk.EventToolCall, sdk.ToolCallEvent{}},
		{sdk.EventToolResult, sdk.ToolResultEvent{}},
		{sdk.EventFileChange, sdk.FileChangeEvent{}},
	}
	for _, c := range cases {
		ev := sdk.SessionEvent{Kind: c.kind, Payload: map[string]any{}}
		got := NormalizePayload(&ev)
		if _, ok := got.(map[string]any); ok {
			t.Fatalf("%s 应还原为具体类型,得 map", c.kind)
		}
		if typeName(got) != typeName(c.want) {
			t.Fatalf("%s 还原类型不符: got %T want %T", c.kind, got, c.want)
		}
	}
	// 未知 Kind → 保留 map(消费者自行容忍)
	ev := sdk.SessionEvent{Kind: "unknown/kind", Payload: map[string]any{"a": 1}}
	if _, ok := NormalizePayload(&ev).(map[string]any); !ok {
		t.Fatal("未识别 Kind 应保留 map")
	}
	// 非 map 载荷 → 原样(空/nil/字符串)
	ev = sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: "raw"}
	if got := NormalizePayload(&ev); got != "raw" {
		t.Fatalf("非 map 载荷应原样返回,得 %#v", got)
	}
	ev = sdk.SessionEvent{Kind: sdk.EventUserMessage}
	if got := NormalizePayload(&ev); got != nil {
		t.Fatalf("nil 载荷应原样返回,得 %#v", got)
	}
	// 类型不匹配(map 里字段类型不对,二次解析失败)→ 保留 map,不 panic
	ev = sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: map[string]any{"Content": 42}}
	if _, ok := NormalizePayload(&ev).(map[string]any); !ok {
		t.Fatal("二次解析失败应保留 map")
	}
}

func TestReadFileTolerant(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	body := strings.Join([]string{
		`{"Kind":"user/message","Seq":1,"Payload":{"Content":"你好"}}`,
		"",
		"坏行不是 json",
		`{"Kind":"tool/result","Seq":2,"Payload":{"Name":"bash","Content":"ok"}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(fp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	evs, err := ReadFile(fp)
	if err != nil {
		t.Fatalf("读文件失败: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("应保留 2 条有效事件(坏行/空行跳过),得 %d", len(evs))
	}
	u, ok := evs[0].Payload.(sdk.UserMessage)
	if !ok || u.Content != "你好" {
		t.Fatalf("首条应为类型化 UserMessage: %#v", evs[0].Payload)
	}
	if r, ok := evs[1].Payload.(sdk.ToolResultEvent); !ok || r.Content != "ok" {
		t.Fatalf("次条应为类型化 ToolResultEvent: %#v", evs[1].Payload)
	}
	// 缺文件:显式错误(不静默返回空)
	if _, err := ReadFile(filepath.Join(dir, "nope.jsonl")); err == nil {
		t.Fatal("缺文件应报错")
	}
}

// typeName 取类型名(避免为断言引入反射依赖的额外分支)。
func typeName(v any) string {
	switch v.(type) {
	case sdk.UserMessage:
		return "UserMessage"
	case sdk.LLMStreamEvent:
		return "LLMStreamEvent"
	case sdk.AssistantMessage:
		return "AssistantMessage"
	case sdk.ToolCallEvent:
		return "ToolCallEvent"
	case sdk.ToolResultEvent:
		return "ToolResultEvent"
	case sdk.FileChangeEvent:
		return "FileChangeEvent"
	}
	return "other"
}
