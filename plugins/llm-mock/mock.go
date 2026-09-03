// Package llmmock 提供 llm-mock 插件:脚本化流式适配器(dev/测试用,不需要 API key)。
// 行为由 Manifest 配置:data.script = JSON 数组,每项 {text?|tool?:{name,args}, finish?}。
// 脚本每一步 = 一次请求的响应(按请求序号消费),模拟多轮 ReAct。CI 不依赖外网。
package llmmock

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

var requestSeq atomic.Uint64

// Plugin 实现 llm-mock。
type Plugin struct{}

func (p *Plugin) Name() string { return "llm-mock" }

type tool struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

type step struct {
	Text   string `json:"text"`
	Tool   *tool  `json:"tool"`
	Finish string `json:"finish"`
}

// Start 注册适配器到 ctx.llm。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	a := &Adapter{steps: defaultSteps()}
	if m != nil && m.Data != nil {
		if raw, ok := m.Data["script"].(string); ok && raw != "" {
			var steps []step
			if err := json.Unmarshal([]byte(raw), &steps); err != nil {
				return nil, fmt.Errorf("llm-mock: script: %w", err)
			}
			a.steps = steps
		}
	}
	var llm sdk.LLMService
	if err := c.Inject("ctx.llm", &llm); err != nil {
		return nil, err
	}
	d := llm.RegisterAdapter(a)
	llm.SetModel("mock-model")
	return d, nil
}

// defaultSteps 默认脚本:请求 1 → shell 工具调用;请求 2 → 文本收尾。
func defaultSteps() []step {
	return []step{
		{Tool: &tool{Name: "shell", Args: `{"command":"echo mock-ok"}`}},
		{Text: "命令已执行,输出 mock-ok。", Finish: "stop"},
	}
}

// Adapter 实现 sdk.LLMAdapter。
type Adapter struct {
	steps []step
	req   atomic.Uint64 // 请求序号:脚本每一步 = 一次请求的响应
}

func (a *Adapter) Name() string { return "llm-mock" }

// Complete 按请求序号消费脚本一步。
func (a *Adapter) Complete(ctx context.Context, _ *sdk.LLMRequest, onChunk func(ev sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	idx := int(a.req.Add(1) - 1)
	if idx >= len(a.steps) {
		idx = len(a.steps) - 1 // 超界时重复最后一步
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := a.steps[idx]

	if s.Tool != nil {
		id := fmt.Sprintf("mock_call_%d", requestSeq.Add(1))
		call := sdk.ToolCall{ID: id, Name: s.Tool.Name, Arguments: s.Tool.Args}
		ev := sdk.LLMStreamEvent{ToolCallID: id, ToolCallName: call.Name, ToolCallArgs: call.Arguments}
		if onChunk != nil {
			if err := onChunk(ev); err != nil {
				return nil, err
			}
		}
		done := sdk.LLMStreamEvent{Done: true, FinishReason: sdk.FinishReasonToolCalls}
		done.Message = sdk.LLMMessage{Role: sdk.RoleAssistant, ToolCalls: []sdk.ToolCall{call}}
		if onChunk != nil {
			if err := onChunk(done); err != nil {
				return nil, err
			}
		}
		return &sdk.LLMResponse{Message: done.Message, FinishReason: sdk.FinishReasonToolCalls}, nil
	}

	if s.Text != "" && onChunk != nil {
		if err := onChunk(sdk.LLMStreamEvent{Delta: s.Text}); err != nil {
			return nil, err
		}
	}
	finish := sdk.FinishReasonStop
	done := sdk.LLMStreamEvent{Done: true, FinishReason: finish}
	done.Message = sdk.LLMMessage{Role: sdk.RoleAssistant, Content: s.Text}
	if onChunk != nil {
		if err := onChunk(done); err != nil {
			return nil, err
		}
	}
	return &sdk.LLMResponse{Message: done.Message, FinishReason: finish}, nil
}
