// tool-subagent 单测(T1):delegate 工具面分发/空 task/错误回传/结果透传。
package toolsubagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestDelegateSuccess delegate → agent 结果透传。
func TestDelegateSuccess(t *testing.T) {
	called := ""
	tool := NewToolFn(func(_ context.Context, input string) (string, error) {
		called = input
		return "子代理完成:已写入 X", nil
	}).(*Tool)
	out, err := tool.Execute(context.Background(), `{"action":"delegate","task":"实现功能 X,给出验收口径"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(called, "验收口径") {
		t.Fatalf("task 应完整透传: %q", called)
	}
	m := out.(map[string]any)
	if m["result"] != "子代理完成:已写入 X" {
		t.Fatalf("结果应透传: %+v", m)
	}
}

// TestDelegateEmptyTask 空 task → 结构化错误(不中断 turn)。
func TestDelegateEmptyTask(t *testing.T) {
	tool := NewToolFn(func(_ context.Context, _ string) (string, error) {
		t.Fatal("不应调用 agent")
		return "", nil
	}).(*Tool)
	out, err := tool.Execute(context.Background(), `{"action":"delegate","task":"  "}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if !strings.Contains(m["error"].(string), "需 task") {
		t.Fatalf("错误提示应明确: %+v", m)
	}
}

// TestDelegateAgentError agent 错误 → 结构化错误回传。
func TestDelegateAgentError(t *testing.T) {
	tool := NewToolFn(func(_ context.Context, _ string) (string, error) {
		return "", sdkError("子代理上下文不足")
	}).(*Tool)
	out, err := tool.Execute(context.Background(), `{"action":"delegate","task":"任务"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if !strings.Contains(m["error"].(string), "子代理上下文不足") {
		t.Fatalf("agent 错误应回传: %+v", m)
	}
}

// TestNilFanout 宿主未装配 ctx.fanout → 显式可操作错误。
func TestNilFanout(t *testing.T) {
	tool := NewTool(nil).(*Tool)
	out, err := tool.Execute(context.Background(), `{"action":"delegate","task":"任务"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if !strings.Contains(m["error"].(string), "ctx.fanout 未装配") {
		t.Fatalf("未装配应显式提示: %+v", m)
	}
}

// TestUnknownAction 未知 action → 枚举提示。
func TestUnknownAction(t *testing.T) {
	tool := NewToolFn(func(_ context.Context, _ string) (string, error) {
		t.Fatal("不应调用")
		return "", nil
	}).(*Tool)
	out, err := tool.Execute(context.Background(), `{"action":"jump"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if !strings.Contains(m["error"].(string), "delegate") {
		t.Fatalf("未知 action 应提示枚举: %+v", m)
	}
}

// TestDefinition schema 与语义(名称/枚举/描述含隔离声明)。
func TestDefinition(t *testing.T) {
	def := NewTool(nil).Definition()
	if def.Name != "subagent" {
		t.Fatalf("工具名: %s", def.Name)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	raw, _ := json.Marshal(def.InputSchema)
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	act, _ := schema.Properties["action"].(map[string]any)
	enum, _ := act["enum"].([]any)
	if len(enum) != 1 || enum[0] != "delegate" {
		t.Fatalf("T1 仅 delegate action: %v", enum)
	}
	if !strings.Contains(def.Description, "上下文隔离") {
		t.Fatalf("描述应含隔离语义")
	}
}

// sdkError 简易错误(避免依赖 errors 的额外导入风格,直接用标准库)。
type sdkError string

func (e sdkError) Error() string { return string(e) }

// 编译期接口校验:Tool 实现 sdk.Tool;Plugin 实现 sdk.Plugin。
var (
	_ sdk.Tool   = (*Tool)(nil)
	_ sdk.Plugin = (*Plugin)(nil)
)
