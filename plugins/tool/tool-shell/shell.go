// Package toolshell 提供 tool-shell 插件:shell 执行工具。
// 沙箱三档拦截在 M4 policy-guard 引入(监听 tools/pre-execute)。
// 普通模式:JSON args {"command": "..."},超时 60s,错误结构化回传模型;
// pty 模式(M6.3,data.pty 开关):命令挂 pseudo-terminal,input 可一次性写入,
// 交互式命令(REPL/git 编辑器/询问式脚本)走 execPty(见 pty.go)。
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
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	// data.pty 开关(M6.3):启用后 shell 挂伪终端执行,交互式命令可用 input 批次输入
	ptyEnabled := false
	if m != nil && m.Data != nil {
		if on, ok := m.Data["pty"].(bool); ok {
			ptyEnabled = on
		}
	}
	d := tools.Register(&ShellTool{timeout: 60 * time.Second, pty: ptyEnabled})
	return d, nil
}

// NewTool 外部化工厂(P1):构造外部进程入口的工具实例。
func NewTool(ptyEnabled bool) sdk.Tool {
	return &ShellTool{timeout: 60 * time.Second, pty: ptyEnabled}
}

type args struct {
	Command string `json:"command"`
	Input   string `json:"input,omitempty"` // pty 模式:一次性写入的输入(REPL 命令/编辑器内容)
}

// ShellTool 执行 shell 命令。
type ShellTool struct {
	timeout time.Duration
	pty     bool // data.pty 开关:命令挂伪终端执行
}

func (s *ShellTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "shell",
		Description: "在用户的 shell 中执行一次命令(输出 stdout/stderr;错误时返回非零退出码信息)。",
		TimeoutMs:   65_000, // 覆盖 host-bridge 默认 3s 桥超时(内部超时 60s 后返回结构化结果)
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "要执行的 shell 命令"},
				"input":   map[string]any{"type": "string", "description": "pty 模式:一次性写入的输入"},
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

	if s.pty {
		// pty 模式(M6.3):伪终端执行,输入批次写入,终输出采集(超时兜底)
		if a.Command == "" {
			return nil, fmt.Errorf("shell: 缺少 command 参数")
		}
		out, timedOut, perr := execPty(ctx, a.Command, a.Input)
		if perr != nil {
			return map[string]any{"error": "shell: pty 启动失败: " + perr.Error()}, nil
		}
		res := map[string]any{"output": string(out)}
		if timedOut {
			res["timeout"] = true // 交互进程未退出:已终止并返回已捕获输出
		}
		return res, nil
	}

	// shell 解析(Windows 需 Git Bash;不提供 cmd/PowerShell 回退,见 sdk/shellpath.go):
	// 缺失时显式回结构化错误,不静默降级。
	sh, serr := sdk.ResolvePOSIXShell()
	if serr != nil {
		return map[string]any{"error": "shell: " + serr.Error()}, nil
	}

	dctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	// 环境 jail **先**建出(见 jail.go),再算内核白名单:kernelWrap 里的 resolvePath 要
	// EvalSymlinks(jailRoot()),而全新数据根下 jail 目录此刻还不存在 —— 解析失败会回退
	// 未解析路径,GAH_HOME 含软链组件(/tmp、/var)时 seatbelt 的白名单整条失效(首条命令
	// 写缓存即报 operation not permitted,第二条起又正常)。顺序不可换。
	// 凭据隔离(SanitizedEnv)在前,环境 jail 覆盖缓存根/临时根在后(见 jail.go):
	// jail 建不起来时显式回结构化错误,不静默放行未受约束的命令。
	env, jerr := jailEnv(sdk.SanitizedEnv(os.Environ()))
	if jerr != nil {
		return map[string]any{"error": "shell: 环境 jail 初始化失败: " + jerr.Error()}, nil
	}

	// 内核级沙箱(第 3 组 ①-E):按宿主注入的**有效**档位施加进程树级文件写限制(见 kernel.go)。
	// 包装为空(未注入档位/全权档/平台不支持/开关关闭)时 argv 与原来一致。
	pre := kernelWrapCtx(ctx)
	argv := prefixedArgv(pre, sh, "-c", a.Command)
	cmd := exec.CommandContext(dctx, argv[0], argv[1:]...)
	cmd.Env = sdk.ShellExecEnv(env) // MSYS 路径转换开关(Windows;见 sdk/shellpath.go)
	// 后台孙进程(如 `sleep 300 &`)会持有 stdout 管道 → CombinedOutput 永不返回;
	// 进程组 + WaitDelay 双保险:超时先杀直接子进程,WaitDelay 到点放弃 I/O 等待。
	setProcessGroup(cmd)
	cmd.WaitDelay = 3 * time.Second
	defer killProcessGroup(cmd) // 收尾杀掉组内残留(含后台孙进程)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return map[string]any{
			"exit_error": err.Error(),
			"output":     string(out),
		}, nil // 结构化错误回传模型
	}
	return map[string]any{"output": string(out)}, nil
}
