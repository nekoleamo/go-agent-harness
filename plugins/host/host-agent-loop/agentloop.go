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

// maxParallelToolCalls 同轮并行工具调用的并发上限(防模型一口气给几十个调用打爆资源)。
const maxParallelToolCalls = 4

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

	// runMu 回合串行化:sessions 追加与 DeriveMessages 是单写者模型,
	// 并发 Run(多路输入同时提交)会让回合互相交错、工具结果错位 → 显式串行不静默交错。
	runMu sync.Mutex
}

// turn 单回合状态(每回合独立对象;修复:此前挂在 Loop 上被并发回合互相踩)。
type turn struct {
	finished bool   // 本轮是否应结束
	reminded bool   // 本回合已给过伪调用提醒(每回合最多 1 次,防无限修正循环)
	reminder string // 待注入下轮的提醒消息(伪调用检测触发)
}

// appendEvents 记录会话事件并返回首个错误。
// 记录失败必须显式失败(此前全部忽略返回值 → 日志满/写盘失败时静默丢历史而模型仍继续)。
func (l *Loop) appendEvents(evs ...sdk.SessionEvent) error {
	for _, ev := range evs {
		if err := l.sessions.Append(ev); err != nil {
			return err
		}
	}
	return nil
}

// Run 处理一次用户输入直至一轮完成(无附件;等价 RunWithAttachments nil)。
func (l *Loop) Run(ctx context.Context, input string) error {
	return l.RunWithAttachments(ctx, input, nil)
}

// RunWithAttachments 处理一次用户输入(附件一期:图片随消息视觉注入,文件路径引用)。
func (l *Loop) RunWithAttachments(ctx context.Context, input string, atts []sdk.Attachment) error {
	// 回合串行化(见 runMu 注释)
	l.runMu.Lock()
	defer l.runMu.Unlock()

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
	t := &turn{} // 回合级状态(伪调用提醒每回合至多一次)
	// turn/start:回合起点标记(与 turn/end 配对;此前只声明未发出,S-P0-1 轨迹视图需要
	// 权威回合边界)。nil 载荷不参与 DeriveMessages 投影,旧会话缺该帧也能正常工作。
	if err := l.appendEvents(
		sdk.SessionEvent{Kind: sdk.EventTurnStart},
		sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: input, Attachments: atts}},
	); err != nil {
		l.c.Emit(context.Background(), "agent/status", "idle", sdk.Emit)
		return fmt.Errorf("session log: %w", err)
	}
	for step := 0; step < maxSteps; step++ {
		if err := l.step(runCtx, t); err != nil {
			if errors.Is(err, context.Canceled) {
				_ = l.appendEvents(sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "cancelled"})
			} else {
				l.c.Emit(runCtx, "agent/error", err, sdk.Emit)
			}
			l.c.Emit(context.Background(), "agent/status", "idle", sdk.Emit)
			return err
		}
		if t.finished {
			break
		}
	}
	if !t.finished {
		// 步数耗尽:此前静默记为 "done" 并返回 nil —— 模型/用户都看不出"未收敛"。
		// 现显式失败(maxSteps 保护仍生效,但事实不再被掩盖)。
		err := fmt.Errorf("agent: 达到最大步数 %d 仍未完成(可能工具循环或模型未收敛)", maxSteps)
		_ = l.appendEvents(sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "max_steps"})
		l.c.Emit(context.Background(), "agent/error", err, sdk.Emit)
		l.c.Emit(context.Background(), "agent/status", "idle", sdk.Emit)
		return err
	}
	if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventTurnEnd, Payload: "done"}); err != nil {
		l.c.Emit(context.Background(), "agent/status", "idle", sdk.Emit)
		return fmt.Errorf("session log: %w", err)
	}
	l.c.Emit(context.Background(), "agent/status", "idle", sdk.Emit)
	return nil
}

