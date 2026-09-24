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

// maxStepsDefault 单轮最大 ReAct 迭代缺省值:`0` = 不设上限。
// 由插件 data.max_steps 覆盖:正数 = 上限(超出即显式失败),0/负 = 不限。
// 老口径是硬编码 10 步 —— 真机反馈「长任务太容易撞线」(2026-09-22:连续几次
// 回合都死在「达到最大步数 10」,同一个回合换个问法就能跑完,说明是阈值不够而非死循环)。
// 不设上限仍不是死循环:用户随时可取消(TUI Esc / POST /api/control),工具各自带超时。
const maxStepsDefault = 0

// maxParallelToolCalls 同轮并行工具调用的并发上限(防模型一口气给几十个调用打爆资源)。
const maxParallelToolCalls = 4

// Plugin 实现 host-agent-loop。requires ctx.sessions/ctx.tools/ctx.llm/ctx.systemPrompt。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-agent-loop" }

// Start 注入依赖并注册 ctx.agentLoop 服务。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
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
	// 提示通道(可选:未装配就跳过 —— 同 web/server.go 口径,不阻断自身启动)。
	// 溢出兜底压缩会改写模型看到的输入,属于“用户看不见的输入改写”,至少要在状态栏/toast 露面。
	var notices sdk.NoticeService
	_ = c.Inject("ctx.notices", &notices)
	loop := &Loop{c: c, sessions: sessions, tools: tools, llm: llm, sp: sp, notices: notices, tc: newTurnControl(), maxSteps: maxStepsFromManifest(m)}
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
// 同时实现 sdk.TurnSteerer:Steer 把消息投给运行中的回合(见 Steer)。
type control struct {
	mu      sync.Mutex
	seq     uint64
	cancels map[uint64]context.CancelFunc
	turns   map[uint64]*turn // 与 cancels 同键(tok):Steer 定向用
}

func newTurnControl() *control {
	return &control{cancels: map[uint64]context.CancelFunc{}, turns: map[uint64]*turn{}}
}

// register 注册一个回合(取消函数 + 回合状态),返回注销 token。
func (c *control) register(fn context.CancelFunc, t *turn) uint64 {
	c.mu.Lock()
	c.seq++
	c.cancels[c.seq] = fn
	c.turns[c.seq] = t
	c.mu.Unlock()
	return c.seq
}

func (c *control) unregister(tok uint64) {
	c.mu.Lock()
	delete(c.cancels, tok)
	delete(c.turns, tok)
	c.mu.Unlock()
}

// Steer 实现 sdk.TurnSteerer:把 text 投给最近注册的运行中回合,返回是否投出。
// 为何取最近:同一时刻正常只有一个交互回合(TUI/Web 各自串行化提交,定时任务回合由
// host-schedule 的 waitIdle 保证不与人回合并发),取最近 = 取那个唯一回合。
// 投递本身不阻塞(消息进回合队列),实际注入与落账发生在下一次模型请求组装之前。
func (c *control) Steer(text string) (bool, error) {
	if strings.TrimSpace(text) == "" {
		return false, errors.New("agentloop: 转向消息为空")
	}
	c.mu.Lock()
	var latest uint64
	var t *turn
	for tok, tt := range c.turns {
		if tt == nil {
			continue
		}
		if t == nil || tok > latest {
			latest, t = tok, tt
		}
	}
	c.mu.Unlock()
	if t == nil {
		return false, nil // 无运行回合:调用方回落(TUI 入队 / Web 409)
	}
	t.pushSteer(text)
	return true, nil
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
	notices  sdk.NoticeService // 可选(ctx.notices 未装配 = nil:跳过提示,不阻断)
	tc       *control          // ctx.turnControl 实现(回合取消注册表)
	maxSteps int               // 单轮最大步数(<=0 = 不限;见 maxStepsDefault)

	// runMu 回合串行化:sessions 追加与 DeriveMessages 是单写者模型,
	// 并发 Run(多路输入同时提交)会让回合互相交错、工具结果错位 → 显式串行不静默交错。
	runMu sync.Mutex
}

