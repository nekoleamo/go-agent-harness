// Package hostfanout 提供 host-fanout 插件:ctx.fanout 子代理编排服务(M6.2 拆分)。
// 从 tool-workflow 拆出的宿主级能力:独立上下文 ReAct(独立会话历史,不写主会话),
// 复用 ctx.llm/ctx.tools/ctx.systemPrompt;agent/parallel/pipeline 编排可由任意入口复用
// (tool-workflow 的 starlark 内建函数仅是消费方之一;未来子代理 fanout 完善在此演进)。
// 红线遵循:只 import sdk,依赖经 Ctx 注入。
package hostfanout

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-fanout。requires ctx.llm/ctx.tools/ctx.systemPrompt。
type Plugin struct{}

// Name 返回插件 id。
func (p *Plugin) Name() string { return "host-fanout" }

// Start 注入依赖并注册 ctx.fanout 服务。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var (
		tools sdk.ToolRegistry
		llm   sdk.LLMService
		sp    sdk.SystemPromptService
	)
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	_ = c.Inject("ctx.llm", &llm)         // 未装配时子代理调用显式报错
	_ = c.Inject("ctx.systemPrompt", &sp) // 同上
	var sessions sdk.SessionLog
	_ = c.Inject("ctx.sessions", &sessions) // fork 需要(ctx.sessions 未装配时 Fork 显式报错)
	f := &Fanout{tools: tools, llm: llm, sp: sp, sessions: sessions, agents: map[string]*agentSession{}}
	if err := c.Provide("ctx.fanout", f); err != nil {
		return nil, err
	}
	return func() {}, nil
}

// Fanout 子代理编排实现。
type Fanout struct {
	tools    sdk.ToolRegistry
	llm      sdk.LLMService
	sp       sdk.SystemPromptService
	sessions sdk.SessionLog // M9.3 fork 的父上下文来源(可 nil → Fork 显式报错)

	// M9.2 后台子代理会话控制:agents 表 + 序号。会话独立上下文(Background + cancel),
	// 不随发起方 ctx 取消;由 KillAgent/宿主 shutdown 显式终止。
	mu     sync.Mutex
	agents map[string]*agentSession
	seq    int
}

// agentSession 一个后台子代理会话。
type agentSession struct {
	handle sdk.AgentHandle
	cancel context.CancelFunc
	done   chan struct{}
	// M9.3 send_message/fork:
	// inbox 父级注入消息通道(运行循环每步 drain);seed 为 fork 种入的父会话历史;
	// dialog 为注入消息与子代理回复的对话记录(经 snapshot 附着到 handle.Messages)。
	inbox  chan string
	seed   []sdk.LLMMessage
	dialog []sdk.AgentMessage
}

// inboxCap 注入消息队列容量(运行循环每步 drain,32 已宽裕)。
const inboxCap = 32

// maxSubSteps 子代理单轮最大 ReAct 迭代(防死循环)。
const maxSubSteps = 8

// SpawnAgent 后台启动单子代理(不阻塞):立即返回句柄 id;子代理在独立上下文运行,
// 完成/失败/被终止后状态经 ListAgents/AgentStatus 可取(轮询)。
func (f *Fanout) SpawnAgent(_ context.Context, input string) (string, error) {
	return f.spawnBackground(input, false)
}

// Fork 派生带父上下文的子代理(M9.3):初始消息历史 = 父会话已投影历史
// (ctx.sessions.DeriveMessages)+ input;后台启动返回句柄 id。
func (f *Fanout) Fork(_ context.Context, input string) (string, error) {
	return f.spawnBackground(input, true)
}

// spawnBackground 后台启动公共路径:forkSeed=true 时种入父会话历史。
func (f *Fanout) spawnBackground(input string, forkSeed bool) (string, error) {
	if f.llm == nil || f.sp == nil {
		return "", fmt.Errorf("子代理编排需要 ctx.llm / ctx.systemPrompt(未装配)")
	}
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("子代理任务为空")
	}
	var seed []sdk.LLMMessage
	if forkSeed {
		if f.sessions == nil {
			return "", fmt.Errorf("fork 需要 ctx.sessions(host-session-log 未装配)")
		}
		seed = f.sessions.DeriveMessages()
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.mu.Lock()
	f.seq++
	id := fmt.Sprintf("ag%d", f.seq)
	f.agents[id] = &agentSession{
		handle: sdk.AgentHandle{ID: id, Input: strings.TrimSpace(input), State: sdk.AgentRunning,
			CreatedAt: time.Now()},
		cancel: cancel,
		done:   make(chan struct{}),
		inbox:  make(chan string, inboxCap),
		seed:   seed,
	}
	f.mu.Unlock()
	go func() {
		result, err := f.runAgentLoop(ctx, input, f.agents[id])
		f.mu.Lock()
		defer f.mu.Unlock()
		ag := f.agents[id]
		if ctx.Err() != nil {
			ag.handle.State = sdk.AgentKilled
			ag.handle.Error = "任务被终止"
		} else if err != nil {
			ag.handle.State = sdk.AgentFailed
			ag.handle.Error = err.Error()
		} else {
			ag.handle.State = sdk.AgentDone
			ag.handle.Result = result
		}
		close(ag.done)
	}()
	return id, nil
}

