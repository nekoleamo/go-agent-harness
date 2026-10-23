// 工具定义与执行流水线(对齐设计 §5.5:工具定义 MCP 兼容 JSON Schema)。
package sdk

import "context"

// ToolDefinition MCP 兼容工具定义:name/description/inputSchema(JSON Schema)。
// TimeoutMs 工具级执行超时(P0-2,host-bridge 覆写全局 3s 桥超时);0 = 使用默认。
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any // JSON Schema 对象
	TimeoutMs   int64          `json:"timeout_ms,omitempty"` // 毫秒;0 = 桥默认

	// PathParams 路径参数**能力声明**:本工具哪些参数承载路径、读写意图如何。
	// 宿主 pre-execute 的路径沙箱按此裁决 —— **声明优先**。
	// 未声明时宿主不再放行(fail-open):按 schema 参数名与工具名推断(见 sdk.InferPathParams),
	// 仍未命中则做值级兜底(参数值一眼是路径就裁决),并在注册期提示一次。
	// 因此插件的**建议**是显式声明(推断只是兼容后手);确实没有任何路径参数的工具用
	// PathParamsDeclared 显式声明,以免被推断误伤。
	// 只在宿主与插件协议间流转,不下发模型(适配层仅取 Name/Description/InputSchema)。
	PathParams []PathParam `json:"PathParams,omitempty"`

	// PathParamsDeclared 显式声明位:作者确认**本工具没有任何承载路径的参数**。
	// 与 PathParams 的空值区分:JSON 里二者都是"空",omitempty 无从分辨 —— 故单列一个布尔。
	// true 时宿主跳过推断与值级兜底(见 sdk.InferPathParams 与 policy-guard CheckToolCallAt)。
	PathParamsDeclared bool `json:"path_params_declared,omitempty"`

	// ProxyArgsParam 代理工具(见 ApprovalTargetParam)的「内层参数对象」字段名。
	// 如 search 模式的 mcp_call 声明 "arguments":宿主按**真实目标工具**的声明/推断裁决内层参数 ——
	// 否则 `mcp_call{name:"mcp_srv_write",arguments:{path:"…"}}` 的内层路径不经任何裁决
	// (审批维度已按真实名匹配,路径维度此前没有,属同一类间接绕过)。
	ProxyArgsParam string `json:"proxy_args_param,omitempty"`

	// ApprovalTargetParam 可选**能力声明**:本工具是「代理工具」—— 一次调用代表对**另一个
	// 工具**的调用(如 MCP 检索模式的 mcp_call:name 参数承载真实工具名)。
	// 声明后宿主审批把 approval_tools 里针对**真实工具名**的规则作用到本次调用
	// (否则逐工具规则会被一个间接名整体绕过),确认弹层也显示「代理工具 → 真实工具」。
	// 只在宿主与插件协议间流转,不下发模型。
	ApprovalTargetParam string `json:"ApprovalTargetParam,omitempty"`
}

// PathAccess 工具对某个路径参数的访问意图(能力声明用)。
type PathAccess string

const (
	PathRead  PathAccess = "read"  // 读:read-only/workspace-write 下限 workspace ∪ $GAH_HOME;凭据类一律拒
	PathWrite PathAccess = "write" // 写:read-only 全拒;workspace-write 限 workspace 内
)

// PathParam 一个承载路径的参数声明(顶层 JSON 字段名 + 访问意图)。
type PathParam struct {
	Arg      string     `json:"arg"`                // JSON 参数名
	Access   PathAccess `json:"access"`             // read / write
	Many     bool       `json:"many,omitempty"`     // 值为字符串数组:逐元素裁决
	Optional bool       `json:"optional,omitempty"` // 缺省即跳过(如“默认当前工作区根”)
	// Nested 嵌套取值路径(2026-09-27 F2 收敛):非空时按路径段取值,`*` = 数组展开,
	// 如 ["files","*","path"] = files 数组每个元素的 path(未声明工具的深层路径因此不再漂白)。
	// Arg 仍记顶层字段名(日志/文案用);值不是字符串的命中点直接跳过(不报错)。
	Nested []string `json:"nested,omitempty"`
}

// Tool 一个可执行工具。Execute 的 args 是 JSON 字符串(模型生成)。
// 返回 any 将序列化为工具结果(可附结构化视图,见 M3)。
type Tool interface {
	Definition() ToolDefinition
	Execute(ctx context.Context, args string) (any, error)
}