// turn 单回合状态(每回合独立对象;修复:此前挂在 Loop 上被并发回合互相踩)。
type turn struct {
	finished        bool   // 本轮是否应结束
	reminded        bool   // 本回合已给过伪调用提醒(每回合最多 1 次,防无限修正循环)
	reminder        string // 待注入下轮的提醒消息(伪调用检测触发)
	overflowRetried bool   // 本回合已因“端点报超窗”强制压缩并重试过(硬上限 1 次,防形成重试环)

	// steers 本回合中用户插进来的消息(人在模型跑工具链时按 Enter)。
	// 投递点 = 下一次模型请求组装之前(step 开头 drain)→ 模型在**本回合内**看到并响应,
	// 而不是等本回合结束另起一回合(口径:Enter 加入当前会话)。
	// 加锁:Steer 来自事件回调/其它 goroutine,drain 在回合 goroutine。
	steerMu sync.Mutex
	steers  []string
}

// pushSteer 暂存一条转向消息(等下一次 step 边界注入)。
func (t *turn) pushSteer(text string) {
	t.steerMu.Lock()
	t.steers = append(t.steers, text)
	t.steerMu.Unlock()
}

// takeSteers 取走全部待注入转向消息(原序;无则 nil)。
func (t *turn) takeSteers() []string {
	t.steerMu.Lock()
	defer t.steerMu.Unlock()
	if len(t.steers) == 0 {
		return nil
	}
	out := t.steers
	t.steers = nil
	return out
}

// hasSteers 是否还有未注入的转向消息(回合收尾判据:有则本回合不结束)。
func (t *turn) hasSteers() bool {
	t.steerMu.Lock()
	defer t.steerMu.Unlock()
	return len(t.steers) > 0
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

// injectSteers 把待注入的转向消息逐条落账为 EventUserMessage(原序、逐条独立帧:
// 导出/轨迹视图的分隔与用户实际发送一致,不合并成一段文本)。
// 必须在 assemble 之前调用:assemble 经 DeriveMessages 读日志,只推内存队列模型看不到
// (不变量:模型可见即已记录)。
func (l *Loop) injectSteers(t *turn) error {
	msgs := t.takeSteers()
	for _, m := range msgs {
		if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventUserMessage,
			Payload: sdk.UserMessage{Content: m}}); err != nil {
			return fmt.Errorf("session log: %w", err)
		}
	}
	return nil
}

