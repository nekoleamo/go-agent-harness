// Package mcpserver 提供 mcp-server 插件(MCP server 端,原"二期"现已交付)。
// stdio JSON-RPC 2.0 传输(每行一个消息,2024-11-05 协议),与 mcp-bridge(client)对称:
//
//	客户端 → initialize / notifications/initialized / tools/list / tools/call
//	host → 把 ctx.tools 已注册工具暴露为 MCP tools,tools/call 经全流水线执行
//
// 默认关闭:适合 serve/headless 场景(gah 作为 MCP server 被外部客户端拉起);
// TUI 等交互模式会与终端 stdin 冲突,不应启用。
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// in/out 默认宿主 stdin/stdout;测试可替换(serve 循环的字节流注入)。
var (
	in  io.Reader = os.Stdin
	out io.Writer = os.Stdout
)

// Plugin 实现 mcp-server。requires ctx.tools(经 Ctx 注入,不 import 宿主内部包)。
type Plugin struct{}

func (p *Plugin) Name() string { return "mcp-server" }

// Start 启动 stdio JSON-RPC 服务循环,把 ctx.tools 已注册工具暴露为 MCP tools。
// Disposer 关闭 done 并等待 serve goroutine 退出(幂等)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serve(tools, in, out, done)
	}()
	return func() {
		close(done)
		wg.Wait()
	}, nil
}

// serve 顺序读行处理(stdin EOF/断开即退出,serve 会话结束)。
func serve(tools sdk.ToolRegistry, r io.Reader, w io.Writer, done chan struct{}) {
	sc := bufio.NewReader(r)
	for {
		line, err := sc.ReadBytes('\n')
		if err != nil {
			return // EOF/断开:serve 结束
		}
		select {
		case <-done:
			return
		default:
		}
		handle(tools, w, line)
	}
}

// —— JSON-RPC 消息 ——

// req MCP 请求。ID 保留原始(raw)以回显任意 JSON 标量;通知无 ID。
type req struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// handle 解析单行并写出响应(通知/无 ID 不响应)。
func handle(tools sdk.ToolRegistry, w io.Writer, line []byte) {
	var r req
	if err := json.Unmarshal(line, &r); err != nil {
		writeErr(w, nil, -32700, "Parse error")
		return
	}
	if r.JSONRPC != "2.0" || r.Method == "" {
		writeErr(w, r.ID, -32600, "Invalid Request")
		return
	}
	// MCP 通知(jsonrpc 2.0 + method,无 id):客户端初始化成功等,不响应。
	if len(r.ID) == 0 || string(r.ID) == "null" {
		return
	}
	switch r.Method {
	case "initialize":
		writeResult(w, r.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": "gah", "version": "dev"},
		})
	case "ping":
		writeResult(w, r.ID, map[string]any{})
	case "tools/list":
		writeResult(w, r.ID, map[string]any{"tools": listDefs(tools)})
	case "tools/call":
		result, rpcErr := toolsCall(tools, r.Params)
		if rpcErr != nil {
			writeErr(w, r.ID, rpcErr.Code, rpcErr.Message)
			return
		}
		writeResult(w, r.ID, result)
	case "notifications/initialized": // 已由无 ID 分支拦截,兜底不响应
	default:
		writeErr(w, r.ID, -32601, "Method not found: "+r.Method)
	}
}

// toolDef MCP 工具清单条目(与 mcp-bridge client 端同构)。
type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// listDefs 当前模型可见工具(ctx.tools 实时快照,插拔立即反映)。
func listDefs(tools sdk.ToolRegistry) []toolDef {
	var defs []toolDef
	for _, d := range tools.List() {
		defs = append(defs, toolDef{Name: d.Name, Description: d.Description, InputSchema: d.InputSchema})
	}
	return defs
}

// rpcErr RPC 层错误(method 缺失/参数非法/执行异常),与工具业务失败(isError)区分。
type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toolsCall 执行一次工具调用。成功/业务失败走 result(isError 标记,规范);协议层错误返回 rpcErr。
func toolsCall(tools sdk.ToolRegistry, params json.RawMessage) (map[string]any, *rpcErr) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
		return nil, &rpcErr{Code: -32602, Message: "tools/call: 需要 name 与 arguments"}
	}
	args := p.Arguments
	if len(args) == 0 || string(args) == "null" {
		args = []byte("{}")
	}
	textResult, err := tools.Execute(context.Background(), p.Name, string(args))
	if err != nil {
		return nil, &rpcErr{Code: -32603, Message: "工具执行失败: " + err.Error()}
	}
	// 工具业务失败:isError=true 回传(与 policy 拒绝/工具错误同语义)。
	text := ""
	isErr := false
	if textResult != nil {
		text = textResult.Content
		if textResult.Error != "" {
			text = textResult.Error
			isErr = true
		}
	}
	res := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	}
	if isErr {
		res["isError"] = true
	}
	return res, nil
}

// —— 响应写出 ——

func writeResult(w io.Writer, id json.RawMessage, result any) {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if err != nil {
		return
	}
	w.Write(append(b, '\n'))
}

func writeErr(w io.Writer, id json.RawMessage, code int, msg string) {
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": msg},
	})
	if err != nil {
		return
	}
	w.Write(append(b, '\n'))
}