// SendMessage 向运行中的后台子代理注入一条消息(M9.3):非阻塞投递到 inbox,
// 子代理运行循环下一轮收到并继续。非 running 会话显式报错。
func (f *Fanout) SendMessage(id, message string) error {
	if strings.TrimSpace(message) == "" {
		return fmt.Errorf("注入消息为空")
	}
	f.mu.Lock()
	ag, ok := f.agents[id]
	var state sdk.AgentState
	if ok {
		state = ag.handle.State
	}
	f.mu.Unlock()
	if !ok {
		return fmt.Errorf("子代理会话不存在 %q", id)
	}
	switch state {
	case sdk.AgentRunning:
		select {
		case ag.inbox <- message:
			return nil
		default:
			return fmt.Errorf("子代理 %q 注入队列已满(运行循环未及时消费)", id)
		}
	case sdk.AgentDone:
		return fmt.Errorf("子代理会话 %q 已完成,不再接收消息", id)
	case sdk.AgentFailed:
		return fmt.Errorf("子代理会话 %q 已失败,不再接收消息", id)
	case sdk.AgentKilled:
		return fmt.Errorf("子代理会话 %q 已终止,不再接收消息", id)
	}
	return fmt.Errorf("子代理会话 %q 状态未知", id)
}

// logDialog 记录父子代理对话一条(f.mu 保护;snapshot 同理锁内读)。
func (f *Fanout) logDialog(ag *agentSession, from, content string) {
	f.mu.Lock()
	ag.dialog = append(ag.dialog, sdk.AgentMessage{From: from, Content: content})
	f.mu.Unlock()
}

// snapshot 组装对外句柄(锁内拷贝 dialog 为 Messages)。
func (f *Fanout) snapshot(ag *agentSession) sdk.AgentHandle {
	h := ag.handle
	h.Messages = append([]sdk.AgentMessage(nil), ag.dialog...)
	return h
}

// ListAgents 全部后台子代理会话(末位最新)。
func (f *Fanout) ListAgents() []sdk.AgentHandle {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]sdk.AgentHandle, 0, len(f.agents))
	for _, ag := range f.agents {
		out = append(out, f.snapshot(ag))
	}
	return out
}

// AgentStatus 取单个会话状态(含 send_message 对话记录)。
func (f *Fanout) AgentStatus(id string) (sdk.AgentHandle, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ag, ok := f.agents[id]
	if !ok {
		return sdk.AgentHandle{}, false
	}
	return f.snapshot(ag), true
}

// KillAgent 终止运行中的子代理(killed 状态)。已完成任务返回错误。
func (f *Fanout) KillAgent(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	ag, ok := f.agents[id]
	if !ok {
		return fmt.Errorf("子代理会话不存在 %q", id)
	}
	switch ag.handle.State {
	case sdk.AgentRunning:
		ag.handle.State = sdk.AgentKilled
		ag.cancel() // 独立 ctx 取消 → runSubAgent 退出,goroutine 结束
		return nil
	case sdk.AgentDone, sdk.AgentFailed:
		return fmt.Errorf("子代理会话 %q 已结束(%s)", id, ag.handle.State)
	case sdk.AgentKilled:
		return fmt.Errorf("子代理会话 %q 已终止", id)
	}
	return nil
}

// Agent 单子代理一轮 ReAct:独立历史(不写主会话),返回最终 assistant 文本。
func (f *Fanout) Agent(ctx context.Context, input string) (string, error) {
	return f.runSubAgent(ctx, input)
}

// Parallel 并发扇出多个子代理并聚合(顺序与 inputs 对应)。
func (f *Fanout) Parallel(ctx context.Context, inputs []string) []sdk.FanoutResult {
	results := make([]sdk.FanoutResult, len(inputs))
	var wg sync.WaitGroup
	for i, in := range inputs {
		wg.Add(1)
		go func(i int, in string) {
			defer wg.Done()
			item := sdk.FanoutResult{Input: in}
			res, err := f.runSubAgent(ctx, in)
			if err != nil {
				item.Error = err.Error()
			} else {
				item.Result = res
			}
			results[i] = item
		}(i, in)
	}
	wg.Wait()
	return results
}

