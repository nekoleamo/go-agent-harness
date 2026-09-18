// tool-subagent 单测(T1):delegate 工具面分发/空 task/错误回传/结果透传。
package toolsubagent

import (
	"context"
	"encoding/json"
	"fmt"
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
	want := []string{"delegate", "spawn", "fork", "agents", "agent_status", "agent_kill", "send_message"}
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
	forked  string
	sent    string
	killed  string
	handles []sdk.AgentHandle
}

func (s *stubFanout) Agent(_ context.Context, input string) (string, error) {
	return "agent:" + input, nil
}
func (s *stubFanout) Parallel(context.Context, []string) []sdk.FanoutResult { return nil }
func (s *stubFanout) Pipeline(context.Context, []string) ([]sdk.FanoutResult, string, error) {
	return nil, "", nil
}
func (s *stubFanout) SpawnAgent(_ context.Context, input string) (string, error) {
	s.spawned = input
	return "ag9", nil
}
func (s *stubFanout) Fork(_ context.Context, input string) (string, error) {
	s.forked = input
	return "ag10", nil
}
func (s *stubFanout) SendMessage(id, message string) error {
	if id != "ag9" {
		return fmt.Errorf("无此会话 %q", id)
	}
	s.sent = message
	return nil
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

// TestForkAction fork → 带父上下文后台启动,返回句柄。
func TestForkAction(t *testing.T) {
	st := &stubFanout{}
	tool := NewTool(st).(*Tool)
	out, err := tool.Execute(context.Background(), `{"action":"fork","task":"继承父上下文的任务"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["agent_id"] != "ag10" || m["state"] != "running" {
		t.Fatalf("fork 应返回句柄: %+v", m)
	}
	if !strings.Contains(st.forked, "继承父上下文") {
		t.Fatalf("task 应透传: %q", st.forked)
	}
	// 空 task → 结构化错误
	out, _ = tool.Execute(context.Background(), `{"action":"fork"}`)
	if _, ok := out.(map[string]any)["error"]; !ok {
		t.Fatalf("fork 缺 task 应报错: %+v", out)
	}
}

// TestSendMessageAction send_message → 注入消息透传。
func TestSendMessageAction(t *testing.T) {
	st := &stubFanout{}
	tool := NewTool(st).(*Tool)
	out, err := tool.Execute(context.Background(), `{"action":"send_message","agent_id":"ag9","message":"补充要求"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["sent"] != "ag9" || !strings.Contains(st.sent, "补充要求") {
		t.Fatalf("send_message 应透传注入: %+v sent=%q", m, st.sent)
	}
	// 缺 agent_id/message → 结构化错误
	for _, a := range []string{`{"action":"send_message","message":"x"}`, `{"action":"send_message","agent_id":"ag9"}`} {
		out, _ = tool.Execute(context.Background(), a)
		if _, ok := out.(map[string]any)["error"]; !ok {
			t.Fatalf("%s 缺参应报错: %+v", a, out)
		}
	}
	// 后端错误回传
	st2 := &stubFanout{}
	tool2 := NewTool(st2).(*Tool)
	out, _ = tool2.Execute(context.Background(), `{"action":"send_message","agent_id":"nope","message":"x"}`)
	if !strings.Contains(out.(map[string]any)["error"].(string), "无此会话") {
		t.Fatalf("后端错误应回传: %+v", out)
	}
}

// TestNoFanoutBackend 未装配 fanout 时控制面动作显式报错。
func TestNoFanoutBackend(t *testing.T) {
	tool := NewTool(nil).(*Tool)
	for _, a := range []string{`{"action":"delegate","task":"t"}`, `{"action":"spawn","task":"t"}`,
		`{"action":"fork","task":"t"}`, `{"action":"send_message","agent_id":"x","message":"m"}`,
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

// isoFanout 具备隔离能力的 fanout 替身(记录请求,回传固定 worktree)。
type isoFanout struct {
	stubFanout
	reqs []sdk.WorktreeRun
}

func (s *isoFanout) RunInWorktree(_ context.Context, req sdk.WorktreeRun) (sdk.WorktreeRunResult, error) {
	s.reqs = append(s.reqs, req)
	wt := sdk.Worktree{ID: "repo-wt1", Path: "/gah/worktrees/repo-wt1", Branch: "gah/repo-wt1", Base: "abc12345def"}
	return sdk.WorktreeRunResult{Worktree: wt, Text: "隔离完成:已改 X",
		Handle: sdk.AgentHandle{ID: "ag7", State: sdk.AgentRunning}}, nil
}

// 编译期:isoFanout 同时满足两个能力接口。
var (
	_ sdk.FanoutService  = (*isoFanout)(nil)
	_ sdk.IsolatedFanout = (*isoFanout)(nil)
)

// TestIsolateWorktree 隔离运行:delegate/spawn/fork 三 action 映射到 RunInWorktree 语义,
// 回包必须含 worktree 路径/分支(父级据此合并或回收)。
func TestIsolateWorktree(t *testing.T) {
	iso := &isoFanout{}
	tool := NewTool(iso).(*Tool)

	out, err := tool.Execute(context.Background(), `{"action":"delegate","task":"实现 X","isolate":"worktree"}`)
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if m["worktree"] != "/gah/worktrees/repo-wt1" || m["branch"] != "gah/repo-wt1" || m["isolated"] != true {
		t.Fatalf("隔离回包应含 worktree/branch: %+v", m)
	}
	if m["result"] != "隔离完成:已改 X" {
		t.Fatalf("同步隔离应回传子代理文本: %+v", m)
	}
	if len(iso.reqs) != 1 || !iso.reqs[0].Sync || iso.reqs[0].Fork {
		t.Fatalf("delegate 应映射 Sync=true: %+v", iso.reqs)
	}
	if iso.reqs[0].Label == "" {
		t.Fatalf("应自动生成 worktree 标签(便于 /worktree list 辨认): %+v", iso.reqs[0])
	}

	// spawn:后台(不 Sync),回包带 agent_id/worktree
	out, _ = tool.Execute(context.Background(), `{"action":"spawn","task":"后台改 X","isolate":"worktree"}`)
	m = out.(map[string]any)
	if m["agent_id"] != "ag7" || m["state"] != "running" {
		t.Fatalf("spawn 应回句柄: %+v", m)
	}
	if iso.reqs[1].Sync || iso.reqs[1].Fork {
		t.Fatalf("spawn 应映射 Sync=false: %+v", iso.reqs[1])
	}

	// fork:后台 + 父上下文
	out, _ = tool.Execute(context.Background(), `{"action":"fork","task":"带上下文改 X","isolate":"worktree"}`)
	if iso.reqs[2].Sync || !iso.reqs[2].Fork {
		t.Fatalf("fork 应映射 Fork=true: %+v", iso.reqs[2])
	}
	_ = out
}

// TestIsolateUnsupported 宿主 fanout 不支持隔离 → 显式报错(不静默退化为主工作区执行)。
func TestIsolateUnsupported(t *testing.T) {
	tool := NewTool(&stubFanout{}).(*Tool)
	for _, a := range []string{
		`{"action":"delegate","task":"x","isolate":"worktree"}`,
		`{"action":"spawn","task":"x","isolate":"worktree"}`,
		`{"action":"fork","task":"x","isolate":"worktree"}`,
	} {
		out, err := tool.Execute(context.Background(), a)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.(map[string]any)["error"].(string), "不支持 worktree 隔离") {
			t.Fatalf("%s 应显式报不支持: %+v", a, out)
		}
	}
}

// TestIsolateInvalidValue isolate 取值非法/用错 action → 结构化错误(防拼错静默不隔离)。
func TestIsolateInvalidValue(t *testing.T) {
	iso := &isoFanout{}
	tool := NewTool(iso).(*Tool)
	out, _ := tool.Execute(context.Background(), `{"action":"delegate","task":"x","isolate":"sandbox"}`)
	if !strings.Contains(out.(map[string]any)["error"].(string), "isolate 仅支持") {
		t.Fatalf("非法 isolate 应报错: %+v", out)
	}
	out, _ = tool.Execute(context.Background(), `{"action":"agents","isolate":"worktree"}`)
	if !strings.Contains(out.(map[string]any)["error"].(string), "仅适用于") {
		t.Fatalf("用错 action 应报错: %+v", out)
	}
	if len(iso.reqs) != 0 {
		t.Fatalf("非法入参不得触发隔离运行: %+v", iso.reqs)
	}
}

// TestWorktreeLabel 标签取任务首行前 12 个字符(目录名可读且不超长)。
func TestWorktreeLabel(t *testing.T) {
	if got := worktreeLabel("实现 X\n第二行"); got != "实现 X" {
		t.Fatalf("应取首行: %q", got)
	}
	long := strings.Repeat("字", 30)
	if got := worktreeLabel(long); len([]rune(got)) != 12 {
		t.Fatalf("应截断到 12 字符: %q", got)
	}
}
