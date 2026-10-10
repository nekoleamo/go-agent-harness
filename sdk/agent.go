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

// SectionSlot 系统提示片段的位置槽(决定片段在组装里的渲染位置)。
// 零值 = SlotDefault,即既有行为(向后兼容:老代码不写 Slot 照样编译且位置不变)。
type SectionSlot int

const (
	// SlotDefault 默认槽:排在「指令层」之后(项目/附加指令 → 各片段 → 工具名清单)。
	SlotDefault SectionSlot = iota
	// SlotIdentity 身份槽:排在固定引导之后、指令层之前 —— 回答「你是谁」。
	// 用途:角色(persona/role)替换身份句;安全规则仍在固定引导里,**身份槽抹不掉它**。
	SlotIdentity
)

// SystemPrompt 片段:命名 + 内容提供器(内容可引用 ctx 动态组装)。
// Content 在**每次组装**时求值 —— 动态状态(如当前角色)因此无需重建服务即生效。
type SystemPromptSection struct {
	Name    string
	Content func() string
	Slot    SectionSlot // 位置槽(零值 = SlotDefault)
}

// SystemPromptService 服务(ctx.systemPrompt):片段注册 + 组装模型可见消息。
type SystemPromptService interface {
	// AddSection 注册一个系统提示片段,返回 Disposer。
	AddSection(s SystemPromptSection) Disposer
	// Assemble 组装 system 消息:固定引导 + 注册片段 + 工具 schema 清单 + 历史消息。
	Assemble(history []LLMMessage, tools []ToolDefinition) []LLMMessage
}

// PromptPart 系统提示一个组成块的体积(诊断用,不含 token 语义)。
type PromptPart struct {
	Label string // 块名(如「全局指令(用户级 AGENTS.md)」「片段 skills」)
	Chars int    // 字符数(rune)
	Bytes int    // 字节数(UTF-8);与 Chars 联合可区分宽字符与 ASCII
}

// SystemPromptInspector 可选扩展:系统提示服务额外实现时,可给出组成分解。
// 供 /context 做本地上限估算(S-P0-4);未实现时调用方回退为「仅总量」。
// 只读诊断:不得改变组装语义,也不得发模型请求。
type SystemPromptInspector interface {
	// Breakdown 返回各组成块体积,顺序与 Assemble 组装顺序一致
	// (引导 → 指令文件各级 → 注册片段 → 工具名清单)。
	Breakdown(tools []ToolDefinition) []PromptPart
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

// TurnSteerer 可选扩展:ctx.turnControl 的实现者额外支持「回合运行中注入一条用户消息」
// (steering:消息在下一次模型请求组装之前参与**当前**回合,而不是排到本回合之后)。
// 未实现该接口时调用方各自回落旧行为(TUI 入队、Web 409),不静默降级。
// 回合结束(完成/取消/失败)时仍未注入的消息经事件 "agent/steer-dropped" 交回发起端。
type TurnSteerer interface {
	// Steer 把 text 注入当前运行中的回合。
	// 返回 false = 当前无运行回合(调用方自行回落:排队或拒绝),此时 err 为 nil。
	// err 非空 = 已受理但未能投递/落账,调用方按「一条不丢」处理(回落排队)。
	Steer(text string) (bool, error)
}

// TurnSteererSession 可选扩展:TurnSteerer 的**按会话定向**变体。
//
// 为何不给 Steer 加参数:那是公共签名,加参数会破坏所有实现与外部插件;而「这次转向
// 属于哪个会话」是按需才有的维度。消费方(web)优先用本接口,拿不到再回落 TurnSteerer。
//
// 为什么必须有它:多会话并行时闸门按会话判「运行中」,而按「最近注册的回合」投递 ——
// 两把锁的键不是同一个,会话 A 的插话会被写进会话 B 的回合(由 B 落账进 B 的日志),
// 内容静默错位。sessionID 空串 = 主会话(与 ctx.turnControl 的归一键同一语义)。
type TurnSteererSession interface {
	// SteerSession 把 text 注入**指定会话**运行中的回合(该会话无回合 → false,调用方回落)。
	SteerSession(sessionID, text string) (bool, error)
}

// ContextualSystemPrompt 可选扩展(宿主默认实现支持;外部实现可不给)。
//
// 为什么不给 Assemble 直接加 ctx 参数:那是公共签名,加参数会破坏所有实现与外部插件;
// 而"这次组装属于哪个会话"是**按需才有**的上下文(无会话 = 旧行为)。
// 消费方(agent-loop)优先用它,拿不到就回落无参 Assemble —— 行为逐字不变。
type ContextualSystemPrompt interface {
	// AssembleFor 与 Assemble 同义,但能按调用所属会话解析角色相关的注入
	// (身份槽之外的:是否注入全局指令、可用技能片段)。
	AssembleFor(ctx context.Context, history []LLMMessage, tools []ToolDefinition) []LLMMessage
}

// SessionRunner 可选扩展:AgentLoop 支持**按会话**执行回合(实例内多会话并行)。
//
// 背景:默认实现把 ctx.sessions(单例)当当前会话写,SetPath/Load 是「切换」语义 ——
// 两个会话并发跑回合会争同一份内存事件与同一个文件句柄(追加与投影互相交错)。
// 逐会话实例化(经 ctx.sessionDir 取日志)之后,不同会话的回合才能真正并行;
// **同一会话内仍然串行**(单写者不变量不变)。
//
// 未实现时:调用方必须回落旧行为(主回路合串行 + 409 拒收),不静默「看起来并行」。
// 能力探测用类型断言;web 侧写侧闸门也看这个接口决定能否放行(见 web/session_scope.go)。
type SessionRunner interface {
	// RunInSession 在指定会话执行一轮(sessionID 空 = 当前主会话,与 Run 等价)。
	RunInSession(ctx context.Context, sessionID, input string) error
	// CancelSession 取消该会话正在跑的回合;返回是否命中(无在跑回合 = false)。
	CancelSession(sessionID string) bool
	// RunningSessions 当前有回合在跑的会话 id(空串 = 主会话);诊断与多窗口展示用。
	RunningSessions() []string
}
