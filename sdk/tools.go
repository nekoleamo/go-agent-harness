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

	// PathParams 可选**能力声明**:本工具哪些参数承载路径、读写意图如何。
	// 宿主 pre-execute 的路径沙箱按此裁决(声明优先;未声明回退内置工具名表)。
	// 名字不在内置表中的插件工具(如 save_note/import_files)**必须**声明,
	// 否则不受路径沙箱约束(见 docs/PLUGIN_DEV.md「路径参数声明」)。
	// 只在宿主与插件协议间流转,不下发模型(适配层仅取 Name/Description/InputSchema)。
	PathParams []PathParam `json:"PathParams,omitempty"`
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
	Optional bool       `json:"optional,omitempty"` // 缺省即跳过(如"默认当前工作区根")
}

// Tool 一个可执行工具。Execute 的 args 是 JSON 字符串(模型生成)。
// 返回 any 将序列化为工具结果(可附结构化视图,见 M3)。
type Tool interface {
	Definition() ToolDefinition
	Execute(ctx context.Context, args string) (any, error)
}

// ToolResult 工具执行的结果载体(结构化错误回传模型,见设计 §11)。
type ToolResult struct {
	Content string // 序列化结果(JSON/文本)
	Error   string // 错误时非空;不中断 turn,原样回传模型
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