// emitDroppedSteers 回合结束(任意路径:完成/取消/失败)时把仍未注入的转向消息经事件
// 交回发起端(TUI 回「待发」队列 / Web 推回输入框)。
// 已注入的已落账(属于历史)不在此列 —— 语义是「你的话没被模型看到,还给你」,
// 不是「撤回你说过的话」。不静默丢:事件名 agent/steer-dropped,载荷 []string。
func (l *Loop) emitDroppedSteers(t *turn) {
	left := t.takeSteers()
	if len(left) == 0 {
		return
	}
	l.c.Emit(context.Background(), "agent/steer-dropped", left, sdk.Emit)
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
	defer runCancel()
	t := &turn{} // 回合级状态(伪调用提醒每回合至多一次;转向注入见 turn.steers)
	// 回吐:取消/失败时仍未注入的转向消息不得静默丢(见 emitDroppedSteers)
	defer l.emitDroppedSteers(t)
	var tok uint64
	if l.tc != nil { // 直接构造的 Loop(旧测试/无 turnControl 场景)跳过注册
		tok = l.tc.register(runCancel, t)
		defer l.tc.unregister(tok)
	}

	l.c.Emit(runCtx, "agent/status", "running", sdk.Emit)
	// turn/start:回合起点标记(与 turn/end 配对;此前只声明未发出,S-P0-1 轨迹视图需要
	// 权威回合边界)。nil 载荷不参与 DeriveMessages 投影,旧会话缺该帧也能正常工作。
	if err := l.appendEvents(
		sdk.SessionEvent{Kind: sdk.EventTurnStart},
		sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: input, Attachments: atts}},
	); err != nil {
		l.c.Emit(context.Background(), "agent/status", "idle", sdk.Emit)
		return fmt.Errorf("session log: %w", err)
	}
	for step := 0; l.maxSteps <= 0 || step < l.maxSteps; step++ {
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
		// 步数耗尽(仅在配了 data.max_steps > 0 时可达):此前静默记为 "done" 并返回 nil ——
		// 模型/用户都看不出"未收敛"。现显式失败,但事实不再被掩盖。
		err := fmt.Errorf("agent: 达到最大步数 %d 仍未完成(可能工具循环或模型未收敛;data.max_steps 放宽或设 0 取消上限)", l.maxSteps)
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
	// 回合已被取消:不再开新步骤(否则会消费掉待注入的插话,并写一个没有 step/end 的
	// step/start)。待注入消息留给 emitDroppedSteers 交回发起端。
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventStepStart}); err != nil {
		return fmt.Errorf("session log: %w", err)
	}

	// agent/pre-step:waterfall 扩展点(M2 无监听器则直过;改写/拒绝留 policy 阶段)
	if _, err := l.c.Emit(ctx, "agent/pre-step", nil, sdk.Waterfall); err != nil {
		return fmt.Errorf("pre-step rejected: %w", err)
	}

	// 转向注入:上一步的工具结果已落账,下一次模型请求组装之前把用户中途插进来的
	// 消息补成 EventUserMessage(见 injectSteers;必须在 assemble 之前)。
	if err := l.injectSteers(t); err != nil {
		return err
	}

	// assemble 组装本轮请求(历史投影 + 工具 schema + 伪调用提醒)。
	// 抽成函数供溢出兜底路径重新组装:强制压缩会改写投影,重试必须用压缩后的历史;
	// 提醒只注入一次(首次组装已消费 t.reminder),重试不得重复追加。
	assemble := func() *sdk.LLMRequest {
		history := l.sessions.DeriveMessages()
		tools := l.tools.List()
		messages := l.sp.Assemble(history, tools)
		// 伪调用提醒注入(上步检测到文本伪造工具调用;作为追加输入给模型修正机会)
		if t.reminder != "" {
			messages = append(messages, sdk.LLMMessage{Role: sdk.RoleUser, Content: t.reminder})
			t.reminder = ""
		}
		return &sdk.LLMRequest{Messages: messages, Tools: tools}
	}

	var (
		content strings.Builder
		calls   []sdk.ToolCall
		final   sdk.LLMResponse
	)
	onChunk := func(ev sdk.LLMStreamEvent) error {
		if ev.Delta != "" {
			content.WriteString(ev.Delta)
		}
		// 思维增量也必须落流(仅正文落流会让推理模型的 thinking 增量整段丢失 →
		// TUI 思维块 / 导出 HTML 思考块 / ACP thinking 永远为空,只剩键位可验)。
		if ev.Delta != "" || ev.Thinking != "" {
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
	req := assemble()
	resp, err := l.llm.Complete(ctx, req, onChunk)
	// 溢出兜底(第五十六批):端点报超窗时**强制压缩后重试同一回合** —— 硬上限 1 次。
	// 为何必需:阈值再准也有估偏来源(非均匀 token 分布/CJK/图片/长单轮几十个工具结果/
	// cpt 未收敛),一旦端点真的拒了,当前实现没有第二次机会 —— 用户只能自己 /compact 再重问。
	// 为何必须先压缩:原样重发只会撞同一堆墙。
	overflowHint := ""
	if err != nil && !t.overflowRetried && sdk.IsContextOverflowError(err) {
		t.overflowRetried = true // 无论折叠成败,同一回合不再试第二次(防重试环/重复计费)
		if folded, ferr := l.compressOnOverflow(); ferr == nil {
			content.Reset() // 失败尝试已落流的部分增量不得混进重试结果
			calls = nil
			final = sdk.LLMResponse{}
			overflowHint = "已自动压缩上下文后重试仍超窗"
			l.publishOverflowNotice(folded)
			req = assemble() // 折叠立即生效:重试发出去的是压缩后的历史
			resp, err = l.llm.Complete(ctx, req, onChunk)
		} else {
			overflowHint = "自动压缩不可用(" + ferr.Error() + ")"
		}
	}
	if err != nil {
		if overflowHint != "" && !errors.Is(err, context.Canceled) {
			// 如实告知已经试过什么,并给两条人话出路(不静默把失败原样丢给用户)。
			err = fmt.Errorf("%w(%s:可先 /compact,或换用窗口更大的模型)", err, overflowHint)
		}
		// 包装模型名(host-usage-stats 经 agent/error 解析错误文本学习窗口;
		// Unwrap 保留,重试/取消 errors.Is 判定不变)
		return &sdk.LLMError{Model: req.Model, Err: err}
	}
	if resp != nil {
		final = *resp
	}
	if len(calls) > 0 {
		final.Message.ToolCalls = calls
	} else if len(final.Message.ToolCalls) > 0 {
		// 适配器内部累积的 tool_calls(如 anthropic:它按 content block 累积,不发增量
		// ToolCallID 事件)。此前只认增量累积 → anthropic 侧的工具调用**永不执行**:
		// 回合被当成"无工具调用"直接结束(正文照出,工具静默不跑)。
		calls = final.Message.ToolCalls
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
		// 用户在本回合中插了话(尚未注入)→ 不收尾:下一 step 开头注入后继续本回合。
		if t.hasSteers() {
			if err := l.appendEvents(sdk.SessionEvent{Kind: sdk.EventStepEnd}); err != nil {
				return fmt.Errorf("session log: %w", err)
			}
			return nil // 不结束:下一轮带转向消息继续
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

// compressOnOverflow 走 sdk.OverflowCompactor(host-session-log 实现):强制压缩一次。
// 未实现(旧装配/纯内存日志)⇒ 如实返回错误,让调用方把原因写进失败文案 ——
// 不能静默假装压过(那会让用户以为“已经帮我压了”而实际什么都没做)。
func (l *Loop) compressOnOverflow() (int, error) {
	oc, ok := l.sessions.(sdk.OverflowCompactor)
	if !ok {
		return 0, errors.New("会话日志未提供溢出压缩能力")
	}
	return oc.CompressForOverflow()
}

// publishOverflowNotice 把“已自动压缩后重试”写进提示通道(ctx.notices 未装配则跳过)。
// 级别 info:自动恢复的好消息,不该打断人(桌面壳只对 warn/error 弹通知)。
func (l *Loop) publishOverflowNotice(folded int) {
	if l.notices == nil {
		return
	}
	body := "模型端点报上下文超窗,已压缩历史后重试本回合"
	if folded > 0 {
		body += fmt.Sprintf("(折叠 %d 帧为摘要)", folded)
	}
	l.notices.Publish(sdk.Notice{Level: sdk.NoticeInfo, Source: "host-agent-loop", Title: "已自动压缩上下文", Body: body})
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

// maxStepsFromManifest 读插件 data.max_steps(缺省 maxStepsDefault = 不限)。
// 只认 yaml 解析出的 int;写错类型(如字符串)按缺省处理并留日志级别的事实即可 ——
// 这里不静默降级为"某个神秘上限":数据不对就等于没配。
func maxStepsFromManifest(m *sdk.Manifest) int {
	if m == nil {
		return maxStepsDefault
	}
	switch v := m.Data["max_steps"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return maxStepsDefault
}