// ToolProgressReporter 工具进度的**上报出口**(2026-10-20 C1)。
//
// 为什么需要:一个跑 3 分钟的构建,界面上只有一个转圈 —— 用户不知道它在动、在等什么、
// 是不是卡住了,而"取消"在那 3 分钟里看上去也像失灵。
//
// 契约:
//   - **可选**:ctx 里没有 reporter 时工具照常工作(不产出进度)—— 旧工具/单测零改动;
//   - **尽力而为**:上报失败(通道断/界面已关)不得影响工具本身的成败;
//   - **只给 UI 看**:进度**不进会话日志**(过程不是事实,落盘会把会话文件灌满、
//     重放时再吐一遍);它走**该会话的事件通道**(sdk.EventToolProgress)广播而不落盘。
//   - **调用 id 由 reporter 侧持有**:工具只管报 (工具名, 增量文本),
//     reporter 自己知道这次调用属于哪一行(进程内:agent-loop 知道模型给的 id;
//     跨进程:桥把 ModelCallID 下传,外部进程经回调通道带回宿主)。
type ToolProgressReporter func(tool, chunk string) error

// progressReporterKey ctx 键(私有类型:外部无法伪造)。
type progressReporterKey struct{}

// WithToolProgressReporter 把进度上报出口挂到 ctx(宿主/外部进程侧装配)。
func WithToolProgressReporter(ctx context.Context, rep ToolProgressReporter) context.Context {
	return context.WithValue(ctx, progressReporterKey{}, rep)
}

// ToolProgressReporterFrom 取进度上报出口(不存在 ⇒ nil,false)。
func ToolProgressReporterFrom(ctx context.Context) (ToolProgressReporter, bool) {
	r, ok := ctx.Value(progressReporterKey{}).(ToolProgressReporter)
	return r, ok && r != nil
}

// ReportToolProgress 上报一段增量(无 reporter ⇒ 静默跳过;上报失败 ⇒ 忽略)。
//
// 工具侧唯一调用点:不判错、不重试 —— 进度是锦上添花,失败不该让命令失败。
func ReportToolProgress(ctx context.Context, tool, chunk string) {
	if chunk == "" {
		return
	}
	rep, ok := ToolProgressReporterFrom(ctx)
	if !ok {
		return
	}
	_ = rep(tool, chunk)
}

// toolCallIDKey ctx 键(私有类型)。
type toolCallIDKey struct{}

// WithToolCallID 把**模型侧**的工具调用 id 挂到 ctx。
//
// 为什么需要:工具执行在宿主里经过多层(agent-loop → host-tools → 插件),
// 而进度要能对回界面上那一行工具 —— 只有 agent-loop 手里有模型给的 call id。
// 各层 id 不同(host-tools 自己的 pre-execute id 是另一套),所以显式传。
func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// ToolCallIDFrom 取模型侧调用 id(不存在 ⇒ 空串)。
func ToolCallIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}

// toolNameKey ctx 键(私有类型)。
type toolNameKey struct{}

// WithToolName 把工具名挂到 ctx(由 host-tools 在执行前统一注入)。
//
// 为什么需要(2026-10-23 工具结果分流):`tools/post-execute` 的载荷只有
// *ToolResult,**没有工具名** —— 而按工具决定分流策略(跳过/豁免/换标)必须有它。
// 与 WithToolCallID 同源同理由:各层手里的 id 不是一套,只能显式传。
func WithToolName(ctx context.Context, name string) context.Context {
	return context.WithValue(ctx, toolNameKey{}, name)
}

// ToolNameFrom 取当前调用的工具名(不存在 ⇒ 空串)。
func ToolNameFrom(ctx context.Context) string {
	name, _ := ctx.Value(toolNameKey{}).(string)
	return name
}

// ToolResult 工具执行的结果载体(结构化错误回传模型,见设计 §11)。
type ToolResult struct {
	Content string // 模型可见内容(序列化结果 JSON/文本;可被宿主瘦身,见 Data)
	Error   string // 错误时非空;不中断 turn,原样回传模型

	// Data 数据面:**完整原文**,只给界面与会话记录,**永不进模型上下文**。
	//
	// 为什么要有两个面(2026-10-23,借鉴 Octop 的 octop_ui artifact 旁路):一个工具
	// 结果同时服务两个消费者 —— 模型(越小越好)与界面(要完整)。此前只有 Content
	// 一份,于是大结果只有两个坏选择:整份塞进上下文(一次 `cat` 大文件就能把窗口
	// 打穿,溢出后被动压缩),或截断后界面也看不全。
	//
	// 分流点是宿主插件 host-tool-offload(订阅 tools/post-execute):超预算时把原文
	// 搬到这里,Content 换成「头 + 省略说明 + 尾」(尽量保持 JSON 合法)。
	//
	// 三条铁律:
	//  1. Data **不进模型投影** —— host-session-log 的 DeriveMessages 只读 Content
	//     (用例 internal/tooloffload 与 host-session-log 各钉一条);
	//  2. Data 随会话事件**落盘**(刷新/重连/换端后界面仍能展开原文);
	//  3. 空串 = 未分流(原文就在 Content 里),不是「原文为空」。
	Data string
}

