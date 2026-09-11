// AgentLoop 接口与系统提示服务。
package sdk

import "context"

// AgentLoop 是可替换的 agent 循环(默认实现由 host-agent-loop 提供,对齐 dsh `ctx.agentLoop`)。
// 宿主不内置任何循环语义;换实现 = 换插件。
type AgentLoop interface {
	// Run 处理一次用户输入(可含多步 ReAct 迭代),直至完成一轮。
	// 输入与输出均经会话日志记录(不变量:模型可见即已记录)。
	Run(ctx context.Context, input string) error
}

// AttachmentInput 可选扩展:AgentLoop 实现时可接收附件(Web 附件一期)。
// 未实现该接口的循环经类型断言失败回落 Run(附件以 [附件] 文本引用随 content 保留)。
type AttachmentInput interface {
	RunWithAttachments(ctx context.Context, input string, atts []Attachment) error
}

// SystemPrompt 片段:命名 + 内容提供器(内容可引用 ctx 动态组装)。
type SystemPromptSection struct {
	Name    string
	Content func() string
}

// SystemPromptService 服务(ctx.systemPrompt):片段注册 + 组装模型可见消息。
type SystemPromptService interface {
	// AddSection 注册一个系统提示片段,返回 Disposer。
	AddSection(s SystemPromptSection) Disposer
	// Assemble 组装 system 消息:固定引导 + 注册片段 + 工具 schema 清单 + 历史消息。
	Assemble(history []LLMMessage, tools []ToolDefinition) []LLMMessage
}

// TurnControl 服务(ctx.turnControl):回合运行控制(/stop 命令、Web 取消、TUI Esc 共用)。
// 由 host-agent-loop 提供:每次 Run 内部派生可取消 ctx 并注册,回合结束自动注销。
// 实现必须并发安全(允许多回合并发注册,各自独立取消)。
type TurnControl interface {
	// Running 是否存在运行中的回合。
	Running() bool
	// Cancel 取消全部运行中的回合(无运行回合 = no-op;幂等,重复调用无害)。
	Cancel()
}