// Pipeline 串行链:上一步输出作为下一步输入;返回每步结果与最终输出。
func (f *Fanout) Pipeline(ctx context.Context, steps []string) ([]sdk.FanoutResult, string, error) {
	var stepsOut []sdk.FanoutResult
	result := ""
	for _, in := range steps {
		cur := in
		if result != "" {
			cur = result // 上一步输出作为下一步输入
		}
		res, err := f.runSubAgent(ctx, cur)
		step := sdk.FanoutResult{Input: cur}
		if err != nil {
			step.Error = err.Error()
			stepsOut = append(stepsOut, step)
			return stepsOut, "", err
		}
		step.Result = res
		stepsOut = append(stepsOut, step)
		result = res
	}
	return stepsOut, result, nil
}

// findCall 按 ToolCallID 定位或追加(流式增量聚合,同 host-agent-loop 语义)。
func findCall(calls *[]sdk.ToolCall, id string) int {
	for i := range *calls {
		if (*calls)[i].ID == id {
			return i
		}
	}
	*calls = append(*calls, sdk.ToolCall{ID: id})
	return len(*calls) - 1
}

// runSubAgent 同步路径:独立上下文跑一轮 ReAct(复用 ctx.llm/ctx.tools/ctx.systemPrompt);
// 工具调用经全流水线执行(策略拦截经 tools/pre-execute 生效)。
func (f *Fanout) runSubAgent(ctx context.Context, input string) (string, error) {
	return f.runAgentLoop(ctx, input, nil)
}

// runAgentLoop 核心循环:ag 非 nil 时(后台会话)每步开头 drain inbox(send_message
// 注入→追加 user 输入继续执行)并记录父子对话;ag.seed 为 fork 种入的父会话历史。
func (f *Fanout) runAgentLoop(ctx context.Context, input string, ag *agentSession) (string, error) {
	if f.llm == nil || f.sp == nil {
		return "", fmt.Errorf("子代理编排需要 ctx.llm / ctx.systemPrompt(未装配)")
	}
	var history []sdk.LLMMessage
	if ag != nil && len(ag.seed) > 0 {
		history = append(append([]sdk.LLMMessage{}, ag.seed...),
			sdk.LLMMessage{Role: sdk.RoleUser, Content: input})
	} else {
		history = []sdk.LLMMessage{{Role: sdk.RoleUser, Content: input}}
	}
	pendingReply := false // 注入消息后等待的首个文本回复(记录为 dialog agent 侧)
	for step := 0; step < maxSubSteps; step++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if ag != nil {
		drainLoop:
			for {
				select {
				case msg := <-ag.inbox:
					history = append(history, sdk.LLMMessage{Role: sdk.RoleUser, Content: msg})
					f.logDialog(ag, "user", msg)
					pendingReply = true
				default:
					break drainLoop
				}
			}
		}
		messages := f.sp.Assemble(history, f.tools.List())
		var (
			content strings.Builder
			calls   []sdk.ToolCall
			final   sdk.LLMResponse
		)
		onChunk := func(ev sdk.LLMStreamEvent) error {
			if ev.Delta != "" {
				content.WriteString(ev.Delta)
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
		resp, err := f.llm.Complete(ctx, &sdk.LLMRequest{Messages: messages}, onChunk)
		if err != nil {
			return "", err
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
		history = append(history, final.Message)
		// 注入消息后的首个文本回复记入 dialog(带工具调用的轮次不记,工具结果非面向父级回复)
		if ag != nil && pendingReply && final.Message.Content != "" {
			f.logDialog(ag, "agent", final.Message.Content)
			pendingReply = false
		}
		if len(calls) == 0 {
			return final.Message.Content, nil
		}
		for _, call := range calls {
			if call.Name == "workflow" || call.Name == "workflow_collect" {
				history = append(history, sdk.LLMMessage{Role: sdk.RoleTool,
					Content: "子代理不允许嵌套调用 " + call.Name, ToolCallID: call.ID})
				continue
			}
			res, err := f.tools.Execute(ctx, call.Name, call.Arguments)
			if err != nil {
				history = append(history, sdk.LLMMessage{Role: sdk.RoleTool, Content: "错误: " + err.Error(), ToolCallID: call.ID})
			} else if res != nil {
				content := res.Content
				if res.Error != "" {
					content = "错误: " + res.Error
				}
				history = append(history, sdk.LLMMessage{Role: sdk.RoleTool, Content: content, ToolCallID: call.ID})
			}
		}
	}
	return "", fmt.Errorf("子代理超过 %d 步未收敛", maxSubSteps)
}
