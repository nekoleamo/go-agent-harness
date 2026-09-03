// Command mcpserver 迷你 MCP server(stdio JSON-RPC,仅供集成测试)。
// 读取 stdin 每行一个 JSON-RPC 请求,写出响应;提供单个工具 greet。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		var req struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue // notification
		}
		var resp map[string]any
		switch req.Method {
		case "initialize":
			resp = map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]string{"name": "mini-mcp", "version": "1.0"},
			}}
		case "tools/list":
			resp = map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": map[string]any{
				"tools": []any{map[string]any{
					"name":        "greet",
					"description": "问候一个名字",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"name": map[string]any{"type": "string"}},
						"required":   []any{"name"},
					},
				}},
			}}
		case "tools/call":
			var p struct {
				Params struct {
					Arguments struct {
						Name string `json:"name"`
					} `json:"arguments"`
				} `json:"params"`
			}
			_ = json.Unmarshal([]byte(line), &p)
			resp = map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "你好, " + p.Params.Arguments.Name + "!"}},
			}}
		default:
			resp = map[string]any{"jsonrpc": "2.0", "id": *req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}}
		}
		b, _ := json.Marshal(resp)
		fmt.Fprintln(os.Stdout, string(b))
	}
}
