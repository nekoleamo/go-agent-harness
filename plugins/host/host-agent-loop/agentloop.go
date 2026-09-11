// Package hostagentloop 提供 host-agent-loop 插件:ctx.agentLoop 默认 ReAct 循环。
// 轮次流程对齐 dsh:claim input → agent/pre-step → llm/stream → tool/call* → tools 流水线 → turn/end。
// 循环本身是可替换插件(dsh 同语义:agentLoop 是服务,实现可换)。
package hostagentloop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

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
	loop := &Loop{c: c, sessions: sessions, tools: tools, llm: llm, sp: sp, tc: newTurnControl()}
	if err := c.Provide("ctx.agentLoop", loop); err != nil {
		return nil, err
	}
	// ctx.turnControl 回合控制(TUI Esc / Web /api/control 共用取消入口)。
	if err := c.Provide("ctx.turnControl", loop.tc); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// control 实现 sdk.TurnControl:并发安全的回合取消注册表。
// 每次 Run 派生可取消 ctx 并 register 拿到 token,回合结束(任意路径)defer unregister。
// Cancel 先摘快照再解锁调用(回调可能触发 unregister,防自锁);CancelFunc 幂等。
type control struct {
	mu      sync.Mutex
	seq     uint64
	cancels map[uint64]context.CancelFunc
}

func newTurnControl() *control { return &control{cancels: make(map[uint64]context.CancelFunc)} }

// register 注册一个回合取消函数,返回注销 token。
func (c *control) register(fn context.CancelFunc) uint64 {
	c.mu.Lock()
	c.seq++
	c.cancels[c.seq] = fn
	c.mu.Unlock()
	return c.seq
}

func (c *control) unregister(tok uint64) {
	c.mu.Lock()
	delete(c.cancels, tok)
	c.mu.Unlock()
}

func (c *control) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.cancels) > 0
}

func (c *control) Cancel() {
	c.mu.Lock()
	fns := make([]context.CancelFunc, 0, len(c.cancels))
	for _, fn := range c.cancels {
		fns = append(fns, fn)
	}
	c.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// Loop 实现 sdk.AgentLoop。
type Loop struct {
	c        sdk.Ctx
	sessions sdk.SessionLog
	tools    sdk.ToolRegistry
	llm      sdk.LLMService
	sp       sdk.SystemPromptService
	tc       *control // ctx.turnControl 实现(回合取消注册表)
	finished bool     // 当前轮次是否应结束(step 内修改,单 goroutine 使用)
	reminded bool     // 本回合已给过伪调用提醒(每回合最多 1 次,防无限修正循环)
	reminder string   // 待注入下轮的提醒消息(伪调用检测触发)
}

// Run 处理一次用户输入直至一轮完成(无附件;等价 RunWithAttachments nil)。
func (l *Loop) Run(ctx context.Context, input string) error {
	return l.RunWithAttachments(ctx, input, nil)
}

// RunWithAttachments 处理一次用户输入(附件一期:图片随消息视觉注入,文件路径引用)。
func (l *Loop) RunWithAttachments(ctx context.Context, input string, atts []sdk.Attachment) error {
	// 回合级可取消 ctx:派生 child 并注册到 ctx.turnControl(TUI Esc/Web 取消经
	// Cancel() 取消同一回合);父 ctx 取消沿链生效;回合结束(任意返回路径)注销并释放。
	runCtx, runCancel := context.WithCancel(ctx)
	var tok uint64
	if l.tc != nil { // 直接构造的 Loop(旧测试/无 turnControl 场景)跳过注册
		tok = l.tc.register(runCancel)
		defer l.tc.unregister(tok)
	}
	defer runCancel()

	l.c.Emit(runCtx, "agent/status", "running", sdk.Emit)
	// 回合级状态重置:伪调用提醒每回合至多一次
	l.reminded = false
	l.reminder = ""
	if err := l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: input, Attachments: atts}}); err != nil {
		return err
	}
	for step := 0; step < maxSteps; step++ {
		if err := l.step(runCtx); err != nil {
			if errors.Is(err, context.Canceled) {
				l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "cancelled"})
			} else {
				l.c.Emit(runCtx, "agent/error", err, sdk.Emit)
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
	tools := l.tools.List()
	messages := l.sp.Assemble(history, tools)
	// 伪调用提醒注入(上步检测到文本伪造工具调用;作为追加输入给模型修正机会)
	if l.reminder != "" {
		messages = append(messages, sdk.LLMMessage{Role: sdk.RoleUser, Content: l.reminder})
		l.reminder = ""
	}

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

	// 结构化 tool_calls 依赖 tools 下发(此前只注入系统提示文本,模型无法走 API 结构化调用,只能正文伪调用 → 工具永不执行)
	req := &sdk.LLMRequest{Messages: messages, Tools: tools}
	resp, err := l.llm.Complete(ctx, req, onChunk)
	if err != nil {
		// 包装模型名(host-usage-stats 经 agent/error 解析错误文本学习窗口;
		// Unwrap 保留,重试/取消 errors.Is 判定不变)
		return &sdk.LLMError{Model: req.Model, Err: err}
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

	// 记录本轮 token 消耗(session/usage;host-usage-stats 订阅累计;无 usage 数据不记)。
	// 携带请求模型名(host-llm 已在 req.Model 填当前模型,统计按模型解析上下文窗口)。
	if final.Usage.PromptTokens > 0 || final.Usage.CompletionTokens > 0 {
		l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventUsage,
			Payload: sdk.UsageEvent{Model: req.Model, Usage: final.Usage}})
	}

	if len(calls) == 0 {
		// 无真实工具调用但正文含伪调用标记(如 <tool_calls>/<invoke>):给一次提醒修正机会
		if !l.reminded && containsFakeToolCall(final.Message.Content) {
			l.reminded = true
			l.reminder = fakeToolCallReminder()
			l.sessions.Append(sdk.SessionEvent{Kind: sdk.EventStepEnd})
			return nil // 不结束:下一轮带提醒重新请求
		}
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

// fakeToolCallMarkers 文本伪调用强信号标记(小写比对)。模型把工具调用格式写进回复正文
// (未走结构化 tool_calls)时 gah 不会执行——检测这些标记以提醒模型,不静默"假装已调用"。
var fakeToolCallMarkers = []string{
	"<tool_calls>",
	"<tool_call>",
	"<invoke",
	"<function_call>",
	"<function_calls>",
	"<antml:invoke", // Claude XML 工具调用(正文形式=未执行)
	"<dsml>",
	"<mcptool",
}

// containsFakeToolCall 是否含正文伪调用标记(纯函数,可测)。空文本/无标记 → false。
func containsFakeToolCall(text string) bool {
	lower := strings.ToLower(text)
	for _, m := range fakeToolCallMarkers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// fakeToolCallReminder 伪调用提醒文案(注入模型;指出正文调用不会被执行、如何修正)。
func fakeToolCallReminder() string {
	return "【系统提醒】你的上一条回复包含文本形式的工具调用标记(如 <tool_calls>/<invoke>/<antml:invoke> 等),但 gah 不会执行正文中的调用——它只执行 API 结构化 tool_calls 字段里的工具调用。" +
		"若确实需要调用工具,请改用工具调用功能重新发起;若当前没有可用工具或调用未实际执行,请直接给出结论或如实说明,不要编造调用与结果。"
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
