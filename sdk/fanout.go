// 子代理编排服务(host-fanout,M6.2 拆出):独立上下文 ReAct 编排。
package sdk

import (
	"context"
	"time"
)

// FanoutResult 一次子代理运行结果(input + result|error)。
type FanoutResult struct {
	Input  string
	Result string
	Error  string
}

// AgentState 后台子代理会话状态。
type AgentState string

const (
	AgentRunning AgentState = "running"
	AgentDone    AgentState = "done"
	AgentFailed  AgentState = "failed"
	AgentKilled  AgentState = "killed"
)

// AgentMessage 父子代理对话记录(send_message 注入 + 子代理回复;经 AgentStatus 可读)。
type AgentMessage struct {
	From    string `json:"from"` // user = 父级注入;agent = 子代理回复
	Content string `json:"content"`
}

// AgentHandle 后台子代理会话句柄(M9.2:delegate 后台带手柄,轮询取状态/结果;
// M9.3:Messages 记录 send_message 注入与子代理回复的对话)。
type AgentHandle struct {
	ID        string         `json:"id"`
	Input     string         `json:"input"`
	State     AgentState     `json:"state"`
	Result    string         `json:"result,omitempty"`
	Error     string         `json:"error,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	Messages  []AgentMessage `json:"messages,omitempty"`
	// Worktree 隔离运行的工作区(S-P1-4;非隔离运行 = nil)。路径/分支供父级合并与回收。
	Worktree *Worktree `json:"worktree,omitempty"`
}

// WorktreeRun 隔离运行请求(S-P1-4)。
type WorktreeRun struct {
	Input string // 子代理任务(必填)
	Fork  bool   // true = 种入父会话已发生的历史(对齐 Fork)
	Sync  bool   // true = 同步等子代理完成并返回文本(对齐 Agent/delegate)
	Label string // worktree 标签(空 = 实现自取;仅标识/展示)
}

// WorktreeRunResult 隔离运行结果。两种模式都回传 Worktree(路径/分支必须能被父级看到 ——
// 否则改动“消失了”:既不在主工作区,也无从合并)。
type WorktreeRunResult struct {
	Worktree Worktree    // 本次运行的工作区
	Text     string      // Sync=true:子代理最终文本
	Handle   AgentHandle // Sync=false:后台句柄(已含 Worktree)
}

// IsolatedFanout 可选能力(ctx.fanout 的扩展):在受管 git worktree 内隔离运行子代理。
// 未实现 = 宿主不支持隔离:消费方必须**显式报错**,不得静默退回非隔离运行
// (静默降级 = 用户以为隔离了而实际没有,比不支持更糟)。
type IsolatedFanout interface {
	RunInWorktree(ctx context.Context, req WorktreeRun) (WorktreeRunResult, error)
}

// FanoutService 服务(ctx.fanout):子代理编排(独立会话历史,不写主会话)。
// 复用 ctx.llm/ctx.tools/ctx.systemPrompt;宿主级服务,可由任意入口复用
// (tool-workflow 的 starlark 内建函数仅是其中一个消费方)。
type FanoutService interface {
	// Agent 单子代理一轮 ReAct:独立历史,返回最终 assistant 文本。
	Agent(ctx context.Context, input string) (string, error)
	// Parallel 并发扇出多个子代理并聚合(顺序与 inputs 对应,每项含 input/result|error)。
	Parallel(ctx context.Context, inputs []string) []FanoutResult
	// Pipeline 串行链:上一步输出作为下一步输入;返回每步结果与最终输出。
	Pipeline(ctx context.Context, steps []string) ([]FanoutResult, string, error)
	// SpawnAgent 后台启动单子代理(不阻塞):返回句柄 id,轮询 ListAgents/AgentHandle。
	SpawnAgent(ctx context.Context, input string) (string, error)
	// Fork 派生带父上下文的子代理(M9.3):初始消息历史 = 父会话已投影历史(DeriveMessages)
	// + input;后台启动返回句柄 id(轮询取状态/结果)。
	Fork(ctx context.Context, input string) (string, error)
	// SendMessage 向运行中的后台子代理注入一条消息(M9.3):追加为 user 输入,
	// 子代理继续执行并回复(对话记录经 AgentStatus.Messages 可读)。非 running 报错。
	SendMessage(id, message string) error
	// ListAgents 全部后台子代理会话(含历史,末位最新)。
	ListAgents() []AgentHandle
	// AgentStatus 取单个会话状态(结果/错误/消息对话)。
	AgentStatus(id string) (AgentHandle, bool)
	// KillAgent 终止运行中的子代理(killed 状态;已完成返回错误)。
	KillAgent(id string) error
}
