// host-roles 集成测试:装配真实 host-tools/host-system-prompt/host-skills,
// 验证「切角色 → 系统提示身份槽 + 技能可见集合」同时变化,以及只读工具与 /role 命令。
package hostroles

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-commands"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-skills"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// writeSkillDoc 写一个技能(目录 + SKILL.md 摘要)。
func writeSkillDoc(t *testing.T, dir, name, desc string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: " + name + "\ndescription: " + desc + "\ntrigger:\n  - " + name + "\n---\n正文-" + name
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// harness 装配一套真实插件组合,返回角色服务与各服务句柄。
type harness struct {
	home  string
	svc   *Service
	tools sdk.ToolRegistry
	sp    sdk.SystemPromptService
	cmds  sdk.CommandRegistry
}

func newHarness(t *testing.T, withCommands bool) *harness {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	for _, p := range []sdk.Plugin{&hosttools.Plugin{}, &hostsystemprompt.Plugin{}, &hostskills.Plugin{}} {
		if _, err := p.Start(c, &sdk.Manifest{}); err != nil {
			t.Fatalf("%s Start: %v", p.Name(), err)
		}
	}
	if withCommands {
		if _, err := (&hostcommands.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatalf("host-roles Start: %v", err)
	}
	h := &harness{home: home}
	if err := c.Inject("ctx.roles", &h.svc); err != nil {
		t.Fatal(err)
	}
	if err := c.Inject("ctx.tools", &h.tools); err != nil {
		t.Fatal(err)
	}
	if err := c.Inject("ctx.systemPrompt", &h.sp); err != nil {
		t.Fatal(err)
	}
	if withCommands {
		if err := c.Inject("ctx.commands", &h.cmds); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// systemText 当前系统提示正文(单条 system 消息)。
func (h *harness) systemText() string {
	msgs := h.sp.Assemble(nil, nil)
	if len(msgs) == 0 {
		return ""
	}
	return msgs[0].Content
}

func TestRoleSwitchesPromptAndSkills(t *testing.T) {
	h := newHarness(t, true)
	shared := filepath.Join(h.home, "skills")
	writeSkillDoc(t, shared, "skill-a", "共享技能A")
	writeSkillDoc(t, shared, "skill-b", "共享技能B")

	// 先建角色(挂载清单显式替换默认池):skill-a 挂上,skill-b 不挂。
	spec, err := h.svc.Create(sdk.RoleSpec{
		ID: "finance", Name: "财务", Identity: "你是财务分析师。",
		Skills: []string{"skill-a"}, SkillsSet: true, ExcludeGlobal: true,
	}, "先确认口径再计算。\n")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if spec.AGENTS == "" || spec.AGENTSBytes == 0 {
		t.Fatalf("新建角色应带 AGENTS.md 模板: %+v", spec)
	}
	if err := h.svc.SetAgents("finance", "先确认口径再计算。\n"); err != nil {
		t.Fatal(err)
	}
	// 技能必须先被扫描到:创建角色时 skills 库还没扫描到刚写入的文件 → 重扫一次
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}

	// 基线(无角色):两个共享技能都可见,无角色身份块
	base := h.systemText()
	if strings.Contains(base, "当前角色:") {
		t.Fatalf("未启用角色不应有身份块: %.300s", base)
	}
	if !strings.Contains(base, "skill-a") || !strings.Contains(base, "skill-b") {
		t.Fatalf("基线应看到全部共享技能: %.400s", base)
	}

	// 切到财务角色:身份槽 + AGENTS.md 出现;技能只剩 skill-a
	if err := h.svc.Use("finance"); err != nil {
		t.Fatalf("Use: %v", err)
	}
	txt := h.systemText()
	if !strings.Contains(txt, "当前角色:财务(finance)") {
		t.Fatalf("身份槽未注入: %.300s", txt)
	}
	if !strings.Contains(txt, "你是财务分析师。") || !strings.Contains(txt, "先确认口径再计算。") {
		t.Fatalf("身份句/工作规则未注入: %.500s", txt)
	}
	if !strings.Contains(txt, "skill-a") || strings.Contains(txt, "skill-b") {
		t.Fatalf("显式挂载应替换默认池: %.500s", txt)
	}
	// 未挂载技能不可读(与索引一致,显式报"未挂载")
	res, _ := h.tools.Execute(context.Background(), "read_skill", `{"name":"skill-b"}`)
	if !strings.Contains(res.Content, "未挂载") {
		t.Fatalf("未挂载技能应报未挂载: %s", res.Content)
	}
	resOK, _ := h.tools.Execute(context.Background(), "read_skill", `{"name":"skill-a"}`)
	if !strings.Contains(resOK.Content, "正文-skill-a") {
		t.Fatalf("已挂载技能应可读: %s", resOK.Content)
	}

	// 只读工具:list_roles / read_role
	lr, err := h.tools.Execute(context.Background(), "list_roles", "{}")
	if err != nil || lr.Error != "" {
		t.Fatalf("list_roles: %v %+v", err, lr)
	}
	if !strings.Contains(lr.Content, "finance") || !strings.Contains(lr.Content, `"current":"finance"`) {
		t.Fatalf("list_roles 内容不符: %s", lr.Content)
	}
	rr, _ := h.tools.Execute(context.Background(), "read_role", "{}")
	if !strings.Contains(rr.Content, "你是财务分析师。") {
		t.Fatalf("read_role 应读当前角色: %s", rr.Content)
	}

	// 停用 → 回到基线与默认池
	if err := h.svc.Use("none"); err != nil {
		t.Fatalf("Use(none): %v", err)
	}
	if h.svc.Current() != "" {
		t.Fatalf("停用后 Current = %q", h.svc.Current())
	}
	txt2 := h.systemText()
	if strings.Contains(txt2, "当前角色:") {
		t.Fatalf("停用后不应有身份块: %.300s", txt2)
	}
	if !strings.Contains(txt2, "skill-b") {
		t.Fatalf("停用后应回到默认池: %.400s", txt2)
	}
}

func TestExcludeGlobalInstructions(t *testing.T) {
	h := newHarness(t, false)
	// 全局指令(用户级 AGENTS.md)写入一个哨兵串(需 /reload 等效:启动时已读过一次)
	if err := os.WriteFile(filepath.Join(h.home, "AGENTS.md"), []byte("全局哨兵-GLOBAL-MARK"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rl, ok := h.sp.(sdk.ReloadableInstructions); ok {
		if err := rl.ReloadInstructions(); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal("systemPrompt 应实现 ReloadableInstructions")
	}
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "dev-role"}, "规则正文"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "no-global", ExcludeGlobal: true}, "规则正文2"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.systemText(), "全局哨兵-GLOBAL-MARK") {
		t.Fatal("基线应注入全局指令")
	}
	if err := h.svc.Use("dev-role"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.systemText(), "全局哨兵-GLOBAL-MARK") {
		t.Fatal("未声明 exclude_global 的角色应保留全局指令")
	}
	if !h.svc.InheritGlobalInstructions() {
		t.Fatal("dev-role 应继承全局指令")
	}
	if err := h.svc.Use("no-global"); err != nil {
		t.Fatal(err)
	}
	if h.svc.InheritGlobalInstructions() {
		t.Fatal("no-global 不应继承全局指令")
	}
	if strings.Contains(h.systemText(), "全局哨兵-GLOBAL-MARK") {
		t.Fatalf("exclude_global 角色不应注入全局指令: %.300s", h.systemText())
	}
	// 角色自身的规则不受影响
	if !strings.Contains(h.systemText(), "规则正文2") {
		t.Fatal("角色规则应仍然注入")
	}
}

func TestPrivateSkillsIsolation(t *testing.T) {
	h := newHarness(t, false)
	shared := filepath.Join(h.home, "skills")
	writeSkillDoc(t, shared, "skill-a", "共享技能A")
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "r1"}, "规则1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "r2"}, "规则2"); err != nil {
		t.Fatal(err)
	}
	// r1 私有技能
	priv := filepath.Join(h.home, "roles", "r1", "skills")
	writeSkillDoc(t, priv, "priv-1", "r1 私有技能")
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}
	// r1 视角:私有 + 共享
	if err := h.svc.Use("r1"); err != nil {
		t.Fatal(err)
	}
	txt := h.systemText()
	if !strings.Contains(txt, "priv-1") || !strings.Contains(txt, "skill-a") {
		t.Fatalf("r1 应看到私有与共享技能: %.400s", txt)
	}
	spec, _ := h.svc.Get("r1")
	if len(spec.OwnSkills) != 1 || spec.OwnSkills[0] != "priv-1" {
		t.Fatalf("OwnSkills = %v", spec.OwnSkills)
	}
	// r2 视角:看不到 r1 的私有技能
	if err := h.svc.Use("r2"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.systemText(), "priv-1") {
		t.Fatalf("r2 不应看到 r1 私有技能: %.400s", h.systemText())
	}
	res, _ := h.tools.Execute(context.Background(), "read_skill", `{"name":"priv-1"}`)
	if !strings.Contains(res.Content, "未挂载") {
		t.Fatalf("r2 读 r1 私有技能应被拒: %s", res.Content)
	}
	// 停用角色(基线):角色私有技能一律不可见
	if err := h.svc.Use("none"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h.systemText(), "priv-1") {
		t.Fatal("基线不应看到任何角色私有技能")
	}
}