// step 执行一轮 ReAct 迭代(单次模型请求 + 其工具调用)。
func (l *Loop) step(ctx context.Context, t *turn) error {
	t.finished = false
	if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventStepStart}); err != nil {
		return fmt.Errorf("session log: %w", err)
	}

	// agent/pre-step:waterfall 扩展点(M2 无监听器则直过;改写/拒绝留 policy 阶段)
	if _, err := l.c.Emit(ctx, "agent/pre-step", nil, sdk.Waterfall); err != nil {
		return fmt.Errorf("pre-step rejected: %w", err)
	}

	history := l.sessions.DeriveMessages()
	tools := l.tools.List()
	messages := l.sp.Assemble(history, tools)
	// 伪调用提醒注入(上步检测到文本伪造工具调用;作为追加输入给模型修正机会)
	if t.reminder != "" {
		messages = append(messages, sdk.LLMMessage{Role: sdk.RoleUser, Content: t.reminder})
		t.reminder = ""
	}

	var (
		content strings.Builder
		calls   []sdk.ToolCall
		final   sdk.LLMResponse
	)
	onChunk := func(ev sdk.LLMStreamEvent) error {
		if ev.Delta != "" {
			content.WriteString(ev.Delta)
			if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventAssistantChunk, Payload: ev}); err != nil {
				return fmt.Errorf("session log: %w", err)
			}
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
	if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventAssistantMessage,
		Payload: sdk.AssistantMessage{Content: final.Message.Content, ToolCalls: final.Message.ToolCalls}}); err != nil {
		return fmt.Errorf("session log: %w", err)
	}

	// 记录本轮 token 消耗(session/usage;host-usage-stats 订阅累计;无 usage 数据不记)。
	// 携带请求模型名(host-llm 已在 req.Model 填当前模型,统计按模型解析上下文窗口)。
	if final.Usage.PromptTokens > 0 || final.Usage.CompletionTokens > 0 {
		if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventUsage,
			Payload: sdk.UsageEvent{Model: req.Model, Usage: final.Usage}}); err != nil {
			return fmt.Errorf("session log: %w", err)
		}
	}

	if len(calls) == 0 {
		// 无真实工具调用但正文含伪调用标记(如 <tool_calls>/<invoke>):给一次提醒修正机会
		if !t.reminded && containsFakeToolCall(final.Message.Content) {
			t.reminded = true
			t.reminder = fakeToolCallReminder()
			if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventStepEnd}); err != nil {
				return fmt.Errorf("session log: %w", err)
			}
			return nil // 不结束:下一轮带提醒重新请求
		}
		t.finished = true
		if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventStepEnd}); err != nil {
			return fmt.Errorf("session log: %w", err)
		}
		return nil
	}

	// 执行工具调用(结果经 tool/result 事件与流水线;日志派生 RoleTool 消息供下轮)。
	// 一轮可含多个调用(OpenAI/Anthropic 均支持并行 tool_calls):**并发执行** —— 串行会互等阻塞,
	// 典型是 ask_user_question 这类等用户作答的工具(第一问阻塞时第二问永远到不了,
	// 问题栈/「待答 N」无法成立)。事件落序固定为调用序(先全部 tool/call,再按序 tool/result),
	// 会话日志重放语义与串行时完全一致;单调用仍走原同步路径,行为零变化。
	for _, call := range calls {
		if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventToolCall,
			Payload: sdk.ToolCallEvent(call)}); err != nil {
			return fmt.Errorf("session log: %w", err)
		}
	}
	type toolOutcome struct{ content, errText string }
	outcomes := make([]toolOutcome, len(calls))
	runOne := func(i int) {
		defer func() { // 并行分支:单工具 panic 不得带走整个进程(转为结构化错误回传模型)
			if r := recover(); r != nil {
				outcomes[i] = toolOutcome{errText: fmt.Sprintf("工具 %q panic: %v", calls[i].Name, r)}
			}
		}()
		res, err := l.tools.Execute(ctx, calls[i].Name, calls[i].Arguments)
		switch {
		case err != nil:
			outcomes[i] = toolOutcome{errText: err.Error()}
		case res != nil:
			outcomes[i] = toolOutcome{content: res.Content, errText: res.Error}
		}
	}
	switch {
	case len(calls) == 1:
		runOne(0)
	case len(calls) > 1:
		sem := make(chan struct{}, maxParallelToolCalls)
		var wg sync.WaitGroup
		for i := range calls {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				runOne(i)
			}(i)
		}
		wg.Wait()
	}
	for i, call := range calls {
		if outcomes[i].content == "" && outcomes[i].errText == "" {
			continue // 与串行路径一致:结果为 nil 且无错时不落事件
		}
		if aerr := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventToolResult,
			Payload: sdk.ToolResultEvent{CallID: call.ID, Name: call.Name, Content: outcomes[i].content, Error: outcomes[i].errText}}); aerr != nil {
			return fmt.Errorf("session log: %w", aerr)
		}
	}
	if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventStepEnd}); err != nil {
		return fmt.Errorf("session log: %w", err)
	}
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
