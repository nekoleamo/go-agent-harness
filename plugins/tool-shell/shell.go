// Package toolshell 提供 tool-shell 插件:最小 shell 执行工具(MVP 阶段)。
// 沙箱三档拦截在 M4 policy-sandbox 引入(监听 tools/pre-execute);本版本仅提供基础执行:
// JSON args {"command": "..."},ContextCommand 超时 60s,错误结构化回传模型。
package toolshell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 tool-shell。requires ctx.tools(注册工具)。
type Plugin struct{}

func (p *Plugin) Name() string { return "tool-shell" }

// Start 注册 shell 工具。
func (p *Plugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	d := tools.Register(&ShellTool{timeout: 60 * time.Second})
	return d, nil
}

type args struct {
	Command string `json:"command"`
}

// ShellTool 执行 shell 命令。
type ShellTool struct {
	timeout time.Duration
}

func (s *ShellTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "shell",
		Description: "在用户的 shell 中执行一次命令(输出 stdout/stderr;错误时返回非零退出码信息)。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "要执行的 shell 命令"},
			},
			"required": []any{"command"},
		},
	}
}

// Execute 执行命令(结构化错误回传模型,不中断 turn)。
func (s *ShellTool) Execute(ctx context.Context, raw string) (any, error) {
	var a args
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("shell: args: %w", err)
	}
	if a.Command == "" {
		return nil, fmt.Errorf("shell: 缺少 command 参数")
	}

	dctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	cmd := exec.CommandContext(dctx, "sh", "-c", a.Command)
	cmd.Env = sdk.SanitizedEnv(os.Environ()) // 凭据隔离:滤除 *_API_KEY/*_TOKEN 等
	out, err := cmd.CombinedOutput()
	if err != nil {
		return map[string]any{
			"exit_error": err.Error(),
			"output":     string(out),
		}, nil // 结构化错误回传模型
	}
	return map[string]any{"output": string(out)}, nil
}
