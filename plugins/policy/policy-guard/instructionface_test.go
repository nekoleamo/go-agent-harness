// S1/S2 指令面写审批单测:角色/技能/全局 AGENTS.md 的写入按审批档裁决。
//
// 背景:这些文件逐字进系统提示(或成为模型可读指令)—— 不可信内容若能落地到这里,
// 就从“一次注入”变成持久提权。凭据面是硬拒;指令面走审批档(有正当用途)。
package policyguard

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// writeProbe 最小写工具替身(显式声明 path 参数为写意图)。
type writeProbe struct{}

func (w *writeProbe) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{
		Name:        "probe_write",
		Description: "写文件(测试替身)",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		PathParams:  []sdk.PathParam{{Arg: "path", Access: sdk.PathWrite}},
	}
}

func (w *writeProbe) Execute(_ context.Context, args string) (any, error) { return "written", nil }

// shellProbe 名为 shell 的替身(发行态 shell 由外部插件提供,单测里自备一份才能走 shell 支路)。
type shellProbe struct{}

func (w *shellProbe) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "shell", Description: "执行命令(测试替身)", InputSchema: map[string]any{"type": "object"}}
}

func (w *shellProbe) Execute(_ context.Context, args string) (any, error) { return "ok", nil }

// probePlugin 注册写替身与 shell 替身。
type probePlugin struct{}

func (p *probePlugin) Name() string { return "probe-write" }

func (p *probePlugin) Start(c sdk.Ctx, _ *sdk.Manifest) (sdk.Disposer, error) {
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		return nil, err
	}
	d1 := tools.Register(&writeProbe{})
	d2 := tools.Register(&shellProbe{})
	return func() { d1(); d2() }, nil
}

// buildProbe 装配 host-tools + policy-guard(data)+ 写替身;返回 ctx 与 GAH_HOME。
// 注意:workspace-write 档下写 $GAH_HOME 会先被**路径裁决**拒(挡在审批之前) ——
// 要单看审批层的语义,用例必须用 full-access 放开路径层。
func buildProbe(t *testing.T, confirm sdk.ConfirmService, approval, sandbox string) (sdk.Ctx, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	data := map[string]any{"approval": approval, "sandbox": sandbox, "sync": false}
	c := buildTools(t, confirm, data)
	if _, err := (&probePlugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	return c, home
}

func TestInstructionFaceWriteApproval(t *testing.T) {
	rec := &recordingConfirm{resp: false}
	c, home := buildProbe(t, rec, "smart", "full-access")
	roleFile := filepath.Join(home, "roles", "finance", "AGENTS.md")
	res := execTool(t, c, "probe_write", fmt.Sprintf(`{"path":%q}`, roleFile))
	if res.Error == "" {
		t.Fatal("smart 档用户拒绝后应拦截角色文件写入")
	}
	if len(rec.prompts) != 1 || !strings.Contains(rec.prompts[0], "指令面") || !strings.Contains(rec.prompts[0], roleFile) {
		t.Fatalf("确认提示应说清在写指令面与目标路径: %q", rec.prompts)
	}

	// 批准 → 放行
	c2, home2 := buildProbe(t, &recordingConfirm{resp: true}, "smart", "full-access")
	if res := execTool(t, c2, "probe_write", fmt.Sprintf(`{"path":%q}`, filepath.Join(home2, "roles", "x", "role.yaml"))); res.Error != "" {
		t.Fatalf("批准后应放行: %+v", res)
	}

	// strict → 直接拒
	c3, home3 := buildProbe(t, &recordingConfirm{resp: true}, "strict", "full-access")
	if res := execTool(t, c3, "probe_write", fmt.Sprintf(`{"path":%q}`, filepath.Join(home3, "AGENTS.md"))); res.Error == "" || !strings.Contains(res.Error, "严格档") {
		t.Fatalf("strict 档应拒绝写全局指令: %+v", res)
	}

	// open → 放行(开放档语义:你在场时不用问)
	c4, home4 := buildProbe(t, &recordingConfirm{resp: false}, "open", "full-access")
	if res := execTool(t, c4, "probe_write", fmt.Sprintf(`{"path":%q}`, filepath.Join(home4, "skills", "s", "SKILL.md"))); res.Error != "" {
		t.Fatalf("open 档应放行: %+v", res)
	}
}

func TestInstructionFaceNotTriggeredForOrdinaryPaths(t *testing.T) {
	rec := &recordingConfirm{resp: false}
	// 沙箱全开:确保唯一可能拦下的是审批层,而不是路径档位
	c, _ := buildProbe(t, rec, "smart", "full-access")
	ws := t.TempDir()
	if res := execTool(t, c, "probe_write", fmt.Sprintf(`{"path":%q}`, filepath.Join(ws, "a.txt"))); res.Error != "" {
		t.Fatalf("普通工作区文件不应被指令面审批拦: %+v", res)
	}
	if len(rec.prompts) != 0 {
		t.Fatalf("普通路径不应弹确认: %q", rec.prompts)
	}
	// 数据根下但非指令面(config 由凭据面管、jail 是临时区)→ 不由此路拦
	if res := execTool(t, c, "probe_write", fmt.Sprintf(`{"path":%q}`, filepath.Join(sdk.Home(), "jail", "x"))); res.Error != "" {
		t.Fatalf("jail 下写入不应被指令面审批拦: %+v", res)
	}
	if len(rec.prompts) != 0 {
		t.Fatalf("jail 下写入不应弹确认: %q", rec.prompts)
	}
}

func TestInstructionFaceShellWriteApproval(t *testing.T) {
	rec := &recordingConfirm{resp: false}
	c, home := buildProbe(t, rec, "smart", "full-access")
	cmd := fmt.Sprintf("printf x >> %s", filepath.Join(home, "roles", "finance", "AGENTS.md"))
	res := execTool(t, c, "shell", fmt.Sprintf(`{"command":%q}`, cmd))
	if res.Error == "" {
		t.Fatal("smart 档应拦下向角色文件追加内容的 shell 写")
	}
	if len(rec.prompts) == 0 || !strings.Contains(rec.prompts[0], "指令面") {
		t.Fatalf("shell 写指令面应弹确认并说明罪名: %q", rec.prompts)
	}
}

func TestInstructionFaceLabelUnit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	cases := []struct {
		path string
		hit  bool
	}{
		{filepath.Join(home, "AGENTS.md"), true},
		{filepath.Join(home, "roles", "a", "AGENTS.md"), true},
		{filepath.Join(home, "roles", "a", "skills", "s", "SKILL.md"), true},
		{filepath.Join(home, "skills", "s", "SKILL.md"), true},
		{filepath.Join(home, "roles"), true},
		{filepath.Join(home, "jail", "x"), false},
		{filepath.Join(home, "sessions", "a.jsonl"), false},
		{filepath.Join(home, "config", "gah-state.json"), false},
		{filepath.Join(home, "AGENTS.override.md"), false},
	}
	for _, tc := range cases {
		_, hit := instructionFaceWriteLabel(tc.path)
		if hit != tc.hit {
			t.Errorf("instructionFaceWriteLabel(%s) = %v, want %v", tc.path, hit, tc.hit)
		}
	}
}
