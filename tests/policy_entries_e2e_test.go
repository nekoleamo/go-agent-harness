// 工具执行入口不变式(②-4,端到端):把「所有工具执行都经 host-tools registry →
// tools/pre-execute」从"实现约定"变成"可回归测试"。
//
// 覆盖三层:
//  1. registry 必发 pre-execute,且 veto 订阅者能让工具**根本不执行**(副作用不发生);
//  2. 端到端(agent-loop 入口):模型请求的越界写命令被路径裁决拦截,结构化错误经
//     tool 消息回传模型(模型可见即已记录);
//  3. 同命令在 full-access 下必须放行 —— 证明上一步的拦截来自**路径层**,而非
//     mock/审批/其它层的误伤(灵敏度验证:去掉路径裁决该用例即失败)。
package tests

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildScriptEnv 与 buildTestEnv 同款装配,但注入自定义 mock 脚本(工具调用参数可控)。
func buildScriptEnv(t *testing.T, script []any) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock", Data: map[string]any{"script": script}},
		{ID: "tool-shell"},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "workspace-write", "sync": true}},
		{ID: "host-agent-loop"},
	})
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c
}

// TestShellPathVetoThroughAgentLoop 端到端:越界写被路径层拦截,且错误对模型可见;
// 切 full-access 后同命令放行(拦截来源归因验证)。
func TestShellPathVetoThroughAgentLoop(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.txt")
	// 进 shell 命令的路径必须过 ShellPath:Windows 反斜杠会被 sh 当转义符,
	// `echo leaked > C:\Users\a` 实写 `C:Usersa` —— 命令就绕开了越界判定。
	cmd := "echo leaked > " + testutil.ShellPath(outside)

	c := buildScriptEnv(t, []any{
		map[string]any{"tool": map[string]any{"name": "shell", "args": `{"command":` + jsonString(cmd) + `}`}},
		map[string]any{"text": "done", "finish": "stop"},
	})

	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	if err := loop.Run(context.Background(), "写一个工作区外的文件"); err != nil {
		t.Fatalf("turn 应正常结束(veto 是结构化结果,不中断回合): %v", err)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("workspace-write 下越界写必须被拦在工作区裁决层(文件不该存在): %s", outside)
	}

	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	var toolMsg string
	for _, m := range sessions.DeriveMessages() {
		if m.Role == sdk.RoleTool {
			toolMsg += m.Content
		}
	}
	if !strings.Contains(toolMsg, "sandbox") {
		t.Fatalf("越界写被拦后应有可识别的结构化错误回传模型(含 sandbox),got: %q", toolMsg)
	}

	// 灵敏度:显式切 full-access 后同命令必须放行(否则说明拦截并非来自路径层档位)
	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err != nil {
		t.Fatal(err)
	}
	sb.SetMode(sdk.SandboxFullAccess)
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "shell", `{"command":`+jsonString(cmd)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("full-access 下同命令应放行,got: %s", res.Error)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("full-access 下命令应真正执行(文件应存在): %v", err)
	}
}

// TestRegistryVetoPreventsSideEffect registry 层不变式:pre-execute 的 veto 必须
// 让工具**不被执行**(副作用不发生),而不只是回一个错误字符串。
func TestRegistryVetoPreventsSideEffect(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "marker.txt")
	c := buildScriptEnv(t, []any{map[string]any{"text": "noop", "finish": "stop"}})
	c.Subscribe("tools/pre-execute", func(_ context.Context, ev *sdk.Event) error {
		if call, ok := ev.Payload.(*sdk.ToolCallEvent); ok && call.Name == "shell" {
			return &policyErr{msg: "veto by test"}
		}
		return nil
	})
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	res, err := tools.Execute(context.Background(), "shell", `{"command":`+jsonString("echo x > "+marker)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" {
		t.Fatalf("veto 应转为结构化错误: %+v", res)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("被 veto 的工具不得产生副作用(文件不该存在)")
	}
}

// TestEffectiveModeFollowsApproval ②-1 的 SDK 侧契约:审批档联动驱动**有效档**
// (Web/TUI 展示与命令回显均以此为准;声明档保持不变)。
func TestEffectiveModeFollowsApproval(t *testing.T) {
	c := buildScriptEnv(t, []any{map[string]any{"text": "noop", "finish": "stop"}})

	var sb sdk.Sandbox
	if err := c.Inject("ctx.sandbox", &sb); err != nil {
		t.Fatal(err)
	}
	es, ok := sb.(sdk.EffectiveSandbox)
	if !ok {
		t.Fatal("policy-guard 沙箱应实现 sdk.EffectiveSandbox(展示与裁决共用有效档)")
	}
	var ap sdk.ApprovalService
	if err := c.Inject("ctx.approval", &ap); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		approval sdk.ApprovalMode
		want     sdk.SandboxMode
	}{
		{sdk.ApprovalSmart, sdk.SandboxWorkspace},
		{sdk.ApprovalOpen, sdk.SandboxFullAccess},
		{sdk.ApprovalStrict, sdk.SandboxReadOnly},
		{sdk.ApprovalSmart, sdk.SandboxWorkspace},
	}
	for _, tc := range cases {
		ap.SetMode(tc.approval)
		if got := es.EffectiveMode(); got != tc.want {
			t.Fatalf("approval=%s 时有效档应为 %s,got %s", tc.approval, tc.want, got)
		}
		// 声明档不被联动改写(展示层需要能区分"声明 vs 有效")
		if sb.Mode() != sdk.SandboxWorkspace {
			t.Fatalf("声明档应保持 workspace-write,got %s", sb.Mode())
		}
	}
}

// jsonString 把字符串编成 JSON 字面量(mock 脚本的 args 是 JSON 字符串)。
func jsonString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
