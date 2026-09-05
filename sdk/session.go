// 会话事件与 SessionLog 服务(对齐设计 §9:会话日志 = 追加式事件流,不变量“模型可见即已记录”)。
package sdk

import "time"

// 持久会话事件 Kind(对齐 dsh 轮次流程的事件域)。
const (
	// EventSession 广播:每次会话事件 Append 后发出(UI/遥测实时订阅;对齐 dsh session/event)。
	EventSession          = "session/event"
	EventTurnStart        = "turn/start"
	EventTurnEnd          = "turn/end"
	EventStepStart        = "step/start"
	EventStepEnd          = "step/end"
	EventUserMessage      = "user/message"
	EventAssistantChunk   = "assistant/chunk"
	EventAssistantMessage = "assistant/message"
	EventToolCall         = "tool/call"
	EventToolResult       = "tool/result"
	// EventSummary 滚动摘要事件(M6.5):载荷为累计摘要文本;原始消息事件保留在日志(留盘完整)。
	EventSummary = "session/summary"
	// EventUsage 每轮 LLM 请求完成后的 token 消耗(M):载荷为 sdk.UsageEvent(模型名 + Usage);
	// agent-loop 每轮记录(同日志留盘),host-usage-stats 订阅累计为会话级统计。
	EventUsage       = "session/usage"
	EventAgentStatus = "agent/status"
	EventAgentError  = "agent/error"
)

// SessionEvent 是追加到会话日志的持久事实。
type SessionEvent struct {
	Kind    string
	Seq     uint64
	Payload any
	TS      time.Time
}

// UserMessage 用户输入(user/message 载荷)。
type UserMessage struct {
	Content string
}

// AssistantMessage 助手完整消息(assistant/message 载荷;chunk 事件只携带增量)。
type AssistantMessage struct {
	Content   string
	ToolCalls []ToolCall
}

// ToolCallEvent 工具调用记录(tool/call 载荷)。
type ToolCallEvent struct {
	ID        string
	Name      string
	Arguments string
}

// ToolResultEvent 工具结果记录(tool/result 载荷)。
type ToolResultEvent struct {
	CallID  string
	Name    string
	Content string // 序列化后的结果/错误
	Error   string
}

// SessionLog 服务(ctx.sessions):追加事件 + 投影模型历史。
// 投影不变量:derive 出的消息必须能从日志重建(即模型可见 = 已记录)。
type SessionLog interface {
	Append(ev SessionEvent) error
	// DeriveMessages 从事件日志投影模型可用的历史消息。
	DeriveMessages() []LLMMessage
	// Replay 全量回放事件(供 fork/导出/UI)。
	Replay() []SessionEvent
	// Flush 落盘(内存会话为 no-op)。
	Flush() error

	// SetPath 设置会话落盘路径(jsonl;host-cwd-sessions 按项目 key 调用)。
	SetPath(path string)

	// Load 切换到指定会话:关闭当前落盘文件,清空内存事件,
	// 读入该路径 jsonl 已有事件(容忍坏行)并恢复序号(seq 接续)。
	// 文件不存在 = 空会话(新建);path 空 = 纯内存会话。
	Load(path string) error

	// SetHistory 设置历史注入条数:-1 = 禁止注入;0 = 全部(unlimited);N>0 = 最近 N 条。
	// 对齐设计 §9:history injection(默认 unlimited)。
	SetHistory(n int)

	// RegisterCompressor 注册滚动摘要压缩器与其字符预算(M6.5 拆分后由 token-compress 注入)。
	// budget <= 0 关闭压缩;压缩器在投影超预算时被调用(详见 SessionCompressor)。
	RegisterCompressor(budget int, c SessionCompressor)
}

// SessionCompressor 滚动摘要引擎(M6.5 拆出 token-compress;仅消费 SessionEvent,零内部状态)。
// host-session-log 在投影超预算时回调 Fold;引擎折叠事件流最旧块为累计摘要,
// 每折一块调用 summary 回调持久化 session/summary 事件;host 据此推进水位(投影跳过已压缩块)。
type SessionCompressor interface {
	// Fold 折叠 evs 中水位后的最旧块(不得越过最后一个用户轮),
	// 直至估算投影回预算内或无可折叠;返回已被摘要覆盖的最大事件索引(水位)。
	// watermark -1 表示尚未压缩;summary 回调幂等可多次调用。
	Fold(evs []SessionEvent, watermark int, budget int, summary func(string)) int
}

// UsageEvent 一轮 LLM 请求的 token 消耗(session/usage 载荷):模型名 + Usage。
// 模型名供 host-usage-stats 按内置窗口表解析上下文总量(不同模型窗口差异大,
// 单值默认过粗暴;模型切换后随事件自动更新)。
type UsageEvent struct {
	Model string
	Usage Usage
}

// SessionInfo 一个会话的元信息(host-cwd-sessions 列表/切换用)。
// ID 空 = 主会话(<key>.jsonl,跨期共享历史);非空 = 切换会话(<key>-<id>.jsonl)。
type SessionInfo struct {
	ID     string // 会话 id(空 = 主会话)
	Path   string // 落盘 jsonl 路径
	Name   string // 显示名(/name 设置;空 = 未命名)
	MTime  int64  // 最后修改时间(unix 秒;0 = 未知/未落盘)
	Frames int    // 事件条数(-1 = 未统计)
}

// CwdSessions 服务(ctx.cwdSessions):项目级会话(host-cwd-sessions)。
type CwdSessions interface {
	// Current 当前项目会话 key(由 cwd 派生,同项目跨期共享)。
	Current() string
	// Path 当前会话落盘路径。
	Path() string
	// List 列出项目会话 key(按名称;含历史项目)。
	List() []string
	// Sessions 当前项目的会话列表(主会话 + 已切换会话;按最后修改时间倒序)。
	Sessions() []SessionInfo
	// Open 切换当前会话:载入 id 对应文件的历史并设为落盘目标。
	// id 空 = 主会话;文件不存在 = 新建会话(空历史,继续从头记)。
	Open(id string) error
	// CurrentSession 当前会话 id(空 = 主会话)。
	CurrentSession() string
	// Rename 设置当前会话显示名(空 = 清除)。名随会话文件持久化,
	// 状态栏/会话列表/切换选择器以名为优先展示,无名称回退 id/主会话。
	Rename(name string) error
	// SessionName 当前会话显示名(空 = 未命名)。
	SessionName() string
	// New 新建会话:生成唯一 id 并 Open,返回新会话 id。
	New() (string, error)
	// SwitchProject 切换当前项目:key = 新项目 key(cwd 派生),重绑后自动新建
	// 空会话(当前上下文与后续记录切到新项目文件;旧项目历史经 List/Sessions 回溯)。
	// 返回新会话 id。key 空 = default。
	SwitchProject(key string) (string, error)
	// RecentProjects 最近使用工作区(项目)列表,按最近使用时间倒序(TUI /workspace 选择)。
	RecentProjects() []ProjectInfo
}

// ProjectInfo 一条工作区(项目)使用记录:key(cwd 派生)与真实目录、最近使用时间。
type ProjectInfo struct {
	Key string `json:"key"`
	Dir string `json:"dir"`
	TS  int64  `json:"ts"` // 最近使用 unix 秒
}
