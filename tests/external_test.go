// Package tests 集成测试:外部化全链路(M6.8 工具类全外部化)。
// 装配宿主(host-tools/host-jobs/host-fanout/host-bridge + 回调通道),从 embed 释放
// 外部二进制到临时目录,验证:外部 tool-workflow(starlark 引擎经回调驱动)、
// background 提交(宿主 jobs 托管)、外部 tool-mcp(MCP client 桥)。
package tests

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/internal/embed"
	"github.com/nekoleamo/go-agent-harness/internal/mcpconfig"
	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// releaseExt 把 embed 中的外部插件二进制释放到临时目录(可执行)。
func releaseExt(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		// M7 体积门:embed 存 gzip,named name.gz;释放时解压
		fgz, err := embed.OpenExtPlugin(n)
		if err != nil {
			t.Fatalf("embed 读取 %s.gz: %v", n, err)
		}
		gzr, gerr := gzip.NewReader(fgz)
		if gerr != nil {
			fgz.Close()
			t.Fatal(gerr)
		}
		raw, err := io.ReadAll(gzr)
		gzr.Close()
		fgz.Close()
		if err != nil {
			t.Fatal(err)
		}
		// 写盘名带平台扩展名(os/exec 在 Windows 上按 PATHEXT 解析,见 embed.ExtPluginBinary)
		dst := filepath.Join(dir, embed.ExtPluginBinary(n))
		if err := os.WriteFile(dst, raw, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// buildExternalEnv 装配宿主:host-tools + llm(mock)+ sysp + jobs + fanout + bridge(回调通道)。
// dir 为外部插件目录;enabled 指定要启用的插件集合(默认上述集合)。
func buildExternalEnv(t *testing.T, dir string, extra ...config.Entry) (*ctx.Ctx, *plugin.Registry) {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	reg := plugin.New()
	tree := config.NewTree()
	entries := []config.Entry{
		{ID: "host-tools"},
		{ID: "host-llm"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock"},
		{ID: "host-session-log"}, // M9.3 fork 的 ctx.sessions 来源(须在 host-fanout 之前装配)
		{ID: "host-jobs"},
		{ID: "host-fanout"},
		{ID: "host-bridge", Data: map[string]any{"dir": dir}},
	}
	entries = append(entries, extra...)
	tree.Apply(entries)
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := base.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c, reg
}

// TestExternalWorkflow 外部 tool-workflow:starlark 引擎在外部进程,脚本内工具调用经
// 宿主回调 → host-bridge → 外部 tool-basic(shell)。
func TestExternalWorkflow(t *testing.T) {
	t.Parallel() // M16 T1:独立 e2e(各自 TempDir)并行化
	extDir := t.TempDir()
	releaseExt(t, extDir, "tool-basic", "tool-workflow")
	c, _ := buildExternalEnv(t, extDir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// 确认外部工具已注册(宿主视角)
	for _, name := range []string{"workflow", "workflow_collect", "shell"} {
		if _, ok := tools.Get(name); !ok {
			t.Fatalf("外部工具 %s 应已注册", name)
		}
	}
	// 脚本:shell 调用经回调回到宿主 → tool-basic
	script := `r = shell({"command": "echo ext-ok"})
result = {"out": r}`
	res, err := tools.Execute(context.Background(), "workflow", mustJSON2(t, map[string]any{"script": script}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("workflow 应成功: %s", res.Error)
	}
	if !strings.Contains(res.Content, "ext-ok") {
		t.Fatalf("shell 输出应经回调回传: %s", res.Content)
	}
}

// TestExternalWorkflowBackground 外部 workflow 背景任务:回调宿主 jobs.run 托管,
// 宿主任务体经桥协议调回外部进程执行;workflow_collect 回调取回。
func TestExternalWorkflowBackground(t *testing.T) {
	t.Parallel() // M16 T1:独立 e2e(各自 TempDir)并行化
	extDir := t.TempDir()
	releaseExt(t, extDir, "tool-basic", "tool-workflow")
	c, _ := buildExternalEnv(t, extDir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "workflow", mustJSON2(t, map[string]any{
		"script":     `result = {"out": "bg-ext-ok"}`,
		"background": true,
	}))
	if err != nil || res.Error != "" {
		t.Fatalf("background 提交失败: err=%v res=%+v", err, res)
	}
	var job struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(res.Content), &job); err != nil || job.JobID == "" {
		t.Fatalf("应返回 job_id: %s", res.Content)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		col, err := tools.Execute(context.Background(), "workflow_collect", mustJSON2(t, map[string]any{"job_id": job.JobID}))
		if err != nil {
			t.Fatal(err)
		}
		if col.Error == "" && strings.Contains(col.Content, "bg-ext-ok") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("collect 超时: %s %s", col.Error, col.Content)
		}
		time.Sleep(80 * time.Millisecond)
	}
}

// TestExternalMCPBridge 外部 tool-mcp:MCP client 桥进程连接迷你 MCP server,
// 工具 mcp_greet 经桥协议注册到宿主并可调用。
func TestExternalMCPBridge(t *testing.T) {
	extDir := t.TempDir()
	releaseExt(t, extDir, "tool-basic", "tool-mcp")
	// 编译迷你 MCP server(tests/mcpserver)
	bin := filepath.Join(extDir, testutil.ExeName("mcpserver"))
	if err := runGoBuild(t, bin, "./mcpserver"); err != nil {
		t.Fatalf("编译 mcpserver: %v", err)
	}
	t.Setenv("GAH_HOME", t.TempDir()) // 隔离真实数据根:tool-mcp 会读 $GAH_HOME/config/mcp.yaml
	t.Setenv("GAH_MCP_COMMAND", bin)
	c, _ := buildExternalEnv(t, extDir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	def, ok := tools.Get("mcp_greet")
	if !ok {
		var names []string
		for _, d := range tools.List() {
			names = append(names, d.Name)
		}
		t.Fatalf("mcp_greet 应经 tool-mcp 注册,实际: %v", names)
	}
	if !strings.Contains(def.Description, "问候") {
		t.Fatalf("定义应来自 MCP server: %+v", def)
	}
	res, err := tools.Execute(context.Background(), "mcp_greet", `{"name":"世界"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || !strings.Contains(res.Content, "你好, 世界") {
		t.Fatalf("MCP 调用应回传: %+v", res)
	}
}

// TestExternalMCPBridgeMulti 外部 tool-mcp 多 server:GAH_MCP_COMMANDS 每行挂一个
// MCP server,工具按 mcp_<server>_<name> 注册且路由到各自 server 实例。-race 全绿。
func TestExternalMCPBridgeMulti(t *testing.T) {
	extDir := t.TempDir()
	releaseExt(t, extDir, "tool-basic", "tool-mcp")
	bin := filepath.Join(extDir, testutil.ExeName("mcpserver"))
	if err := runGoBuild(t, bin, "./mcpserver"); err != nil {
		t.Fatalf("编译 mcpserver: %v", err)
	}
	t.Setenv("GAH_HOME", t.TempDir()) // 隔离真实数据根(同上)
	t.Setenv("GAH_MCP_COMMANDS", "alpha="+bin+" -name alpha\nbeta="+bin+" -name beta")
	c, _ := buildExternalEnv(t, extDir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"mcp_alpha_greet": "alpha", "mcp_beta_greet": "beta"} {
		def, ok := tools.Get(name)
		if !ok {
			var names []string
			for _, d := range tools.List() {
				names = append(names, d.Name)
			}
			t.Fatalf("%s 应经 tool-mcp 注册,实际: %v", name, names)
		}
		if !strings.Contains(def.Description, want) {
			t.Fatalf("定义应来自 server %s: %+v", want, def)
		}
		res, err := tools.Execute(context.Background(), name, `{"name":"世界"}`)
		if err != nil {
			t.Fatal(err)
		}
		if res.Error != "" || !strings.Contains(res.Content, "你好, 世界!(via "+want+")") {
			t.Fatalf("%s 应路由到 server %s: %+v", name, want, res)
		}
	}
}

// TestExternalMCPConfigFile tool-mcp 读 $GAH_HOME/config/mcp.yaml(NOND-M1 第 2 步):
// direct 模式照旧全量注册;search 模式只暴露 mcp_search/mcp_call(工具清单不进固定前缀);
// 停用与连接失败的 server 跳过不影响其余;改完配置经 ctx.extplugins.Reload 热生效;
// 配置全坏 → 插件启动失败显式报错且宿主存活(旧工具已被撤销)。
func TestExternalMCPConfigFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	t.Setenv("GAH_MCP_COMMANDS", "")
	extDir := t.TempDir()
	releaseExt(t, extDir, "tool-basic", "tool-mcp")
	bin := filepath.Join(extDir, testutil.ExeName("mcpserver"))
	if err := runGoBuild(t, bin, "./mcpserver"); err != nil {
		t.Fatalf("编译 mcpserver: %v", err)
	}
	off := false
	if err := mcpconfig.Save([]mcpconfig.Server{
		{Name: "alpha", Command: bin + " -name alpha"},
		{Name: "big", Command: bin + " -name big", Mode: mcpconfig.ModeSearch},
		{Name: "off", Command: bin + " -name off", Enabled: &off},
		{Name: "dead", Command: filepath.Join(extDir, "no-such-server")},
	}); err != nil {
		t.Fatalf("写 mcp.yaml: %v", err)
	}
	c, _ := buildExternalEnv(t, extDir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, ok := tools.Get("mcp_alpha_greet"); !ok {
		t.Fatalf("direct 模式应全量注册: %v", registryNamesForTest(tools))
	}
	if _, ok := tools.Get("mcp_big_greet"); ok {
		t.Fatal("search 模式工具不得直接注册进工具表")
	}
	for _, n := range []string{"mcp_search", "mcp_call"} {
		if _, ok := tools.Get(n); !ok {
			t.Fatalf("search 模式应暴露 %s: %v", n, registryNamesForTest(tools))
		}
	}
	// 空查询 = 全量清单,应只含 search 模式 server 的工具
	res, err := tools.Execute(context.Background(), "mcp_search", `{"query":""}`)
	if err != nil || res.Error != "" {
		t.Fatalf("mcp_search 应可用: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "mcp_big_greet") || strings.Contains(res.Content, "mcp_alpha_greet") {
		t.Fatalf("索引应只含 search 模式工具: %s", res.Content)
	}
	// 检索 → 调用:路由到 big 实例
	res, err = tools.Execute(context.Background(), "mcp_call",
		mustJSON2(t, map[string]any{"name": "mcp_big_greet", "arguments": map[string]any{"name": "世界"}}))
	if err != nil || res.Error != "" {
		t.Fatalf("mcp_call 应成功: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "(via big)") {
		t.Fatalf("mcp_call 应路由到 search 模式 server: %s", res.Content)
	}

	// 热重载:改成只剩一个启动不了的 server → Reload 显式报错,宿主存活,旧工具已撤销
	var extp sdk.ExternalPlugins
	if err := c.Inject("ctx.extplugins", &extp); err != nil {
		t.Fatal(err)
	}
	if err := mcpconfig.Save([]mcpconfig.Server{{Name: "dead", Command: filepath.Join(extDir, "no-such-server")}}); err != nil {
		t.Fatal(err)
	}
	if err := extp.Reload("tool-mcp"); err == nil {
		t.Fatal("全部 server 不可用时重载应显式报错")
	}
	if _, ok := tools.Get("mcp_search"); ok {
		t.Fatal("重载失败后旧工具应已撤销(不留半死状态)")
	}
	// 宿主仍可用(插件失败不影响宿主)
	if _, ok := tools.Get("shell"); !ok {
		t.Fatal("宿主工具不应受外部插件失败影响")
	}
}

// registryNamesForTest 工具名清单(失败信息用)。
func registryNamesForTest(tools sdk.ToolRegistry) []string {
	var names []string
	for _, d := range tools.List() {
		names = append(names, d.Name)
	}
	return names
}

// TestExternalSubagent 外部 tool-subagent:子代理委派工具在独立进程注册(崩溃隔离),
// delegate 经宿主回调通道请求 fanout.agent——子代理(独立上下文)由 mock llm 驱动返回结论。
func TestExternalSubagent(t *testing.T) {
	t.Parallel() // M16 T1:独立 e2e(各自 TempDir)并行化
	extDir := t.TempDir()
	releaseExt(t, extDir, "tool-subagent")
	c, _ := buildExternalEnv(t, extDir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	def, ok := tools.Get("subagent")
	if !ok {
		var names []string
		for _, d := range tools.List() {
			names = append(names, d.Name)
		}
		t.Fatalf("subagent 应经 tool-subagent 注册,实际: %v", names)
	}
	if !strings.Contains(def.Description, "隔离") {
		t.Fatalf("定义应含上下文隔离语义: %+v", def)
	}
	res, err := tools.Execute(context.Background(), "subagent", mustJSON2(t, map[string]any{
		"action": "delegate",
		"task":   "独立评审这段设计",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("delegate 应成功(子代理经 mock 回结论): %s", res.Error)
	}
	if !strings.Contains(res.Content, "result") {
		t.Fatalf("delegate 应回传子代理结论: %s", res.Content)
	}
}

// TestExternalSubagentBackground 外部 tool-subagent 后台会话(M9.2):spawn 经回调宿主
// fanout.SpawnAgent(不阻塞),agents/agent_status 轮询至 done 取回结论。
func TestExternalSubagentBackground(t *testing.T) {
	t.Parallel() // M16 T1:独立 e2e(各自 TempDir)并行化
	extDir := t.TempDir()
	releaseExt(t, extDir, "tool-subagent")
	c, _ := buildExternalEnv(t, extDir)

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "subagent", mustJSON2(t, map[string]any{
		"action": "spawn",
		"task":   "后台独立任务",
	}))
	if err != nil || res.Error != "" {
		t.Fatalf("spawn 失败: err=%v res=%+v", err, res)
	}
	var sp struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(res.Content), &sp); err != nil || sp.AgentID == "" {
		t.Fatalf("spawn 应返回 agent_id: %s", res.Content)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := tools.Execute(context.Background(), "subagent", mustJSON2(t, map[string]any{
			"action":   "agent_status",
			"agent_id": sp.AgentID,
		}))
		if err != nil {
			t.Fatal(err)
		}
		var h sdk.AgentHandle
		if json.Unmarshal([]byte(st.Content), &h) == nil && h.State == sdk.AgentDone && h.Result != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("后台子代理轮询超时: %s", st.Content)
		}
		time.Sleep(80 * time.Millisecond)
	}
	// agents 列表应含该会话
	ls, err := tools.Execute(context.Background(), "subagent", mustJSON2(t, map[string]any{"action": "agents"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ls.Content, sp.AgentID) {
		t.Fatalf("agents 应含会话 %s: %s", sp.AgentID, ls.Content)
	}
}

// TestExternalSubagentFork 外部 tool-subagent fork(M9.3):宿主 ctx.sessions 已有父历史,
// fork 经回调通道请求 fanout.Fork(种入父历史后台启动)并轮询至 done 取回结论。
func TestExternalSubagentFork(t *testing.T) {
	t.Parallel() // M16 T1:独立 e2e(各自 TempDir)并行化
	extDir := t.TempDir()
	releaseExt(t, extDir, "tool-subagent")
	c, _ := buildExternalEnv(t, extDir)

	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	// 父会话已有历史(fork 种入源;模型可见=已记录)
	if err := sessions.Append(sdk.SessionEvent{Kind: sdk.EventUserMessage, Payload: sdk.UserMessage{Content: "父级问题"}}); err != nil {
		t.Fatal(err)
	}
	if err := sessions.Append(sdk.SessionEvent{Kind: sdk.EventAssistantMessage, Payload: sdk.AssistantMessage{Content: "父级回答"}}); err != nil {
		t.Fatal(err)
	}

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "subagent", mustJSON2(t, map[string]any{
		"action": "fork",
		"task":   "在父上下文基础上继续",
	}))
	if err != nil || res.Error != "" {
		t.Fatalf("fork 失败: err=%v res=%+v", err, res)
	}
	var sp struct {
		AgentID string `json:"agent_id"`
	}
	if err := json.Unmarshal([]byte(res.Content), &sp); err != nil || sp.AgentID == "" {
		t.Fatalf("fork 应返回 agent_id: %s", res.Content)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, err := tools.Execute(context.Background(), "subagent", mustJSON2(t, map[string]any{
			"action":   "agent_status",
			"agent_id": sp.AgentID,
		}))
		if err != nil {
			t.Fatal(err)
		}
		var h sdk.AgentHandle
		if json.Unmarshal([]byte(st.Content), &h) == nil && h.State == sdk.AgentDone && h.Result != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fork 子代理轮询超时: %s", st.Content)
		}
		time.Sleep(80 * time.Millisecond)
	}
}

// runGoBuild 编译测试辅助(相对 tests/ 包目录)。
func runGoBuild(t *testing.T, out, pkg string) error {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = "."
	outB, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, outB)
	}
	return nil
}

func mustJSON2(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestExternalPathCapabilityDeclared 能力化路径沙箱的**全链路**回归:
// 外部 tool-basic(tool-files 四件套)→ 桥协议(defDTO)→ 宿主注册表声明 →
// policy-guard 按声明 veto 越界写。任一环丢字段(声明不下发/注册表不带/guard 不读)
// 这一步都会失败——此前登记的缺口正是"自定义名与表外工具不受路径沙箱约束"。
func TestExternalPathCapabilityDeclared(t *testing.T) {
	t.Parallel()
	extDir := t.TempDir()
	releaseExt(t, extDir, "tool-basic")
	c, _ := buildExternalEnv(t, extDir, config.Entry{ID: "policy-guard"})

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// ① 外部插件自述的路径声明跨桥到达宿主注册表
	def, ok := tools.Get("file_write")
	if !ok {
		t.Fatal("外部 file_write 应已注册")
	}
	if len(def.PathParams) == 0 {
		t.Fatal("外部工具的路径参数声明未跨桥传递(能力化沙箱失效)")
	}
	if got := def.PathParams[0]; got.Arg != "path" || got.Access != sdk.PathWrite {
		t.Fatalf("声明内容不符: %+v", got)
	}
	// ② 越界写按声明被宿主 pre-execute veto(工具未真正执行)
	outside := filepath.Join(t.TempDir(), "leak.txt")
	res, err := tools.Execute(context.Background(), "file_write",
		mustJSON2(t, map[string]any{"path": outside, "content": "x"}))
	if err != nil {
		t.Fatalf("veto 应为结构化结果而非硬错误: %v", err)
	}
	if res.Error == "" {
		t.Fatal("越界写应被路径沙箱 veto")
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Fatal("被 veto 的写不应落盘")
	}
}

// TestExternalFileChangeLandsInLedger 变更审查面的**发行态**全链路回归(S-P1-1 缺口):
// 默认装配里 tool-files 是外部进程(enabled:false,由 tool-basic 提供)→ 它没有宿主 Ctx,
// file/change 只能经回调通道(change.record)回传。这条链断了不会报任何错,只会让
// 变更视图 / `/diff` / Web 变更卡**恒为空**(2026-09-18 实测踩到)—— 故用真实 embed 产物钉住:
// 外部 file_write → 桥回传 → 宿主账本(含宿主补算的 Rel 与 patch 统计)。
func TestExternalFileChangeLandsInLedger(t *testing.T) {
	t.Parallel()
	extDir := t.TempDir()
	releaseExt(t, extDir, "tool-basic")
	// policy-guard 提供 ctx.sandbox(工作区根);host-session-log 已在 buildExternalEnv 装好
	c, _ := buildExternalEnv(t, extDir, config.Entry{ID: "policy-guard"})

	ws := t.TempDir()
	if _, err := c.Emit(context.Background(), "cwd/workspace-switched", ws, sdk.Emit); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	var sess sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sess); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(ws, "note.txt")
	res, err := tools.Execute(context.Background(), "file_write",
		mustJSON2(t, map[string]any{"path": target, "content": "第一行\n第二行\n"}))
	if err != nil {
		t.Fatalf("外部 file_write 执行失败: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("外部 file_write 应成功: %s", res.Error)
	}
	// 回传是同步 RPC(写盘工具在返回前调用),写盘结果拿到时事件应已在账本里
	var got []sdk.FileChangeEvent
	for _, ev := range sess.Replay() {
		if ev.Kind != sdk.EventFileChange {
			continue
		}
		fc, ok := sdk.FileChangeFrom(ev.Payload)
		if !ok {
			t.Fatalf("file/change 载荷类型不符: %T", ev.Payload)
		}
		got = append(got, fc)
	}
	if len(got) != 1 {
		t.Fatalf("账本应有 1 条 file/change(外部工具改动经桥落账),得到 %d 条", len(got))
	}
	fc := got[0]
	if fc.Path != target || fc.Op != "write" || fc.Tool != "file_write" {
		t.Fatalf("事件字段不符: %+v", fc)
	}
	if fc.Rel != "note.txt" {
		t.Fatalf("宿主应按自己的沙箱根补算 Rel: %q", fc.Rel)
	}
	if !fc.Created || fc.Added != 2 || fc.Removed != 0 {
		t.Fatalf("新建文件统计不符: %+v", fc)
	}
	if !strings.Contains(fc.Diff, "+第一行") || fc.Bytes != len("第一行\n第二行\n") {
		t.Fatalf("patch/字节数不符: %+v", fc)
	}
	// 二次改动:同一文件再写一次 → 追加第二条(审查视图按路径聚合)
	if _, err := tools.Execute(context.Background(), "file_write",
		mustJSON2(t, map[string]any{"path": target, "content": "第一行\n改过的第二行\n"})); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ev := range sess.Replay() {
		if ev.Kind == sdk.EventFileChange {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("同文件二次改动应再记一条(不是覆盖): %d 条", n)
	}
}
