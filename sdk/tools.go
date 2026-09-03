// 工具定义与执行流水线(对齐设计 §5.5:工具定义 MCP 兼容 JSON Schema)。
package sdk

import "context"

// ToolDefinition MCP 兼容工具定义:name/description/inputSchema(JSON Schema)。
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any // JSON Schema 对象
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
