// host-roles 集成测试:装配真实 host-tools/host-system-prompt/host-skills,
// 验证「切角色 → 系统提示身份槽 + 技能可见集合」同时变化,以及只读工具与 /role 命令。
package hostroles

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	c     sdk.Ctx
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
	h := &harness{home: home, c: c}
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

// —— 角色目录变化后的技能索引一致性(第七十九批复查补)——
// 三处都是"角色目录动了、索引没跟上/校验过严"造成的:私有技能是随角色目录走的。

// TestRenameKeepsOwnSkillsVisible 改标识后,角色自己的私有技能不能失效。
// (Rename 会连 skills/ 一起改名,索引里那些技能的归属还停在旧 id → visibleFor 判不可见)
func TestRenameKeepsOwnSkillsVisible(t *testing.T) {
	h := newHarness(t, false)
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "aa", Name: "AA"}, ""); err != nil {
		t.Fatal(err)
	}
	writeSkillDoc(t, filepath.Join(h.home, "roles", "aa", "skills"), "own-a", "私有A")
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}
	if sp, _ := h.svc.Get("aa"); len(sp.OwnSkills) != 1 {
		t.Fatalf("改名前的私有技能应被发现: %+v", sp.OwnSkills)
	}
	if _, err := h.svc.Rename("aa", "bb", "BB"); err != nil {
		t.Fatal(err)
	}
	sp, ok := h.svc.Get("bb")
	if !ok {
		t.Fatal("改名后角色应存在")
	}
	if len(sp.OwnSkills) != 1 || len(sp.EffectiveSkills) != 1 {
		t.Fatalf("改名后私有技能应对自己可见: own=%v effective=%v", sp.OwnSkills, sp.EffectiveSkills)
	}
}

// TestDeleteDropsOwnSkillsFromIndex 删除角色后,它的私有技能不能再算「已加载」。
// (整个角色目录进 .trash,索引不重扫就还留着 —— 面板会列出一条属于已删角色的技能)
func TestDeleteDropsOwnSkillsFromIndex(t *testing.T) {
	h := newHarness(t, false)
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "cc", Name: "CC"}, ""); err != nil {
		t.Fatal(err)
	}
	writeSkillDoc(t, filepath.Join(h.home, "roles", "cc", "skills"), "own-c", "私有C")
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Delete("cc"); err != nil {
		t.Fatal(err)
	}
	for _, si := range h.svc.Skills() {
		if si.Name == "own-c" {
			t.Fatalf("已删角色的私有技能仍在索引里: %+v", si)
		}
	}
	if got := h.svc.Skills(); len(got) != 0 { // 本用例只建了这一个技能
		t.Fatalf("索引应已清空: %+v", got)
	}
}

// TestStaleMountDoesNotBlockUpdate 挂载的技能被删后,该角色仍能保存(否则面板全量 400),
// 但**新增**一个不存在的技能仍必须显式失败。
func TestStaleMountDoesNotBlockUpdate(t *testing.T) {
	h := newHarness(t, false)
	writeSkillDoc(t, filepath.Join(h.home, "skills"), "gone", "将被删")
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Create(sdk.RoleSpec{
		ID: "aa", Name: "AA", Skills: []string{"gone"}, SkillsSet: true,
	}, ""); err != nil {
		t.Fatal(err)
	}
	// 删技能(等价于面板 DELETE /api/skills + Rescan)→ 挂载变悬空
	if err := os.RemoveAll(filepath.Join(h.home, "skills", "gone")); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}

	spec, _ := h.svc.Get("aa")
	spec.Name = "改名"
	updated, err := h.svc.Update("aa", spec)
	if err != nil {
		t.Fatalf("悬空挂载不应阻塞保存: %v", err)
	}
	if updated.Name != "改名" {
		t.Fatalf("保存应生效: %+v", updated)
	}
	// 取消那条悬空挂载 = 自助修复路径,必须走得通
	spec, _ = h.svc.Get("aa")
	spec.Skills = nil
	if _, err := h.svc.Update("aa", spec); err != nil {
		t.Fatalf("应能清掉悬空挂载: %v", err)
	}
	// 反过来:新增未知名仍严格失败(严进宽出)
	spec, _ = h.svc.Get("aa")
	spec.SkillsSet, spec.Skills = true, []string{"nope"}
	if _, err := h.svc.Update("aa", spec); err == nil || !strings.Contains(err.Error(), "技能不存在") {
		t.Fatalf("新增不存在的技能应显式失败: %v", err)
	}
}