func TestUseValidationAndRollback(t *testing.T) {
	h := newHarness(t, false)
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "ok-role"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Use("ghost"); err == nil {
		t.Fatal("切换不存在的角色应失败")
	}
	if h.svc.Current() != "" {
		t.Fatalf("失败不应改状态: Current = %q", h.svc.Current())
	}
	if err := h.svc.Use("ok-role"); err != nil {
		t.Fatal(err)
	}
	// 幂等:重复切换同一角色不报错
	if err := h.svc.Use("ok-role"); err != nil {
		t.Fatal(err)
	}
	// 当前角色不可删
	if err := h.svc.Delete("ok-role"); err == nil {
		t.Fatal("删除当前角色应失败")
	}
	// 挂载不存在的技能:显式失败
	if _, err := h.svc.Update("ok-role", sdk.RoleSpec{ID: "ok-role", Skills: []string{"nope"}, SkillsSet: true}); err == nil {
		t.Fatal("挂载不存在的技能应失败")
	}
	// ID 不可经 Update 偷改
	if _, err := h.svc.Update("ok-role", sdk.RoleSpec{ID: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.svc.Get("other"); ok {
		t.Fatal("Update 不应改 ID")
	}
}

func TestRoleCommand(t *testing.T) {
	h := newHarness(t, true)
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "writer", Name: "撰稿人"}, "规则正文"); err != nil {
		t.Fatal(err)
	}
	spec, ok := h.cmds.Get("role")
	if !ok {
		t.Fatal("/role 未注册")
	}
	out, err := spec.Run(nil)
	if err != nil || !strings.Contains(out, "writer") {
		t.Fatalf("/role list: %v %s", err, out)
	}
	// use
	if out, err = spec.Run([]string{"use", "writer"}); err != nil || !strings.Contains(out, "已切换到角色") {
		t.Fatalf("/role use: %v %s", err, out)
	}
	if h.svc.Current() != "writer" {
		t.Fatalf("Current = %q", h.svc.Current())
	}
	// show
	if out, err = spec.Run([]string{"show"}); err != nil || !strings.Contains(out, "规则正文") {
		t.Fatalf("/role show: %v %s", err, out)
	}
	// 省略 use
	if out, err = spec.Run([]string{"none"}); err != nil || !strings.Contains(out, "已停用角色") {
		t.Fatalf("/role none: %v %s", err, out)
	}
	// new + rm
	if out, err = spec.Run([]string{"new", "temp-role", "临时"}); err != nil || !strings.Contains(out, "已新建角色") {
		t.Fatalf("/role new: %v %s", err, out)
	}
	if out, err = spec.Run([]string{"rm", "temp-role"}); err != nil || !strings.Contains(out, ".trash") {
		t.Fatalf("/role rm: %v %s", err, out)
	}
	// 非法 ID
	if _, err := spec.Run([]string{"new", "Bad_ID"}); err == nil {
		t.Fatal("非法 ID 应失败")
	}
	// rename
	if out, err = spec.Run([]string{"rename", "writer", "writer2", "撰稿人2"}); err != nil || !strings.Contains(out, "writer2") {
		t.Fatalf("/role rename: %v %s", err, out)
	}
}

