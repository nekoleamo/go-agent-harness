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
	want := []string{"delegate", "spawn", "agents", "agent_status", "agent_kill"}
	if len(enum) != len(want) {
		t.Fatalf("action 集不符: %v", enum)
	}
	for i, w := range want {
		if enum[i] != w {
			t.Fatalf("action[%d] 应 %q: %v", i, w, enum)
		}
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

// stubFanout 后台会话控制面测试替身(实现 sdk.FanoutService 全接口)。
type stubFanout struct {
	spawned string
	killed  string
	handles []sdk.AgentHandle
}

func (s *stubFanout) Agent(_ context.Context, input string) (string, error) { return "agent:" + input, nil }
func (s *stubFanout) Parallel(context.Context, []string) []sdk.FanoutResult { return nil }
func (s *stubFanout) Pipeline(context.Context, []string) ([]sdk.FanoutResult, string, error) {
	return nil, "", nil
}
func (s *stubFanout) SpawnAgent(_ context.Context, input string) (string, error) {
	s.spawned = input
	return "ag9", nil
}
func (s *stubFanout) ListAgents() []sdk.AgentHandle {
	s.handles = []sdk.AgentHandle{{ID: "ag9", Input: "x", State: sdk.AgentDone, Result: "r"}}
	return s.handles
}
func (s *stubFanout) AgentStatus(id string) (sdk.AgentHandle, bool) {
	if id != "ag9" {
		return sdk.AgentHandle{}, false
	}
	return sdk.AgentHandle{ID: "ag9", State: sdk.AgentDone, Result: "done-result"}, true
}
func (s *stubFanout) KillAgent(id string) error { s.killed = id; return nil }

// TestSpawnAction spawn → agent_id 句柄返回。
func TestSpawnAction(t *testing.T) {
	st := &stubFanout{}
	tool := NewTool(st).(*Tool)
	out, err := tool.Execute(context.Background(), `{"action":"spawn","task":"后台长任务"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["agent_id"] != "ag9" || m["state"] != "running" {
		t.Fatalf("spawn 应返回句柄: %+v", m)
	}
	if !strings.Contains(st.spawned, "后台长任务") {
		t.Fatalf("task 应透传: %q", st.spawned)
	}
}

// TestAgentsAction agents → 列表返回。
func TestAgentsAction(t *testing.T) {
	st := &stubFanout{}
	tool := NewTool(st).(*Tool)
	out, err := tool.Execute(context.Background(), `{"action":"agents"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	list, ok := m["agents"].([]sdk.AgentHandle)
	if !ok || len(list) != 1 || list[0].ID != "ag9" {
		t.Fatalf("agents 应返回列表: %+v", m)
	}
}

// TestAgentStatusAndKill agent_status/agent_kill 分发。
func TestAgentStatusAndKill(t *testing.T) {
	st := &stubFanout{}
	tool := NewTool(st).(*Tool)
	out, err := tool.Execute(context.Background(), `{"action":"agent_status","agent_id":"ag9"}`)
	if err != nil {
		t.Fatal(err)
	}
	h := out.(sdk.AgentHandle)
	if h.State != sdk.AgentDone || h.Result != "done-result" {
		t.Fatalf("agent_status 应回状态: %+v", h)
	}
	// 缺 id → 结构化错误
	out, _ = tool.Execute(context.Background(), `{"action":"agent_status"}`)
	if _, ok := out.(map[string]any)["error"]; !ok {
		t.Fatalf("agent_status 缺 id 应报错: %+v", out)
	}
	out, err = tool.Execute(context.Background(), `{"action":"agent_kill","agent_id":"ag9"}`)
	if err != nil {
		t.Fatal(err)
	}
	if st.killed != "ag9" || out.(map[string]any)["killed"] != "ag9" {
		t.Fatalf("agent_kill 应分发: %+v killed=%q", out, st.killed)
	}
}

// TestNoFanoutBackend 未装配 fanout 时控制面动作显式报错。
func TestNoFanoutBackend(t *testing.T) {
	tool := NewTool(nil).(*Tool)
	for _, a := range []string{`{"action":"delegate","task":"t"}`, `{"action":"spawn","task":"t"}`,
		`{"action":"agents"}`, `{"action":"agent_kill","agent_id":"x"}`} {
		out, err := tool.Execute(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.(map[string]any)["error"].(string), "未装配") {
			t.Fatalf("%s 未装配应显式报错: %+v", a, out)
		}
	}
}

// 编译期:stubFanout 实现 sdk.FanoutService。
var _ sdk.FanoutService = (*stubFanout)(nil)
