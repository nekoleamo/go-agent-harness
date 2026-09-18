// wire.go:Agent Client Protocol v1 线上结构(逐字段对齐 schema/v1,字段名与规范一致)。
//
// 纪律:本文件只描述线上形状,不含逻辑。协议适配的风险集中在字段名与可选性上,
// 与业务逻辑分离便于逐字段比对规范;新增 session/update 变体时同步在 updates.go 登记。
package acpserver

import "encoding/json"

// protocolVersion 本实现支持的 ACP 线上协议主版本(Zed 等客户端协商的即此整数)。
// 协商规则(规范 initialize):客户端发它支持的最新版;服务端支持则回同一版,
// 否则回自己支持的最新版,由客户端决定是否断开。v1 是当前稳定线上版本
// (v2 仍是 alpha,客户端未跟进),故本实现只声明 1。
const protocolVersion = 1

// agentName agentInfo.name(与 mcp-server 同一口径)。
const agentName = "gah"

// —— 客户端 → 服务端:请求参数 ——

// initializeParams initialize 请求参数(clientCapabilities 当前未消费:本实现不调用
// 客户端能力方法,fs/terminal 全走 gah 自身沙箱工具)。
type initializeParams struct {
	ProtocolVersion    int            `json:"protocolVersion"`
	ClientCapabilities map[string]any `json:"clientCapabilities"`
	ClientInfo         *implInfo      `json:"clientInfo"`
}

// implInfo 双方自述信息。
type implInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

// newSessionParams session/new 参数:cwd 必填,mcpServers 必填(可为空数组)。
type newSessionParams struct {
	Cwd                   string          `json:"cwd"`
	McpServers            []mcpServerSpec `json:"mcpServers"`
	AdditionalDirectories []string        `json:"additionalDirectories"`
}

// mcpServerSpec ACP 侧 MCP server 声明(本实现不消费:gah 的 MCP 接入走自身配置与
// GAH_MCP_COMMANDS 环境,不接受按会话注入的外部进程 —— 见包注释「未实现清单」)。
type mcpServerSpec struct {
	Name    string              `json:"name"`
	Command string              `json:"command"`
	Args    []string            `json:"args"`
	Env     []map[string]string `json:"env"`
	URL     string              `json:"url"`
}

// promptParams session/prompt 参数。
type promptParams struct {
	SessionID string         `json:"sessionId"`
	Prompt    []contentBlock `json:"prompt"`
}

// cancelParams session/cancel 通知参数。
type cancelParams struct {
	SessionID string `json:"sessionId"`
}

// contentBlock 提示内容块(规范 ContentBlock 联合类型,按 type 判别)。
// 基线必支持 text 与 resource_link;image/audio/resource 需要能力声明,
// 本实现声明为不支持(initialize 里 promptCapabilities 全 false)。
type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

// —— 服务端 → 客户端:更新载荷 ——

// sessionUpdateParams session/update 通知参数(所有变体共用外层)。
type sessionUpdateParams struct {
	SessionID string `json:"sessionId"`
	Update    any    `json:"update"`
}

// textBlock 文本内容块(session/update 的 content 字段)。
type textBlock struct {
	Type string `json:"type"` // 恒为 "text"
	Text string `json:"text"`
}

// chunkUpdate 流式内容块变体(agent_message_chunk / agent_thought_chunk)。
type chunkUpdate struct {
	SessionUpdate string    `json:"sessionUpdate"`
	Content       textBlock `json:"content"`
	MessageID     string    `json:"messageId,omitempty"`
}

// toolUpdate 工具调用变体:首报用 sessionUpdate="tool_call"(title 必填),
// 后续用 "tool_call_update"(只有 toolCallId 必填,其余为「本次变更的字段」)。
type toolUpdate struct {
	SessionUpdate string         `json:"sessionUpdate"`
	ToolCallID    string         `json:"toolCallId"`
	Title         string         `json:"title,omitempty"`
	Name          string         `json:"name,omitempty"`
	Kind          string         `json:"kind,omitempty"`
	Status        string         `json:"status,omitempty"`
	Content       []any          `json:"content,omitempty"`
	Locations     []toolLocation `json:"locations,omitempty"`
	RawInput      any            `json:"rawInput,omitempty"`
}

// toolLocation 工具调用涉及的文件位置(客户端据此做 follow-along)。
type toolLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// contentItem tool_call content 的通用项(type="content",内嵌 ContentBlock)。
type contentItem struct {
	Type    string    `json:"type"` // 恒为 "content"
	Content textBlock `json:"content"`
}

// usageUpdate 会话用量变体:used = 最近一次请求的输入 token(当前上下文占用量),
// size = 模型上下文窗口;cost 不报(gah 无计费表,不编造)。
type usageUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	Used          int    `json:"used"`
	Size          int    `json:"size"`
}

// commandsUpdate 可用命令变体(客户端斜杠菜单):命令以提示文本发送执行(ACP v1 无独立执行方法)。
type commandsUpdate struct {
	SessionUpdate     string             `json:"sessionUpdate"`
	AvailableCommands []availableCommand `json:"availableCommands"`
}

// availableCommand 一条可用命令。
type availableCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// —— 服务端 → 客户端:请求 ——

// permissionParams session/request_permission 参数。
type permissionParams struct {
	SessionID string             `json:"sessionId"`
	ToolCall  permissionToolCall `json:"toolCall"`
	Options   []permissionOption `json:"options"`
}

// permissionToolCall 权限请求关联的工具调用描述(ToolCallUpdate 的最小子集:
// 只有 toolCallId 必填;gah 的审批请求只带一句提示文本,故这里给占位标题)。
type permissionToolCall struct {
	ToolCallID string `json:"toolCallId"`
	Title      string `json:"title,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Status     string `json:"status,omitempty"`
}

// permissionOption 一个权限选项。
type permissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once / allow_always / reject_once / reject_always
}

// permissionResult 客户端裁决(Tagged union:outcome 判别)。
type permissionResult struct {
	Outcome permissionOutcome `json:"outcome"`
}

// permissionOutcome 裁决内容:{outcome:"selected", optionId} 或 {outcome:"cancelled"}。
type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

// —— JSON-RPC 信封 ——

// rpcMsg 一行 JSON-RPC 消息(请求 / 通知 / 响应三态共用一个解析结构)。
type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

// rpcError JSON-RPC 错误对象。
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC 标准错误码 + 本实现自定义码(实现定义区间 -32000..-32099)。
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603

	codeNotInitialized = -32002 // initialize 之前调用了会话方法
	codeBusy           = -32003 // 已有回合在运行(gah 单进程单回合)
)