func TestBlockTextTruncation(t *testing.T) {
	h := newHarness(t, false)
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "big"}, ""); err != nil {
		t.Fatal(err)
	}
	// 直接写超限正文:SetAgents 拒绝 → 绕过走磁盘(模拟用户手改)
	huge := strings.Repeat("规则行\n", 20000)
	if err := os.WriteFile(filepath.Join(h.home, "roles", "big", "AGENTS.md"), []byte(huge), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Use("big"); err != nil {
		t.Fatal(err)
	}
	txt := h.systemText()
	if len(txt) > 64*1024 {
		t.Fatalf("身份槽未按上限截断: %d 字节", len(txt))
	}
	if !strings.Contains(txt, "已截断") {
		t.Fatal("截断应显式标注")
	}
	// 身份槽块本身必须被截到上限内(其余部分是项目/全局指令,不计入此断言):
	// 从角色块头到截断标注的距离就是实际注入长度。
	head := strings.Index(txt, "当前角色:big")
	if head < 0 {
		t.Fatalf("未找到角色块: %.200s", txt)
	}
	rest := txt[head:]
	cut := strings.Index(rest, "已截断")
	if cut < 0 {
		t.Fatal("截断标注不在角色块内")
	}
	if cut > 34*1024 {
		t.Fatalf("角色块超上限: %d 字节", cut)
	}
}
