// Package hostagentloop 提供 host-agent-loop 插件:ctx.agentLoop 默认 ReAct 循环。
// 轮次流程对齐 dsh:claim input → agent/pre-step → llm/stream → tool/call* → tools 流水线 → turn/end。
// 循环本身是可替换插件(dsh 同语义:agentLoop 是服务,实现可换)。
package hostagentloop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// maxSteps 单轮最大 ReAct 迭代(防死循环)。
const maxSteps = 10

// Plugin 实现 host-agent-loop。requires ctx.sessions/ctx.tools/ctx.llm/ctx.systemPrompt。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-agent-loop" }

// Start 注入依赖并注册 ctx.agentLoop 服务。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var sessions sdk.SessionLog
	var tools sdk.ToolRegistry
	var llm sdk.LLMService
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		return nil, err
	}
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	if err := c.Inject("ctx.llm", &llm); err != nil {
		return nil, err
	}
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		return nil, err
	}
	loop := &Loop{c: c, sessions: sessions, tools: tools, llm: llm, sp: sp}
	if err := c.Provide("ctx.agentLoop", loop); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Loop 实现 sdk.AgentLoop。
type Loop struct {
	c        sdk.Ctx
	sessions sdk.SessionLog
	tools    sdk.ToolRegistry
	llm      sdk.LLMService
	sp       sdk.SystemPromptService
	finished bool // 当前轮次是否应结束(step 内修改,单 goroutine 使用)
}

// Run 处理一次用户输入直至一轮完成。
func (l *Loop) Run(ctx context.Context, input string) error {
	l.c.Emit(ctx, "agent/status", "running", sdk.Emit)
	if err := l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: input}}); err != nil {
		return err
	}
	for step := 0; step < maxSteps; step++ {
		if err := l.step(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "cancelled"})
			} else {
				l.c.Emit(ctx, "agent/error", err, sdk.Emit)
			}
			l.c.Emit(context.Background(), "agent/status", "idle", sdk.Emit)
			return err
		}
		if l.finished {
			break
		}
	}
	l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "done"})
	l.c.Emit(context.Background(), "agent/status", "idle", sdk.Emit)
	return nil
}

// step 执行一轮 ReAct 迭代(单次模型请求 + 其工具调用)。
func (l *Loop) step(ctx context.Context) error {
	l.finished = false
	l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventStepStart})

	// agent/pre-step:waterfall 扩展点(M2 无监听器则直过;改写/拒绝留 policy 阶段)
	if _, err := l.c.Emit(ctx, "agent/pre-step", nil, sdk.Waterfall); err != nil {
		return fmt.Errorf("pre-step rejected: %w", err)
	}

	history := l.sessions.DeriveMessages()
	messages := l.sp.Assemble(history, l.tools.List())

	var (
		content strings.Builder
		calls   []sdk.ToolCall
		final   sdk.LLMResponse
	)
	onChunk := func(ev sdk.LLMStreamEvent) error {
		if ev.Delta != "" {
			content.WriteString(ev.Delta)
			l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventAssistantChunk, Payload: ev})
		}
		if ev.ToolCallID != "" {
			idx := findCall(&calls, ev.ToolCallID)
			calls[idx].Name += ev.ToolCallName
			calls[idx].Arguments += ev.ToolCallArgs
		}
		if ev.Done {
			final = sdk.LLMResponse{Message: ev.Message, FinishReason: ev.FinishReason, Usage: ev.Usage}
		}
		return nil
	}

	resp, err := l.llm.Complete(ctx, &sdk.LLMRequest{Messages: messages}, onChunk)
	if err != nil {
		return err
	}
	if resp != nil {
		final = *resp
	}
	if len(calls) > 0 {
		final.Message.ToolCalls = calls
	}
	if final.Message.Content == "" {
		final.Message.Content = content.String()
	}

	// 持久记录 assistant/message(模型可见即已记录)
	l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage,
		Payload: sdk.AssistantMessage{Content: final.Message.Content, ToolCalls: final.Message.ToolCalls}})

	if len(calls) == 0 {
		l.finished = true
		l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventStepEnd})
		return nil
	}

	// 执行工具调用(结果经 tool/result 事件与流水线;日志派生 RoleTool 消息供下轮)
	for _, call := range calls {
		l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventToolCall,
			Payload: sdk.ToolCallEvent{ID: call.ID, Name: call.Name, Arguments: call.Arguments}})
		res, err := l.tools.Execute(ctx, call.Name, call.Arguments)
		if err != nil {
			l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventToolResult,
				Payload: sdk.ToolResultEvent{CallID: call.ID, Name: call.Name, Error: err.Error()}})
		} else if res != nil {
			l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventToolResult,
				Payload: sdk.ToolResultEvent{CallID: call.ID, Name: call.Name, Content: res.Content, Error: res.Error}})
		}
	}
	l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventStepEnd})
	return nil
}

// findCall 按 ToolCallID 定位或追加(流式增量聚合)。
func findCall(calls *[]sdk.ToolCall, id string) int {
	for i := range *calls {
		if (*calls)[i].ID == id {
			return i
		}
	}
	*calls = append(*calls, sdk.ToolCall{ID: id})
	return len(*calls) - 1
}