// ToolConflict 注册期同名工具冲突(B3,2026-09-27 审计观察项)。
//
// 背景:注册是 first-wins(先到者胜)—— 后到的同名工具被**忽略**,只留一行日志。
// 后果是“插件启用成功但工具不存在”这种状态在界面上看不见,排查只能翻日志;
// 若后到者是修复版插件,还会静默沿用旧实现。这里把“被忽略者”记下来供可见性面使用。
type ToolConflict struct {
	Name    string `json:"name"`    // 冲突的工具名(已被先注册者占用)
	Ignored string `json:"ignored"` // 被忽略者的描述(定位是哪个插件——插件清单暂不带提供者字段)
}

// ToolConflictReporter 可选能力:注册期冲突的可见性(host-tools 实现)。
//
// 为何不扩 ToolRegistry 接口:实现方只需**自愿**实现,消费者按类型断言读取,避免为了
// 一个诊断面把所有实现方+测试替身都撑大。
type ToolConflictReporter interface {
	ToolConflicts() []ToolConflict
}

// ToolCatalogue 可选能力:角色工具集过滤(host-tools 实现,第九十一批)。
//
// 为何不扩 ToolRegistry 接口:与 ToolConflictReporter 同款 —— 只让 host-tools **自愿**实现,
// 测试替身/极简宿主不受影响(未实现 = 不做工具过滤,即今天行为;不是静默降级)。
//
// 与技能可见性(host-skills 的 SetFilter)**分工明确**:技能是“有没有这份知识”,
// 工具是“能不能动手” —— 同一个角色表达两件事,分别落在两个 Registry 上。
// 过滤必须同时作用于 List 与 Execute:只过 List 会让模型凭记忆调用绕过
// (工具名在历史上下文里出现过)。
//
// 豁免:Get **不**过过滤 —— 它是裁决面(policy-guard 靠它取真实目标工具定义做
// 路径/审批裁决)与插件自查面,过滤它会让被排除的工具连裁决都拿不到定义。
// 管理面需“注册了什么”而非“模型看得见什么”时用 ListAll。
//
// 单一判据:判定函数用 sdk.ToolVisible(role, name),运行期与展示端共用。
// 未装配 ctx.tools 的宿主:host-roles 侧跳过(显式不报错)。
type ToolCatalogue interface {
	// SetFilter 安装可见性判定函数(nil = 不过滤,恢复全量);返回 Disposer 幂等撤销。
	SetFilter(visible func(ToolDefinition) bool) Disposer
	// ListAll 全部已注册工具(不过滤);未注册工具时返回空切片。
	ListAll() []ToolDefinition
}

// ContextualToolCatalogue 可选扩展(ctx.tools 实现者):按**调用所属会话**过滤可见工具。
//
// 为什么需要(第一百一十六批):SetFilter 的判定是无参的,而 List() 在组装请求时调用 ——
// 那时 ctx 里带着会话 id。多页签各用各的角色时,角色 A 排除的工具不该在页签 B 的
// 工具列表里(那等于让 A 的收窄失去意义)。
type ContextualToolCatalogue interface {
	// SetContextFilter 安装按会话的可见性判定(nil = 回落 SetFilter 的判定)。
	SetContextFilter(visible func(ctx context.Context, def ToolDefinition) bool) Disposer
	// ListFor 该会话下模型可见的工具定义。
	ListFor(ctx context.Context) []ToolDefinition
}

// ToolRegistry 服务(ctx.tools):注册/列举/带流水线执行。
type ToolRegistry interface {
	// Register 注册工具,返回 Disposer。
	Register(t Tool) Disposer
	// List 返回模型可见的工具定义列表。
	List() []ToolDefinition
	// Get 取回单个工具定义。
	Get(name string) (ToolDefinition, bool)
	// Execute 经全流水线执行一个工具:
	//   tools/pre-execute(waterfall,veto 阻止)
	//   tools/execute(waterfall,默认实现为实际调用)
	//   tools/post-execute(waterfall)
	//   tool/result(emit 广播结果)
	Execute(ctx context.Context, name, args string) (*ToolResult, error)
}
