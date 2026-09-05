package llmmock

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestParseScript 覆盖三种输入形态:JSON 字符串(既有)、YAML 嵌套列表(修复新增)、非法输入显式报错。
func TestParseScript(t *testing.T) {
	// JSON 字符串形态(原有兼容)
	steps, err := parseScript(`[{"tool":{"name":"echo","args":"{\"text\":\"hi\"}"}},{"text":"done","finish":"stop"}]`)
	if err != nil {
		t.Fatalf("JSON 字符串形态解析失败: %v", err)
	}
	if len(steps) != 2 || steps[0].Tool == nil || steps[0].Tool.Name != "echo" || steps[1].Text != "done" {
		t.Fatalf("JSON 字符串形态解析结果不符: %+v", steps)
	}

	// YAML 嵌套列表形态(yaml.v3 解码后为 []any{map[string]any})
	steps2, err := parseScript([]any{
		map[string]any{"tool": map[string]any{"name": "echo", "args": `{"text":"hi"}`}},
		map[string]any{"text": "done", "finish": "stop"},
	})
	if err != nil {
		t.Fatalf("YAML 嵌套列表形态解析失败: %v", err)
	}
	if len(steps2) != 2 || steps2[0].Tool == nil || steps2[0].Tool.Name != "echo" || steps2[1].Finish != "stop" {
		t.Fatalf("YAML 嵌套列表形态解析结果不符: %+v", steps2)
	}

	// 非法形态:对象 → 显式报错(修复点:此前静默回退默认脚本)
	if _, err := parseScript(map[string]any{"tool": "x"}); err == nil {
		t.Fatal("对象形态应报错")
	}
	// 空字符串 → 显式报错
	if _, err := parseScript("  "); err == nil {
		t.Fatal("空字符串应报错")
	}
	// 空列表 → 显式报错(否则 Adapter 会访问 steps[-1] panic)
	if _, err := parseScript([]any{}); err == nil {
		t.Fatal("空列表应报错")
	}
	// 非法 JSON 字符串 → 显式报错
	if _, err := parseScript("[not-json"); err == nil {
		t.Fatal("非法 JSON 应报错")
	}
}

// TestAdapterConsumesScript 验证脚本按请求序号消费:请求 1 调度工具,请求 2 文本收尾。
func TestAdapterConsumesScript(t *testing.T) {
	a := &Adapter{steps: []step{
		{Tool: &tool{Name: "echo", Args: `{"text":"hi"}`}},
		{Text: "第三方插件调用成功", Finish: "stop"},
	}}

	// 请求 1 → 工具调用
	var gotTool string
	resp, err := a.Complete(context.Background(), &sdk.LLMRequest{}, func(ev sdk.LLMStreamEvent) error {
		if ev.ToolCallName != "" {
			gotTool = ev.ToolCallName
		}
		return nil
	})
	if err != nil || resp.FinishReason != sdk.FinishReasonToolCalls || gotTool != "echo" {
		t.Fatalf("请求 1 应为工具调用: resp=%+v err=%v tool=%q", resp, err, gotTool)
	}

	// 请求 2 → 文本收尾
	var text strings.Builder
	resp2, err := a.Complete(context.Background(), &sdk.LLMRequest{}, func(ev sdk.LLMStreamEvent) error {
		if ev.Delta != "" {
			text.WriteString(ev.Delta)
		}
		return nil
	})
	if err != nil || resp2.FinishReason != sdk.FinishReasonStop || text.String() != "第三方插件调用成功" {
		t.Fatalf("请求 2 应为文本收尾: resp=%+v err=%v text=%q", resp2, err, text.String())
	}
}
