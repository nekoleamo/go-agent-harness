package hostroles

// 角色的可见性按**会话**生效(第一百一十六批)。
//
// 要钉的是页签的核心承诺:A 页签用"代码评审"角色(排除某些工具与技能)、B 页签用
// "数据分析"角色时,两者的工具清单、技能可见集合必须**各按各的** —— 相同就说明
// 过滤还在读全局当前角色,角色的收窄等于静默失效。

import (
	"context"
	"encoding/json"

	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	hostskills "github.com/nekoleamo/go-agent-harness/plugins/host/host-skills"
	hostsystemprompt "github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	hosttools "github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// keys 取集合的排序键列表(用于比较两个清单是否真不同)。
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// prefsStub 会话偏好桩:按会话给出角色 id。
type prefsStub struct {
	sdk.CwdSessions
	bySession map[string]string
}

func (p *prefsStub) SessionPrefsOf(id string) sdk.SessionPrefs {
	return sdk.SessionPrefs{Role: p.bySession[id]}
}

type echoTool struct{ def sdk.ToolDefinition }

func (e echoTool) Definition() sdk.ToolDefinition { return e.def }
func (e echoTool) Execute(context.Context, string) (any, error) {
	return map[string]any{"ok": true}, nil
}

// setup 装配 tools + skills + roles,并造两个各排除一工具/一技能的角色。
func setup(t *testing.T, bySession map[string]string) (sdk.ToolRegistry, *Service) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)

	// 技能:目录式 <dir>/<name>/SKILL.md(与真实扫描一致 —— 早先写成平铺 .md,扫不到)
	skillsDir := filepath.Join(home, "skills")
	for _, n := range []string{"s-alpha", "s-beta"} {
		d := filepath.Join(skillsDir, n)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		doc := "---\nname: " + n + "\ndescription: 技能 " + n + "\n---\n正文"
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	for _, p := range []sdk.Plugin{&hostsystemprompt.Plugin{}, &hosttools.Plugin{}, &hostskills.Plugin{}} {
		if _, err := p.Start(c, &sdk.Manifest{}); err != nil {
			t.Fatalf("%s Start: %v", p.Name(), err)
		}
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(echoTool{def: sdk.ToolDefinition{Name: "alpha", Description: "A"}})
	tools.Register(echoTool{def: sdk.ToolDefinition{Name: "beta", Description: "B"}})

	// 会话偏好(角色按会话)要先 Provide —— roles 用懒解析读它
	if err := c.Provide("ctx.cwdSessions", &prefsStub{bySession: bySession}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	var rs sdk.RoleService
	if err := c.Inject("ctx.roles", &rs); err != nil {
		t.Fatal(err)
	}
	svc := rs.(*Service)

	// 角色走真实创建路径(手写 role.yaml 会把测试绑在文件格式上)
	if _, err := svc.Create(sdk.RoleSpec{ID: "r1", Name: "排除 beta", ToolsExclude: []string{"beta"}, Skills: []string{"s-alpha"}, SkillsSet: true}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(sdk.RoleSpec{ID: "r2", Name: "排除 alpha", ToolsExclude: []string{"alpha"}, Skills: []string{"s-beta"}, SkillsSet: true}, ""); err != nil {
		t.Fatal(err)
	}
	return tools, svc
}

// TestToolVisibilityPerSession 两个会话看到各自的工具清单。
func TestToolVisibilityPerSession(t *testing.T) {
	tools, svc := setup(t, map[string]string{"sA": "r1", "sB": "r2"})
	if sp, ok := svc.Get("r1"); !ok {
		t.Fatal("角色 r1 没加载出来")
	} else if sdk.ToolVisible(&sp, "beta") {
		t.Fatalf("角色定义自检失败:r1 应排除 beta,实得 %+v", sp.ToolsExclude)
	}
	cc, ok := tools.(sdk.ContextualToolCatalogue)
	if !ok {
		t.Fatal("host-tools 未实现 sdk.ContextualToolCatalogue(按会话过滤)")
	}
	ctxA := sdk.WithSessionContext(context.Background(), "sA")
	ctxB := sdk.WithSessionContext(context.Background(), "sB")

	if got := sdk.SessionFromContext(ctxA); got != "sA" {
		t.Fatalf("测试自身的问题:ctx 里没带会话 id(得 %q)", got)
	}
	names := func(ctx context.Context) map[string]bool {
		out := map[string]bool{}
		for _, d := range cc.ListFor(ctx) {
			out[d.Name] = true
		}
		return out
	}
	a, b := names(ctxA), names(ctxB)
	if a["beta"] {
		t.Fatalf("会话 A 的角色 r1 排除了 beta,它不该看到 beta:%v", a)
	}
	if b["alpha"] {
		t.Fatalf("会话 B 的角色 r2 排除了 alpha,它不该看到 alpha:%v", b)
	}
	// 反向:两个会话的清单必须**内容不同**(各排除一个 ⇒ 个数相同是正常的,
	// 判据放在内容上 —— 用数量判会把"刚好各排一个"的正确实现判成错)
	if len(keys(a)) == len(keys(b)) && keys(a)[0] == keys(b)[0] {
		t.Fatalf("两个会话的工具清单应各不相同:A=%v B=%v", keys(a), keys(b))
	}
	// 未设角色的会话:跟随全局(基线)⇒ 两个工具都在
	none := names(context.Background())
	if !none["alpha"] || !none["beta"] {
		t.Fatalf("没设角色的会话应看到全部工具,得 %v", none)
	}
}

// TestSkillVisibilityPerSession 技能可见性同样按会话(经 list_skills 工具,真实链路)。
func TestSkillVisibilityPerSession(t *testing.T) {
	tools, _ := setup(t, map[string]string{"sA": "r1", "sB": "r2"})
	run := func(ctx context.Context) string {
		res, err := tools.Execute(ctx, "list_skills", "{}")
		if err != nil {
			t.Fatalf("list_skills: %v", err)
		}
		b, _ := json.Marshal(res.Content)
		return string(b)
	}
	a := run(sdk.WithSessionContext(context.Background(), "sA"))
	b := run(sdk.WithSessionContext(context.Background(), "sB"))
	if strings.Contains(a, "s-beta") {
		t.Fatalf("会话 A 的角色排除了 s-beta,列表里不该有它:%s", a)
	}
	if strings.Contains(b, "s-alpha") {
		t.Fatalf("会话 B 的角色排除了 s-alpha,列表里不该有它:%s", b)
	}
	if !strings.Contains(a, "s-alpha") || !strings.Contains(b, "s-beta") {
		t.Fatalf("各自该看到的技能应还在:A=%s B=%s", a, b)
	}
}

// TestRoleForSession 角色按会话解析;没设的会话跟随全局当前角色。
func TestRoleForSession(t *testing.T) {
	setup(t, map[string]string{"sA": "r1"})
	_, svc := setup(t, map[string]string{"sA": "r1"})
	if got := svc.RoleForSession("sA"); got != "r1" {
		t.Fatalf("会话 sA 应解析到 r1,得 %q", got)
	}
	if got := svc.RoleForSession("sB"); got != "" {
		t.Fatalf("没设角色的会话应为空(跟随全局),得 %q", got)
	}
}