// TestDanglingActiveRoleIsExplicit 当前角色被外部删掉(目录不见了)时不得静默回落基线:
// 偏好保留原 id、身份槽显式说明「本轮按基线运行」,技能可见集合 = 基线共享池。
func TestDanglingActiveRoleIsExplicit(t *testing.T) {
	h := newHarness(t, false)
	writeSkillDoc(t, filepath.Join(h.home, "skills"), "shared-a", "共享技能")
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "gone", Name: "会消失", Identity: "你是测试角色"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Use("gone"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.systemText(), "你是测试角色") {
		t.Fatalf("切换后身份槽应生效")
	}

	// 绕过服务直接删目录(等价于用户手删 roles/gone/ 或回滚角色文件),再刷新
	if err := os.RemoveAll(filepath.Join(h.home, "roles", "gone")); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}

	if got := h.svc.Current(); got != "gone" {
		t.Fatalf("不得静默改动用户偏好(Current 应仍是 gone): %q", got)
	}
	block := h.svc.BlockText()
	if !strings.Contains(block, "gone") || !strings.Contains(block, "找不到") || !strings.Contains(block, "基线") {
		t.Fatalf("身份槽应显式说明角色已失效并按基线运行: %q", block)
	}
	if !strings.Contains(h.systemText(), "找不到") {
		t.Fatalf("悬空当前角色必须体现在系统提示里(不静默降级)")
	}
	// 悬空 = 基线:共享技能仍可见,不掉能力
	eff := h.svc.effectiveSkills("gone")
	if len(eff) != 1 || eff[0] != "shared-a" {
		t.Fatalf("悬空角色应回落到基线共享池: %v", eff)
	}
	// 重新选一个存在角色即恢复正常身份槽
	if err := h.svc.Use(""); err != nil {
		t.Fatal(err)
	}
	if h.svc.BlockText() != "" {
		t.Fatalf("停用角色后身份槽应为空: %q", h.svc.BlockText())
	}
}

// switchLog 只实现 Append/Replay 的会话账本桩(内嵌 nil 接口:未覆盖的方法一旦被调用即 panic,
// 保证本用例只依赖"切换记了一条事件"这一条路径)。
type switchLog struct {
	sdk.SessionLog
	evs []sdk.SessionEvent
}

func (l *switchLog) Append(ev sdk.SessionEvent) error { l.evs = append(l.evs, ev); return nil }
func (l *switchLog) Replay() []sdk.SessionEvent       { return l.evs }

