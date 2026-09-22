// 会话事件与 SessionLog 服务(对齐设计 §9:会话日志 = 追加式事件流,不变量“模型可见即已记录”)。
package sdk

import (
	"context"
	"time"
)

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
	// EventUsageWindow 当前模型上下文窗口快照(载荷 int,单位 token;0 = 未知)。
	// 由 host-usage-stats 在每次 usage 事件后广播:窗口解析链(context_window 配置 > 按模型名
	// 前缀覆盖 > 错误驱动学习 > 内置表)只在那里;消费方(压缩阈值按窗口比例派生)在**运行期**
	// 取这个值,而非 Start 期 Inject 服务 —— 服务在装配顺序上拿不到(bundle 里 host-session-log
	// 排在 host-usage-stats 之前,Provide/Inject 无晚绑定)。
	EventUsageWindow = "usage/window"
	// EventJobDone 后台任务终态事件(host-jobs 终态 done/failed/killed 发出;载荷 *sdk.JobDoneEvent)。
	// 订阅方可主动通知(Web 推送)或触发联动;output/result 经 ctx.jobs.Output 取回,不进载荷。
	EventJobDone = "job/done"
	// EventDocOpen 文档预览意图事件(D 组文档预览):载荷 sdk.DocOpenEvent{Path, Page, Sheet}。
	// 模型 doc_open 工具 / `/preview` 命令发出,各端 UI(TUI/Web)订阅后本地打开预览——
	// 对齐既有交互事件化先例(confirm/question requested↔resolved):只读观察面 + 各端 presenter。
	EventDocOpen = "doc/open"
	// EventFileChange 文件改动事件(S-P1-1 变更审查面):载荷 FileChangeEvent。
	// 工具写盘成功后由工具自身追加(经 ctx.sessions):审计信息属会话事实 → 落账本 + 广播,
	// 三端(TUI/Web/headless)与断线重放同源同全。
	// 纪律:diff 来自**捕获的写操作**(写盘前后自取),不伪造 git HEAD、不读 git 工作区 ——
	// 工作区无 git 仓库时同样可用,也不会把用户未提交的改动混进来。
	EventFileChange = "file/change"
	// EventDiffOpen 变更审查意图(S-P1-1):`/diff [路径]` 命令发出。
	// 载荷 DiffOpenEvent:Path 空 = 只请求打开审查视图(清单);Path 非空 = 定位到该文件。
	// TUI 订阅后弹 pager 浮层,Web 订阅后切到变更视图 —— 命令不解锁任何能力,只表达意图。
	EventDiffOpen = "diff/open"
)

// FileChangeEvent 一次文件改动事实(EventFileChange 载荷)。
// 一次工具调用产生一条;同一文件多次改动 = 多条事件(审查视图按路径聚合)。
type FileChangeEvent struct {
	Path string `json:"path"`           // 写入目标(绝对路径;运行时定位用)
	Rel  string `json:"rel,omitempty"`  // 相对工作区路径(展示/聚合主键;取不到回退 Path)
	Op   string `json:"op"`             // write|append|edit
	Tool string `json:"tool,omitempty"` // 工具名(file_write/file_append/file_edit)

	Added   int  `json:"added"`             // 新增行数
	Removed int  `json:"removed"`           // 删除行数
	Created bool `json:"created,omitempty"` // 目标原不存在(新建文件)
	Binary  bool `json:"binary,omitempty"`  // 二进制/无逐行 diff(只记统计)
	Bytes   int  `json:"bytes,omitempty"`   // 写后文件字节数

	// Diff unified diff 正文(无文件头,从 @@ 起;Binary 或超预算时为空/截断)
	Diff      string `json:"diff,omitempty"`
	Truncated bool   `json:"truncated,omitempty"` // Diff 超预算已截断
	Coarse    bool   `json:"coarse,omitempty"`    // 差异段过大 → 整段替换(未逐行对齐)
}

// DiffOpenEvent 变更审查意图载荷(EventDiffOpen)。
type DiffOpenEvent struct {
	Path      string `json:"path,omitempty"`      // 定位到的文件(空 = 只打开清单视图)
	Title     string `json:"title,omitempty"`     // 呈现端标题(已含文件名的短标题)
	Diff      string `json:"diff,omitempty"`      // 逐行 patch 文本(命令按预算截断后的成品)
	Added     int    `json:"added,omitempty"`     // 命中文件累计新增行数
	Removed   int    `json:"removed,omitempty"`   // 命中文件累计删除行数
	Changes   int    `json:"changes,omitempty"`   // 命中文件的改动条数
	Truncated bool   `json:"truncated,omitempty"` // Diff 被截断(呈现端需明示)
}

// FileChangeFrom 从事件载荷取 FileChangeEvent(值/指针兼容;取不到返回 false)。
func FileChangeFrom(payload any) (FileChangeEvent, bool) {
	switch p := payload.(type) {
	case FileChangeEvent:
		return p, true
	case *FileChangeEvent:
		if p != nil {
			return *p, true
		}
	}
	return FileChangeEvent{}, false
}

