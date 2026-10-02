package toolshell

// PowerShell 工具(NOND-W1b)的行为契约。
//
// 能在 macOS 上验的部分:工具契约(定义/参数校验/平台门)、PowerShell 查找的失败路径、
// 以及「非 Windows 上不注册」。**真执行路径只能在 Windows 上验**(已登记真机清单)——
// 这里是把它挡在编译期之外,不是假装验过。

import (
	"encoding/json"
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestPowerShellToolDefinition(t *testing.T) {
	def := NewPowerShellTool().Definition()
	if def.Name != "powershell" {
		t.Fatalf("工具名应是 powershell,got %q", def.Name)
	}
	// 执行器类工具必须显式声明「无路径参数」,否则 host-tools 会打推断告警,
	// 而路径裁决应走 powershellCmdPaths 那一支。
	if !def.PathParamsDeclared {
		t.Error("powershell 必须显式声明 PathParamsDeclared(执行器类:命令文本不是路径参数)")
	}
	if def.ApprovalTargetParam != "command" {
		t.Errorf("审批目标参数应是 command,got %q", def.ApprovalTargetParam)
	}
	if def.TimeoutMs < 60_000 {
		t.Errorf("工具超时应覆盖内部 60s,got %d", def.TimeoutMs)
	}
	sch := def.InputSchema
	if sch == nil || sch["type"] != "object" {
		t.Fatalf("InputSchema 形状不对:%#v", def.InputSchema)
	}
	props, _ := sch["properties"].(map[string]any)
	if props["command"] == nil {
		t.Fatal("schema 应有 command 参数")
	}
	if req, _ := sch["required"].([]any); len(req) != 1 || req[0] != "command" {
		t.Errorf("required 应只含 command,got %#v", sch["required"])
	}
}

func TestPowerShellToolArgValidation(t *testing.T) {
	ctx := t.Context()
	tool := NewPowerShellTool()

	// 坏 JSON / 缺 command ⇒ 返回 error(参数错不是运行错)
	if _, err := tool.Execute(ctx, "{not json"); err == nil {
		t.Error("坏 JSON 应报错")
	}
	if _, err := tool.Execute(ctx, `{}`); err == nil {
		t.Error("缺 command 应报错")
	}
	if _, err := tool.Execute(ctx, `{"command":"   "}`); err == nil {
		t.Error("空白 command 应报错")
	}
}

func TestPowerShellToolNonWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上应走真实执行路径(需真机验)")
	}
	res, err := NewPowerShellTool().Execute(t.Context(), `{"command":"Get-Date"}`)
	if err != nil {
		t.Fatalf("非 Windows 上应回结构化说明而不是报错:%v", err)
	}
	m, _ := res.(map[string]any)
	msg, _ := m["error"].(string)
	if !strings.Contains(msg, "只在 Windows") {
		t.Errorf("错误应说清只在 Windows 上注册,got %q", msg)
	}
}

func TestResolvePowerShellUnavailableOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 上大概率能找到 PowerShell,跳过")
	}
	// 非 Windows 上恒不可用 —— 且**不能**返回成功(返回成功会让一段 Windows 逻辑
	// 在 macOS 上被当成可用)。错误要点明平台。
	_, err := ResolvePowerShell()
	if err == nil {
		t.Fatal("非 Windows 上 ResolvePowerShell 不该成功")
	}
	if !strings.Contains(err.Error(), runtime.GOOS) || !strings.Contains(err.Error(), "Windows") {
		t.Errorf("错误应点明平台限制,got %q", err.Error())
	}
}

// 平台门:非 Windows 上 Start 不应注册 powershell(模型不该为一个不存在的工具花轮次)。
func TestPowerShellRegisteredOnlyOnWindows(t *testing.T) {
	c := psTestCtx(t)
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var reg sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &reg); err != nil {
		t.Fatal(err)
	}
	hasShell := false
	for _, d := range reg.List() {
		if d.Name == "shell" {
			hasShell = true
		}
		if d.Name == "powershell" && runtime.GOOS != "windows" {
			t.Error("非 Windows 上不该注册 powershell")
		}
	}
	if !hasShell {
		t.Error("shell 工具应始终注册")
	}
}

// 工具定义能被真的序列化(invoke 通道要过 JSON);顺带钉住参数名。
func TestPowerShellToolArgsJSON(t *testing.T) {
	b, err := json.Marshal(psArgs{Command: "Get-Process", Timeout: 5})
	if err != nil {
		t.Fatal(err)
	}
	var back psArgs
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Command != "Get-Process" || back.Timeout != 5 {
		t.Fatalf("往返不对:%+v", back)
	}
}

// psTestCtx 起一个「已装配工具注册表」的宿主上下文(tool-shell 的 Start 需要 ctx.tools)。
func psTestCtx(t *testing.T) sdk.Ctx {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c
}

// Windows 上真跑一条命令(powershell_windows.go 的执行段就在这里被覆盖)。
// 为什么值得写:darwin/linux 上那段代码**根本不存在**(平台专属文件),所以它的覆盖率
// **只由 Windows CI 负责** —— 不写这个用例,那段代码的覆盖就是「没人验过」。
// 判据用 `Write-Output`(不是 `$PSVersionTable`:那个字段在不同版本上格式会变)。
func TestPowerShellExecOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell 执行路径只在 Windows 上存在")
	}
	res, err := NewPowerShellTool().Execute(t.Context(), `{"command":"Write-Output gah-ps-ok"}`)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := res.(map[string]any)
	if e, _ := m["exit_error"].(string); e != "" {
		t.Fatalf("命令应成功,exit_error=%s output=%v", e, m["output"])
	}
	out, _ := m["output"].(string)
	if !strings.Contains(out, "gah-ps-ok") {
		t.Fatalf("输出应含命令打印的内容,got %q", out)
	}
}

// 非零退出码 → 结构化 exit_error(不 panic、不中断回合)。
func TestPowerShellExecFailureOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只在 Windows 上执行")
	}
	res, err := NewPowerShellTool().Execute(t.Context(), `{"command":"exit 3"}`)
	if err != nil {
		t.Fatalf("失败也应回结构化结果而非 error:%v", err)
	}
	m, _ := res.(map[string]any)
	if _, ok := m["exit_error"]; !ok {
		t.Fatalf("非零退出应给 exit_error,got %#v", m)
	}
}

// timeoutOf:入参秒数优先,缺省/0 走默认。两分支都要钉(默认那支是「用户没传 timeout」的常态)。
func TestPowerShellTimeoutOf(t *testing.T) {
	def := 60 * time.Second
	if got := timeoutOf(0, def); got != def {
		t.Errorf("缺省应走工具默认:%v != %v", got, def)
	}
	if got := timeoutOf(5, def); got != 5*time.Second {
		t.Errorf("入参秒数应优先:%v", got)
	}
}
