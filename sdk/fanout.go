// 子代理编排服务(host-fanout,M6.2 拆出):独立上下文 ReAct 编排。
package sdk

import "context"

// FanoutResult 一次子代理运行结果(input + result|error)。
type FanoutResult struct {
	Input  string
	Result string
	Error  string
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
}