// DocOpenEvent 文档预览意图载荷(EventDocOpen)。
type DocOpenEvent struct {
	Path  string `json:"path"`
	Page  int    `json:"page,omitempty"`
	Sheet int    `json:"sheet,omitempty"`
}

// SessionEvent 是追加到会话日志的持久事实。
type SessionEvent struct {
	Kind    string
	Seq     uint64
	Payload any
	TS      time.Time
}

// UserMessage 用户输入(user/message 载荷)。
type UserMessage struct {
	Content     string
	Attachments []Attachment // 附件(图片视觉/文件引用;Rel 随 jsonl 便携,Path 运行时)
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

// BudgetPlanner 可选接口:压缩器不只“按预算折”,还能按**真实上下文占用**裁量本轮预算。
// host-session-log 每次投影前调用 Plan(未实现则退回 RegisterCompressor 的固定字符预算)。
// 为何需要:字符预算与模型窗口没有对应关系(实测语料约 2.8 字符/token,固定 40960 字符在
// 200K 窗口上约 7% 使用率就开始丢细节);而 token 占用有实测来源(session/usage 的
// PromptTokens),按窗口比例定阈值才准。
type BudgetPlanner interface {
	Plan(in CompressInput) CompressDecision
}

// CompressInput 交给压缩器的观测事实(策略不在这里:值全由 host-session-log 给出)。
type CompressInput struct {
	// LastPromptTokens 最近一次请求的实测 prompt token(0 = 尚无用量事件)。
	LastPromptTokens int
	// LastProjectChars 那次请求投影的字符数。与 LastPromptTokens 配套 ⇒ 两者之差即
	// 固定开销(系统提示 + 工具 schema)的占用,不必单独估算。
	LastProjectChars int
	// Window 最近一次请求时的模型上下文窗口(token;0 = 未知;经 usage/window 事件下发)。
	Window int
	// ProjectChars 当前投影字符数(这一次要发出去的历史部分)。
	ProjectChars int
}

// CompressDecision 压缩器给出的本轮处置。
type CompressDecision struct {
	// BudgetChars 本轮投影字符预算(<=0 = 本轮不压)。
	BudgetChars int
	// TrimChars 折叠后投影仍超此字符数时,由宿主截断**最旧的工具结果**(<=0 = 不截断)。
	// 用途:水位不得越过最后一个用户轮,长单轮(一轮内几十次工具调用)只能这样收。
	TrimChars int
}

// ForkPoint 会话历史中可作分支点的用户消息(seq + 摘要;供 /fork 定位与 /tree 展示)。
type ForkPoint struct {
	Seq  uint64
	Text string
}

// ForkNode 分支树节点:派生会话与来源(父会话 id + 父分支点 seq;seq 0 = 全量克隆)。
type ForkNode struct {
	ID        string // 会话 id(空 = 主会话)
	Parent    string // 父会话 id(空 = 根)
	ParentSeq uint64 // 父会话分支点 seq(0 = 克隆全量)
}

// ForkableSessions 会话树/分支(P4-10;可选实现——host-cwd-sessions)。类型断言发现,
// ctx.cwdSessions 接口不变。分支 = 复制继承历史到点的独立会话文件,继续演进互不影响。
type ForkableSessions interface {
	// ForkAt 从当前会话历史 seq 处派生新会话(继承 seq 及以前的全部事件),
	// 切换过去并从该点继续(新轮次只写新文件);返回新会话 id。
	ForkAt(seq uint64) (string, error)
	// CloneCurrent 复制当前会话全量到新会话文件(同一分支的另一路演进);返回新 id。
	CloneCurrent() (string, error)
	// ForkPoints 某会话文件(空 id = 主会话)的用户消息分支点列表(时间序;seq 供 /fork)。
	ForkPoints(id string) ([]ForkPoint, error)
	// ForkTree 项目会话分支树节点(派生关系:/fork 与 /clone 记录;P5.2-B3)。
	// 返回全部节点(含主会话,Parent 空 = 根);实现不提供时返回空表。
	ForkTree() ([]ForkNode, error)
}

// ReloadableInstructions 指令文件热重载(/reload 等效;可选实现——host-system-prompt 实现)。
// 重读全局/多级项目/附加指令文件,失败保留旧值(错误回滚,免重启生效)。
type ReloadableInstructions interface {
	ReloadInstructions() error
}

// CompactService 手动滚动压缩服务(/compact;可选实现——host-session-log 实现,
// 未实现时 TUI 命令提示不可用)。不改变 ctx.sessions 接口(类型断言发现)。
type CompactService interface {
	// Compact 立即以注册预算折叠滚动摘要(不等待投影超限)。prompt 为调用方指示词
	// (token-compress 为抽取式引擎,不消费其内容,仅作记录);返回最新累计摘要文本
	// 与本次被摘要覆盖的事件跨度(0 = 无可折叠/未发生)。
	Compact(prompt string) (summary string, folded int, err error)
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
	ID      string // 会话 id(空 = 主会话)
	Path    string // 落盘 jsonl 路径
	Name    string // 显示名(/name 设置;空 = 未命名)
	Preview string // 会话内容省略版(首条用户消息截断;空 = 无内容)
	MTime   int64  // 最后修改时间(unix 秒;0 = 未知/未落盘)
	Frames  int    // 事件条数(-1 = 未统计)

	// F 组会话体验(DESIGN §14.1 F0/F2/F3):置顶与概述(omitempty 向后兼容)。
	Pinned        bool     `json:",omitempty"` // 是否置顶
	PinnedAt      int64    `json:",omitempty"` // 置顶时间(unix 秒;置顶区按此倒序)
	Summary       string   `json:",omitempty"` // 已生成概述(空 = 未生成)
	SummaryTopics []string `json:",omitempty"` // 主题词(≤3)
	// SummaryCoveredFrames 概述覆盖到的会话帧数(与 Frames 比较判定 stale)
	SummaryCoveredFrames int `json:",omitempty"`
	// SummaryState:ready(覆盖帧数 = 当前帧数)/ stale(已生成但会话又更新)/
	// missing(未生成)/ unavailable(模型不可用)
	SummaryState string `json:",omitempty"`
}

// SessionSummary 会话概述产物(F3:host-session-summary 生成,落 meta.json 缓存)。
type SessionSummary struct {
	Text          string   `json:"text"`
	Topics        []string `json:"topics,omitempty"`
	CoveredFrames int      `json:"covered_frames,omitempty"`
	Model         string   `json:"model,omitempty"`
	TS            int64    `json:"ts,omitempty"`
	InputHash     string   `json:"input_hash,omitempty"`
}

// SessionSummaryService 会话概述服务(ctx.sessionSummary;host-session-summary 提供)。
// 纪律:Summary 会调用模型(**列表请求绝不触发**);force = 忽略缓存重新生成。
type SessionSummaryService interface {
	// Summary 取回/生成指定会话概述(id 空 = 主会话)。
	Summary(ctx context.Context, id string, force bool) (SessionSummary, error)
	// AutoEnabled 「回合后自动生成」是否开启(默认开,可经插件 data 关闭)。
	AutoEnabled() bool
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
	// SetName 设置指定会话显示名(id 空 = 主会话;name 空 = 清除)。
	// 与 Rename 的区别:Rename 只作用于当前会话,本方法按 id 定位(F 组 F3 概述回填标题)。
	SetName(id, name string) error
	// SetSummary 写入指定会话的概述缓存(id 空 = 主会话;供 ctx.sessionSummary 回写)。
	// 概述只落元数据(meta.json),**不落会话 jsonl、不进模型上下文**。
	SetSummary(id string, sum SessionSummary) error
	// SetPinned 置顶/取消置顶指定会话(id 空 = 主会话);幂等。
	// 置顶上限 8(超出返回显式错误,不静默丢弃);置顶项不参与任何自动清理。
	SetPinned(id string, pinned bool) error
	// Rename 设置当前会话显示名(空 = 清除)。名随会话文件持久化,
	// 状态栏/会话列表/切换选择器以名为优先展示,无名称回退 id/主会话。
	Rename(name string) error
	// Delete 删除会话记录(仅删该会话 jsonl 与显示名索引,不动任何目录)。
	// id 空 = 主会话;删除的是当前打开会话时自动切回主会话。
	Delete(id string) error
	// UnrecordProject 删除工作区(项目)使用记录(仅移除 workspaces 记录,
	// 不删除对应文件夹与其中的会话文件)。不存在则幂等成功。
	UnrecordProject(key string) error
	// SessionName 当前会话显示名(空 = 未命名)。
	SessionName() string
	// New 新建会话:生成唯一 id 并 Open,返回新会话 id。
	New() (string, error)
	// SwitchProject 切换当前项目:key = 新项目 key(cwd 派生),重绑后自动新建
	// 空会话(当前上下文与后续记录切到新项目文件;旧项目历史经 List/Sessions 回溯)。
	// 返回新会话 id。key 空 = default。宿主侧需先 os.Chdir(与 SwitchDir 的分工)。
	SwitchProject(key string) (string, error)
	// SwitchDir 切换工作区到真实目录(dir 语义,与 TUI 一致):os.Chdir(dir) →
	// key = ProjectKey(dir) → 重绑并新建空会话;工作区记录以真实 dir 落盘。
	// 目录不可用显式失败(不静默降级)。返回新会话 id。
	SwitchDir(dir string) (string, error)
	// RecentProjects 最近使用工作区(项目)列表,按最近使用时间倒序(TUI /workspace 选择)。
	RecentProjects() []ProjectInfo
}

// ProjectInfo 一条工作区(项目)使用记录:key(cwd 派生)与真实目录、最近使用时间。
type ProjectInfo struct {
	Key string `json:"key"`
	Dir string `json:"dir"`
	TS  int64  `json:"ts"` // 最近使用 unix 秒
}
