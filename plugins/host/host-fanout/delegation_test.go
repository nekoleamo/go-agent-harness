// 工具执行入口不变式(R10 ②,host-fanout 侧):子代理的工具调用必须经注入的
// ctx.tools(全流水线)执行,且策略 veto 必须以错误文本进入子代理上下文(可见、可纠偏)。
package hostfanout

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-llm"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fanoutRecordingTools registry 替身:记录调用,可注入 veto 结果。
type fanoutRecordingTools struct {
	mu    sync.Mutex
	calls []string
	res   sdk.ToolResult
}

func (r *fanoutRecordingTools) Register(sdk.Tool) sdk.Disposer { return func() {} }

func (r *fanoutRecordingTools) List() []sdk.ToolDefinition {
	return []sdk.ToolDefinition{{Name: "shell", Description: "执行 shell", InputSchema: map[string]any{"type": "object"}}}
}

func (r *fanoutRecordingTools) Get(name string) (sdk.ToolDefinition, bool) {
	for _, d := range r.List() {
		if d.Name == name {
			return d, true
		}
	}
	return sdk.ToolDefinition{}, false
}

func (r *fanoutRecordingTools) Execute(_ context.Context, name, args string) (*sdk.ToolResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, name+"|"+args)
	res := r.res
	r.mu.Unlock()
	return &res, nil
}

func (r *fanoutRecordingTools) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

var _ sdk.ToolRegistry = (*fanoutRecordingTools)(nil)

// recordingLLM 首个请求回一次工具调用,后续请求回终结文本;记录每次请求的消息与工具定义。
type recordingLLM struct {
	mu    sync.Mutex
	reqs  []sdk.LLMRequest
	tool  string
	args  string
	final string
}

func (a *recordingLLM) Name() string { return "recording" }

func (a *recordingLLM) Complete(ctx context.Context, req *sdk.LLMRequest, onChunk func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	a.mu.Lock()
	a.reqs = append(a.reqs, sdk.LLMRequest{Messages: append([]sdk.LLMMessage(nil), req.Messages...), Tools: req.Tools})
	n := len(a.reqs)
	a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n == 1 {
		call := sdk.ToolCall{ID: "c1", Name: a.tool, Arguments: a.args}
		if onChunk != nil {
			if err := onChunk(sdk.LLMStreamEvent{ToolCallID: "c1", ToolCallName: call.Name, ToolCallArgs: call.Arguments}); err != nil {
				return nil, err
			}
		}
		return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{call}},
			FinishReason: sdk.FinishReasonToolCalls}, nil
	}
	if onChunk != nil {
		if err := onChunk(sdk.LLMStreamEvent{Delta: a.final}); err != nil {
			return nil, err
		}
	}
	return &sdk.LLMResponse{Message: sdk.LLMMessage{Role: sdk.RoleAssistant, Content: a.final}, FinishReason: sdk.FinishReasonStop}, nil
}

func (a *recordingLLM) lastMessages() []sdk.LLMMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.reqs) == 0 {
		return nil
	}
	return a.reqs[len(a.reqs)-1].Messages
}

// capturingPrompt 系统提示替身:记录 Assemble 收到的工具定义(模型可见面来源)。
type capturingPrompt struct {
	mu    sync.Mutex
	tools []sdk.ToolDefinition
}

func (p *capturingPrompt) AddSection(sdk.SystemPromptSection) sdk.Disposer { return func() {} }

func (p *capturingPrompt) Assemble(history []sdk.LLMMessage, tools []sdk.ToolDefinition) []sdk.LLMMessage {
	p.mu.Lock()
	p.tools = append([]sdk.ToolDefinition(nil), tools...)
	p.mu.Unlock()
	return history
}

func (p *capturingPrompt) lastTools() []sdk.ToolDefinition {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tools
}

// buildContractEnv 装配 host-llm + 记录型适配器 + 捕获型 system-prompt + 本插件(替换真实 registry)。
func buildContractEnv(t *testing.T, reg *fanoutRecordingTools, ad *recordingLLM) (sdk.FanoutService, *capturingPrompt) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	var llm sdk.LLMService
	if err := c.Provide("ctx.llm", llm); err == nil {
		t.Fatal("nil 服务不应可注册")
	}
	if _, err := (&hostllm.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	sp := &capturingPrompt{}
	if err := c.Provide("ctx.systemPrompt", sdk.SystemPromptService(sp)); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.tools", sdk.ToolRegistry(reg)); err != nil {
		t.Fatal(err)
	}
	if err := c.Inject("ctx.llm", &llm); err != nil {
		t.Fatal(err)
	}
	llm.RegisterAdapter(ad)
	llm.SetModel("recording-model")
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var svc sdk.FanoutService
	if err := c.Inject("ctx.fanout", &svc); err != nil {
		t.Fatal(err)
	}
	return svc, sp
}

// TestSubAgentToolsGoThroughInjectedRegistry 子代理工具调用经注入 registry(参数原样),
// 模型可见的工具定义也取自 registry(可见面与执行面同源)。
func TestSubAgentToolsGoThroughInjectedRegistry(t *testing.T) {
	reg := &fanoutRecordingTools{res: sdk.ToolResult{Content: `{"output":"ok"}`}}
	ad := &recordingLLM{tool: "shell", args: `{"command":"echo sub"}`, final: "完成"}
	svc, sp := buildContractEnv(t, reg, ad)

	if _, err := svc.Agent(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	if calls := reg.snapshot(); len(calls) != 1 || calls[0] != `shell|{"command":"echo sub"}` {
		t.Fatalf("子代理应经注入 registry 执行且参数原样: %v", calls)
	}
	tools := sp.lastTools()
	if len(tools) != 1 || tools[0].Name != "shell" {
		t.Fatalf("模型可见工具定义应取自 registry: %+v", tools)
	}
}

// TestSubAgentPolicyVetoVisibleToModel veto(结果 Error = "blocked: ...")必须以错误文本
// 进入子代理上下文 —— 否则模型看不到被拦截,会当成功继续。
func TestSubAgentPolicyVetoVisibleToModel(t *testing.T) {
	reg := &fanoutRecordingTools{res: sdk.ToolResult{Error: "blocked: 沙箱拒绝写 workspace 外路径", Content: "{}"}}
	ad := &recordingLLM{tool: "shell", args: `{"command":"echo x > /tmp/a"}`, final: "受阻"}
	svc, _ := buildContractEnv(t, reg, ad)

	if _, err := svc.Agent(context.Background(), "任务"); err != nil {
		t.Fatal(err)
	}
	if calls := reg.snapshot(); len(calls) != 1 {
		t.Fatalf("veto 来自 registry 裁决,必须先经 registry: %v", calls)
	}
	var toolMsg string
	for _, m := range ad.lastMessages() {
		if m.Role == sdk.RoleTool {
			toolMsg = m.Content
		}
	}
	if !strings.Contains(toolMsg, "blocked: 沙箱拒绝") {
		t.Fatalf("veto 文本应进入子代理上下文: %q", toolMsg)
	}
}