// TestUseRecordsRoleSwitchEvent R-3:切角色**不换会话**,但系统提示被整段换掉 ——
// 会话记录里必须留下"换过谁"的痕迹,否则重看会话/`/recap` 无从判断某段话属于哪个人格。
func TestUseRecordsRoleSwitchEvent(t *testing.T) {
	h := newHarness(t, false)
	lg := &switchLog{}
	if err := h.c.Provide("ctx.sessions", sdk.SessionLog(lg)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "finance", Name: "财务"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Use("finance"); err != nil {
		t.Fatal(err)
	}
	if len(lg.evs) != 1 {
		t.Fatalf("切换后应记 1 条事件: %+v", lg.evs)
	}
	if ev := lg.evs[0]; ev.Kind != sdk.EventRoleSwitch {
		t.Fatalf("事件类型 = %s, want %s", ev.Kind, sdk.EventRoleSwitch)
	}
	p, ok := lg.evs[0].Payload.(sdk.RoleSwitchEvent)
	if !ok {
		t.Fatalf("载荷类型 = %T", lg.evs[0].Payload)
	}
	if p.ID != "finance" || p.Name != "财务" || p.Prev != "" {
		t.Errorf("载荷不符: %+v", p)
	}

	// 幂等切换(同角色)不动状态也不重复记事件
	if err := h.svc.Use("finance"); err != nil {
		t.Fatal(err)
	}
	if len(lg.evs) != 1 {
		t.Errorf("同角色重复切换不该再记事件: %+v", lg.evs)
	}
	// 停用回基线:ID 空,prev 记下从哪来
	if err := h.svc.Use(""); err != nil {
		t.Fatal(err)
	}
	if len(lg.evs) != 2 {
		t.Fatalf("回基线应记第 2 条: %+v", lg.evs)
	}
	if p := lg.evs[1].Payload.(sdk.RoleSwitchEvent); p.ID != "" || p.Prev != "finance" {
		t.Errorf("回基线载荷不符: %+v", p)
	}
	// 失败的切换不落事件(没发生的事不进账本)
	if err := h.svc.Use("ghost"); err == nil {
		t.Fatal("切到不存在的角色应报错")
	}
	if len(lg.evs) != 2 {
		t.Errorf("失败的切换不该落事件: %+v", lg.evs)
	}

	// 未装配账本的极简 profile:切换本身照常成功(记账是可通道,不是前置条件)
	h2 := newHarness(t, false)
	if _, err := h2.svc.Create(sdk.RoleSpec{ID: "novelist", Name: "小说家"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := h2.svc.Use("novelist"); err != nil {
		t.Errorf("未装配 ctx.sessions 时应照常切换: %v", err)
	}
}

// TestUseNoneAliasPrefersRealRole "none"/"off" 是停用别名,但只在**没有同名角色**时生效:
// 否则用户在面板点「切换」到一个真叫 none 的角色,会被静默停用(展示成"已停用"却不报错)。
func TestUseNoneAliasPrefersRealRole(t *testing.T) {
	h := newHarness(t, false)
	if err := h.svc.Use("none"); err != nil || h.svc.Current() != "" {
		t.Fatalf("无同名角色时 none 应停用,得到 current=%q err=%v", h.svc.Current(), err)
	}
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "none", Name: "无名"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Use("none"); err != nil {
		t.Fatalf("有同名角色时应真的切过去: %v", err)
	}
	if h.svc.Current() != "none" {
		t.Fatalf("Current = %q, want none", h.svc.Current())
	}
	if err := h.svc.Use(""); err != nil || h.svc.Current() != "" {
		t.Fatalf("Use(\"\") 应回基线,得到 current=%q err=%v", h.svc.Current(), err)
	}
}

// TestPatchRoleSerializesConcurrentUpdates 并发「读-改-写」不得丢改动。
// 旧实现是「读缓存快照 → 整份 Save」:两个面板控件同时提交时,后者用旧快照盖掉前者,
// 两次都回 200 —— 用户看到两条「已保存」但只生效一条。mutate 里 sleep 把窗口放大。
func TestPatchRoleSerializesConcurrentUpdates(t *testing.T) {
	h := newHarness(t, false)
	shared := filepath.Join(h.home, "skills")
	var want []string
	for _, n := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		writeSkillDoc(t, shared, n, "技能"+n)
		want = append(want, n)
	}
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "finance", Name: "财务", SkillsSet: true}, ""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, n := range want {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, err := h.svc.PatchRole("finance", func(cur *sdk.RoleSpec) error {
				cur.SkillsSet = true
				cur.Skills = append(append([]string(nil), cur.Skills...), name)
				time.Sleep(5 * time.Millisecond) // 放大竞态窗口(旧实现下必丢改动)
				return nil
			})
			if err != nil {
				t.Errorf("PatchRole(%s): %v", name, err)
			}
		}(n)
	}
	wg.Wait()
	got, ok := h.svc.Get("finance")
	if !ok {
		t.Fatal("finance 不见了")
	}
	if len(got.Skills) != len(want) {
		t.Fatalf("并发挂载丢了改动:得到 %v(%d 项),want %d 项", got.Skills, len(got.Skills), len(want))
	}
	// mutate 返回 error = 一个字节都不写
	if _, err := h.svc.PatchRole("finance", func(*sdk.RoleSpec) error { return errNope }); err == nil {
		t.Fatal("mutate 报错应原样上冒")
	}
	after, _ := h.svc.Get("finance")
	if len(after.Skills) != len(want) {
		t.Fatalf("被拒的 PatchRole 改动了定义: %v", after.Skills)
	}
}

var errNope = errors.New("校验失败(测试)")

// toolNames 当前模型可见的工具名集合。
func (h *harness) toolNames() map[string]bool {
	out := map[string]bool{}
	for _, d := range h.tools.List() {
		out[d.Name] = true
	}
	return out
}

