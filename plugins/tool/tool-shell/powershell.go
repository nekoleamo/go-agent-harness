// PowerShell 执行工具(NOND-W1b · Windows 侧执行器)。
//
// 定位(**为什么是独立工具而不是 shell 的兜底**):`shell` 在 Windows 上走 Git Bash
// (`sdk.ResolvePOSIXShell`),那是为「类 Unix 命令习惯」准备的。但真实 Windows 用户
// 要跑的是 `Get-ChildItem`、`Get-Process`、注册表与服务 cmdlet —— 用 bash 去拼这些
// 既别扭又常缺依赖。两个工具各管各的语法,**不用 PowerShell 去兜底 bash 的活**,
// 也不用 bash 去模拟 PowerShell。
//
// 为什么放在 tool-shell 这个包里(而不是新插件):内核沙箱包装(apply kernel sandbox
// 到进程树)、环境 jail、凭据隔离、进程组回收这四段**都是安全关键代码**,复制一份到
// 新插件等于复制四个可被各自漂移的实现。插件之间不互相 import(红线),所以复用只有
// 「同包」这一条路。登记、bundle、发布矩阵因此都不变 —— 这是取舍,不是省事。
//
// 安全口径(与 shell 完全同一条链,不因「新工具」而放松):
//   - 命令文本经 `tools/pre-execute` 进 policy-guard:**自己的**危险词表
//     (`dangerousPowerShellPatterns`)+ **自己的**写目标扫描器(`powershellCmdPaths`);
//   - 内核级沙箱包装(Windows 上无内核层 ⇒ 这一层不存在,如实降级并告警);
//   - 凭据隔离 + 环境 jail + 进程组/WaitDelay 收尾,全部复用同包的实现。
package toolshell

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// PowerShellTool 执行一条 PowerShell 命令(仅 Windows 注册)。
type PowerShellTool struct {
	timeout time.Duration
}

// NewPowerShellTool 构造(供单测与外部化工厂用)。
func NewPowerShellTool() sdk.Tool { return &PowerShellTool{timeout: 60 * time.Second} }

type psArgs struct {
	Command string `json:"command"`
	// Timeout 可选:单条命令的秒数上限(0/缺省 = 工具默认 60s)。
	Timeout int `json:"timeout,omitempty"`
}

func (p *PowerShellTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name: "powershell",
		Description: "在 Windows 上执行一条 PowerShell 命令(Get-ChildItem / Get-Process / 注册表与服务 cmdlet 这类原生命令用这个;" +
			"类 Unix 命令习惯用 shell 工具,它走 Git for Windows)。输出 stdout/stderr,失败时返回退出码信息。",
		TimeoutMs: 65_000, // 覆盖 host-bridge 默认 3s 桥超时(内部超时 60s 后返回结构化结果)
		// 显式声明「无路径参数」:命令文本不是路径参数(执行器类豁免),不声明会触发
		// host-tools 的推断告警,声明了则路径裁决走 powershellCmdPaths 那一支。
		PathParamsDeclared:  true,
		ApprovalTargetParam: "command",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "要执行的 PowerShell 命令"},
				"timeout": map[string]any{"type": "integer", "description": "超时秒数(可选,默认 60)"},
			},
			"required": []any{"command"},
		},
	}
}

// Execute 执行命令(结构化错误回传模型,不中断 turn)。
//
// 三段分平台的理由(顺带解决一个覆盖率问题):真实执行那一段在 **Windows 专属文件**
// 里(`powershell_windows.go` / `powershell_other.go`)。不这么切的话,darwin/linux 会把
// 一段**永远跑不到**的 Windows 代码编进二进制并计入覆盖率 —— tool-shell 因此从 90%
// 掉到 75.6%(实测)。按平台切文件是 Go 的标准做法,它同时说清了一件事:
// **这段逻辑的覆盖率归 Windows 平台管**。
func (p *PowerShellTool) Execute(ctx context.Context, raw string) (any, error) {
	// 参数校验在平台门**之前**:参数错就是参数错,与平台无关。先判平台的话,一次坏调用
	// 在 macOS 上会得到「只在 Windows 上注册」这种与真实原因无关的回执。
	var a psArgs
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("powershell: args: %w", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return nil, fmt.Errorf("powershell: 缺少 command 参数")
	}
	// 平台分派交给 runPowerShell(平台专属文件),不在这里再判一次平台 ——
	// 两处都判的话,非 Windows 上 runPowerShell 的那一句就永远走不到(0% 覆盖),
	// 而它恰恰是「本平台没有这个工具」这句人话的唯一出处。
	return runPowerShell(ctx, a.Command, timeoutOf(a.Timeout, p.timeout), workdirOf(ctx)), nil
}

// timeoutOf 单条命令的超时:入参秒数优先,缺省用工具默认值。
func timeoutOf(sec int, def time.Duration) time.Duration {
	if sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return def
}