// TestRoleSwitchesToolVisibility 角色排除清单 → 工具面立即变化(第九十一批)。
//
// 为何与技能挂载同一套写法:"切换即生效"不能靠重装插件 —— 判定函数每次现算读当前角色;
// 缓存会让"改完角色没生效"变成偶发 bug(第八十七批 P1-3 的教训)。
func TestRoleSwitchesToolVisibility(t *testing.T) {
	h := newHarness(t, false)
	if _, err := h.svc.Create(sdk.RoleSpec{
		ID: "finance", Name: "财务", ExcludeGlobal: true,
		ToolsExclude: []string{"read_skill"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	// 基线:全量工具(含 read_skill)
	if !h.toolNames()["read_skill"] || !h.toolNames()["list_roles"] {
		t.Fatalf("基线应看到全部工具: %v", h.toolNames())
	}
	// 切到财务:排除清单生效,且**无需重装插件**
	if err := h.svc.Use("finance"); err != nil {
		t.Fatal(err)
	}
	got := h.toolNames()
	if got["read_skill"] {
		t.Fatalf("被排除的工具不该在模型可见表里: %v", got)
	}
	if !got["list_roles"] {
		t.Fatalf("未排除的工具应保留: %v", got)
	}
	// 凭记忆调用:显式拒绝(不是"不存在" —— 后者会让人去查插件安装)
	res, _ := h.tools.Execute(context.Background(), "read_skill", `{"name":"x"}`)
	if !strings.Contains(res.Error, "排除") || strings.Contains(res.Error, "不存在") {
		t.Fatalf("被排除的工具应给未授权文案: %q", res.Error)
	}
	// 停用 → 立即回全量
	if err := h.svc.Use(""); err != nil {
		t.Fatal(err)
	}
	if !h.toolNames()["read_skill"] {
		t.Fatalf("停用后应恢复全部工具: %v", h.toolNames())
	}
	// 改角色的排除清单(走 Update)→ 下一轮判定即变(不缓存)
	spec, ok := h.svc.Get("finance")
	if !ok {
		t.Fatal("角色不见了")
	}
	spec.ToolsExclude = []string{"list_roles"}
	if _, err := h.svc.Update("finance", spec); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Use("finance"); err != nil {
		t.Fatal(err)
	}
	if h.toolNames()["list_roles"] || !h.toolNames()["read_skill"] {
		t.Fatalf("改完排除清单应立即生效: %v", h.toolNames())
	}
}

// TestToolFilterRemovedOnUnload 卸载 host-roles(执行 Disposer)→ 工具面回全量,
// 不残留过滤(注册即副作用、卸载即撤销)。
func TestToolFilterRemovedOnUnload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	for _, p := range []sdk.Plugin{&hosttools.Plugin{}, &hostsystemprompt.Plugin{}, &hostskills.Plugin{}} {
		if _, err := p.Start(c, &sdk.Manifest{}); err != nil {
			t.Fatal(err)
		}
	}
	stop, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	var svc *Service
	if err := c.Inject("ctx.roles", &svc); err != nil {
		t.Fatal(err)
	}
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(sdk.RoleSpec{ID: "r1", ToolsExclude: []string{"read_skill"}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := svc.Use("r1"); err != nil {
		t.Fatal(err)
	}
	visible := func() bool {
		for _, d := range tools.List() {
			if d.Name == "read_skill" {
				return true
			}
		}
		return false
	}
	if visible() {
		t.Fatal("角色生效时 read_skill 应不可见")
	}
	stop()
	if !visible() {
		t.Fatal("卸载 host-roles 后工具面应回全量(不残留过滤)")
	}
}

// noCatalogueRegistry 未实现 sdk.ToolCatalogue 的注册表替身(旧宿主/测试桩)。
type noCatalogueRegistry struct{ sdk.ToolRegistry }

// TestToolFilterOptionalCapability 注册表未实现 ToolCatalogue ⇒ host-roles 不报错、不 panic
// (能力不存在 ≠ 静默降级:工具过滤这一层就没有)。
func TestToolFilterOptionalCapability(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	t.Setenv("GAH_HOME", t.TempDir())
	inner := &memRegistry{}
	if err := c.Provide("ctx.tools", noCatalogueRegistry{ToolRegistry: inner}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostskills.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatalf("未实现 ToolCatalogue 的注册表不该让 host-roles 启动失败: %v", err)
	}
}

// memRegistry 极简 ToolRegistry 替身。
type memRegistry struct {
	mu    sync.Mutex
	tools map[string]sdk.Tool
	order []string
}

func (m *memRegistry) Register(t sdk.Tool) sdk.Disposer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tools == nil {
		m.tools = map[string]sdk.Tool{}
	}
	n := t.Definition().Name
	m.tools[n] = t
	m.order = append(m.order, n)
	return func() {}
}
func (m *memRegistry) List() []sdk.ToolDefinition {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []sdk.ToolDefinition
	for _, n := range m.order {
		out = append(out, m.tools[n].Definition())
	}
	return out
}
func (m *memRegistry) Get(name string) (sdk.ToolDefinition, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tools[name]
	if !ok {
		return sdk.ToolDefinition{}, false
	}
	return t.Definition(), true
}
func (m *memRegistry) Execute(context.Context, string, string) (*sdk.ToolResult, error) {
	return &sdk.ToolResult{Content: "{}"}, nil
}
