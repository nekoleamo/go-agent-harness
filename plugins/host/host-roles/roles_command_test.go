// /role 命令面与只读工具的包内补测(第九十四批 · 覆盖率补强)。
//
// 为什么单独补:命令面是**用户逐字输入**的接口(补全项、用法报错、列表/详情措辞),
// 而既有用例只走了 list/use/show/none/new/rm/rename 的顺路径 —— 补全层(Args 各层
// Options/FreeArgs)与显示分支(预置/继承/不含全局指令/坏角色目录)此前一行都没进过。
package hostroles

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// cmdOf 取已注册的 /role 命令(装配了命令注册表的 harness)。
func cmdOf(t *testing.T, h *harness) sdk.CommandSpec {
	t.Helper()
	spec, ok := h.cmds.Get("role")
	if !ok {
		t.Fatal("/role 未注册")
	}
	return spec
}

// TestRoleCommandCompletion 补全层:第一层列出全部动词(顺序即提示顺序),
// 第二层只在"要挑一个角色"的动词上给角色枚举,FreeArgs 逐动词给参数占位说明。
// 这层错了用户看到的是空补全 —— 只有测试能钉住。
func TestRoleCommandCompletion(t *testing.T) {
	h := newHarness(t, true)
	spec := cmdOf(t, h)

	// 第一层:9 个动词,Desc 非空(提示里要显示中文说明)
	top := spec.Args[0].Options(nil)
	want := []string{"list", "show", "use", "none", "new", "rename", "rm", "export", "import"}
	if len(top) != len(want) {
		t.Fatalf("第一层补全应 %d 项,得 %d:%+v", len(want), len(top), top)
	}
	for i, w := range want {
		if top[i].Value != w {
			t.Fatalf("第 %d 项应为 %q,得 %q", i, w, top[i].Value)
		}
		if strings.TrimSpace(top[i].Desc) == "" {
			t.Errorf("%s 的补全说明为空", w)
		}
	}

	// 第二层:未给出动词时不猜(nil),列出角色之外没别的可选
	if opts := spec.Args[1].Options(nil); opts != nil {
		t.Fatalf("未选动词时不该给角色枚举:%+v", opts)
	}
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "writer", Name: "撰稿人"}, ""); err != nil {
		t.Fatal(err)
	}
	if opts := spec.Args[1].Options([]string{"role", "list"}); opts != nil {
		t.Fatalf("list 不吃角色参数:%+v", opts)
	}
	for _, verb := range []string{"show", "use", "rm", "export"} {
		opts := spec.Args[1].Options([]string{"role", verb})
		found := false
		for _, o := range opts {
			if o.Value == "writer" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s 应补全出 writer:%+v", verb, opts)
		}
	}
	// 当前角色要标出来(切换/查看时最容易点错的就是它)
	if err := h.svc.Use("writer"); err != nil {
		t.Fatal(err)
	}
	marked := false
	for _, o := range spec.Args[1].Options([]string{"role", "show"}) {
		if o.Value == "writer" && strings.Contains(o.Desc, "★当前") {
			marked = true
		}
	}
	if !marked {
		t.Fatal("当前角色应在补全里标 ★当前")
	}

	// FreeArgs:逐动词的参数占位说明
	for _, tc := range []struct {
		picked []string
		want   int
	}{
		{nil, 0},
		{[]string{"role"}, 0},
		{[]string{"role", "list"}, 0},
		{[]string{"role", "show"}, 0},
		{[]string{"role", "new"}, 2},
		{[]string{"role", "rename"}, 3},
		{[]string{"role", "export"}, 1},
		{[]string{"role", "import"}, 3},
	} {
		if got := spec.Args[1].FreeArgs(tc.picked); len(got) != tc.want {
			t.Errorf("FreeArgs(%v) = %v,want %d 项", tc.picked, got, tc.want)
		}
	}
}

// TestRoleCommandUsageErrors 缺参数的动作用法必须逐条说清"该怎么写" ——
// 只说 "缺少参数" 等于让用户去猜 ID 从哪来。
func TestRoleCommandUsageErrors(t *testing.T) {
	h := newHarness(t, true)
	spec := cmdOf(t, h)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"use"}, "/role use <id>"},
		{[]string{"new"}, "/role new <id> [显示名]"},
		{[]string{"rename"}, "/role rename <当前 ID>"},
		{[]string{"rm"}, "/role rm <id>"},
		{[]string{"delete"}, "/role rm <id>"}, // 别名走同一分支
		{[]string{"import"}, "/role import <角色包路径>"},
	} {
		_, err := spec.Run(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v 应报 %q,得 %v", tc.args, tc.want, err)
		}
	}
	// 省略 "use":/role <id> = /role use <id>
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "writer", Name: "撰稿人"}, ""); err != nil {
		t.Fatal(err)
	}
	out, err := spec.Run([]string{"writer"})
	if err != nil || !strings.Contains(out, "已切换到角色") {
		t.Fatalf("省略 use 应等价于切换: %v %s", err, out)
	}
	// 不存在的角色:切不过去,且不许静默留在原角色上
	if _, err := spec.Run([]string{"use", "ghost"}); err == nil || !strings.Contains(err.Error(), "角色不存在") {
		t.Fatalf("切到不存在的角色应显式报错: %v", err)
	}
	if h.svc.Current() != "writer" {
		t.Fatalf("失败后当前角色不该被改:%q", h.svc.Current())
	}
}

// TestRoleListTextEmptyAndProblems 空库与"有坏角色目录"两个边界:
// 空库要给出下一步(新建);坏目录要**照实列出**而不是从列表里消失。
func TestRoleListTextEmptyAndProblems(t *testing.T) {
	h := newHarness(t, true)
	spec := cmdOf(t, h)
	out, err := spec.Run([]string{"list"})
	if err != nil || !strings.Contains(out, "(无角色;用 /role new <id> 新建)") {
		t.Fatalf("空库应提示怎么建角色: %v %s", err, out)
	}
	// 手工塞一个没有 role.yaml 的目录:它读不出来,但必须可见(不替用户隐藏磁盘上的东西)
	if err := os.MkdirAll(filepath.Join(h.home, "roles", "rotten"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}
	out, err = spec.Run([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "以下角色目录不可读") || !strings.Contains(out, "rotten") {
		t.Fatalf("坏角色目录应显式列出: %s", out)
	}
}

// TestRoleListTextMarkers 列表行的语义标记:显式挂载/继承默认池/不含全局指令/预置。
// 这几个标记决定用户"看不看得出这个角色到底能看到什么",标错 = 静默误导。
func TestRoleListTextMarkers(t *testing.T) {
	h := newHarness(t, true)
	writeSkillDoc(t, filepath.Join(h.home, "skills"), "skill-a", "甲")
	if _, err := h.svc.Create(sdk.RoleSpec{
		ID: "writer", Name: "撰稿人", Description: "第一行说明\n第二行不该出现",
		ExcludeGlobal: true, Skills: []string{"skill-a"}, SkillsSet: true, SkillsInherit: true,
	}, "正文"); err != nil {
		t.Fatal(err)
	}
	// 预置标记来自目录里的 .seed-version(与 internal/roles 的口径一致)
	if err := os.WriteFile(filepath.Join(h.home, "roles", "writer", roles.SeedVersionName), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "plain", Name: "默认池角色"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}
	spec := cmdOf(t, h)
	if err := h.svc.Use("writer"); err != nil {
		t.Fatal(err)
	}
	out, err := spec.Run([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"★ 撰稿人 (writer) — 第一行说明", "(显式挂载+继承默认池)", "不含全局指令", "[预置]", "默认池角色 (plain)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("列表缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "第二行不该出现") {
		t.Fatalf("说明只取首行:\n%s", out)
	}
	if !strings.Contains(out, "(默认池)") {
		t.Fatalf("未写 skills 键的角色应标 (默认池):\n%s", out)
	}
	if strings.Contains(out, "当前未启用角色(基线行为)") {
		t.Fatalf("已启用角色时不该提示基线:\n%s", out)
	}
}

// TestRoleShowTextBranches 详情页的三种技能语义 + 全局指令 + 私有技能 + 正文。
func TestRoleShowTextBranches(t *testing.T) {
	h := newHarness(t, true)
	spec := cmdOf(t, h)
	// 没启用角色时 /role show = 提示去看列表(不是报"角色不存在")
	if out, err := spec.Run([]string{"show"}); err != nil || !strings.Contains(out, "当前未启用角色。用 /role list 查看可选项。") {
		t.Fatalf("无当前角色应给指路: %v %s", err, out)
	}
	if out, err := spec.Run([]string{"show", "ghost"}); err != nil || !strings.Contains(out, "角色不存在:ghost") {
		t.Fatalf("不存在的角色应说明: %v %s", err, out)
	}

	writeSkillDoc(t, filepath.Join(h.home, "skills"), "skill-a", "甲")
	if _, err := h.svc.Create(sdk.RoleSpec{
		ID: "full", Name: "全能", Description: "描述", Identity: "你是全能助手",
		Skills: []string{"skill-a"}, SkillsSet: true, SkillsInherit: true, ExcludeGlobal: true,
	}, "规则正文\n第二行"); err != nil {
		t.Fatal(err)
	}
	// 私有技能(角色目录下的 skills/)
	writeSkillDoc(t, filepath.Join(h.home, "roles", "full", "skills"), "tax", "税务口径")
	if _, err := h.svc.Create(sdk.RoleSpec{
		ID: "repl", Name: "替换池", Skills: []string{"skill-a"}, SkillsSet: true,
	}, ""); err != nil {
		t.Fatal(err)
	}
	// 正文写空白:Create 在 agents=="" 时会灌一份模板,这里要的是"没有正文"的角色
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "bare", Name: "裸角色"}, " "); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Use("full"); err != nil {
		t.Fatal(err)
	}
	out, err := spec.Run([]string{"show", "full"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"全能 (full) ★当前", "说明:描述", "身份句:你是全能助手", "私有技能:tax(来自 ",
		"挂载清单 = skill-a + 默认池", "全局指令(AGENTS.md):不注入", "—— 规则正文 ——", "规则正文",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("详情缺 %q:\n%s", want, out)
		}
	}
	// 不带 inherit = 替换默认池
	out, err = spec.Run([]string{"show", "repl"})
	if err != nil || !strings.Contains(out, "挂载清单 = skill-a(替换默认池)") {
		t.Fatalf("替换池语义不符: %v\n%s", err, out)
	}
	// 未写 skills 键 / 无描述无身份 / 全局指令照常注入 / 无正文
	out, err = spec.Run([]string{"show", "bare"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"裸角色 (bare)", "未写 skills 键 = 使用默认技能池", "全局指令(AGENTS.md):照常注入"} {
		if !strings.Contains(out, want) {
			t.Fatalf("裸角色详情缺 %q:\n%s", want, out)
		}
	}
	for _, absent := range []string{"说明:", "身份句:", "私有技能:", "—— 规则正文 ——"} {
		if strings.Contains(out, absent) {
			t.Fatalf("空字段不该占行(%q):\n%s", absent, out)
		}
	}
}

// TestTopNAndFirstLine 两个展示助手:超长清单要能看出"还有更多",空说明要有兜底。
func TestTopNAndFirstLine(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	got := topN(items, 8)
	if len(got) != 9 || got[8] != "…" || got[0] != "a" || got[7] != "h" {
		t.Fatalf("topN 截断不符:%v", got)
	}
	if items[0] != "a" || len(items) != 10 {
		t.Fatalf("topN 不该改原切片:%v", items)
	}
	if got := topN(nil, 8); got != nil {
		t.Fatalf("空清单原样返回:%v", got)
	}
	if got := topN([]string{"a", "b"}, 8); len(got) != 2 {
		t.Fatalf("不超上限不该加省略号:%v", got)
	}
	if got := firstLine("第一行\n第二行", "无说明"); got != "第一行" {
		t.Fatalf("firstLine 取首行:%q", got)
	}
	if got := firstLine("   ", "无说明"); got != "无说明" {
		t.Fatalf("空白说明应兜底:%q", got)
	}
	if got := firstLine("单行", "无说明"); got != "单行" {
		t.Fatalf("单行原样:%q", got)
	}
}

// TestRoleUseTextBranches 切换回执的边界:已在基线上停用、重复切同一个角色。
// 这两条回执是"用户以为切了其实没切"的防线 —— 必须说清无变化。
func TestRoleUseTextBranches(t *testing.T) {
	h := newHarness(t, true)
	spec := cmdOf(t, h)
	if out, err := spec.Run([]string{"none"}); err != nil || !strings.Contains(out, "当前已是基线") {
		t.Fatalf("基线时停用应说明无变化: %v %s", err, out)
	}
	if _, err := h.svc.Create(sdk.RoleSpec{ID: "writer", Name: "撰稿人"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := spec.Run([]string{"use", "writer"}); err != nil {
		t.Fatal(err)
	}
	// 重复切同一个人:别再演一遍"已切换"(那会让用户以为状态变了)
	if out, err := spec.Run([]string{"use", "writer"}); err != nil || !strings.Contains(out, "已是当前角色") {
		t.Fatalf("重复切换应说明无需切换: %v %s", err, out)
	}
	// 再停用:这一支才真的写偏好
	if out, err := spec.Run([]string{"none"}); err != nil || !strings.Contains(out, "已停用角色,回到基线行为") {
		t.Fatalf("停用回执不符: %v %s", err, out)
	}
	if h.svc.Current() != "" {
		t.Fatalf("停用后当前角色应为空:%q", h.svc.Current())
	}
}

// TestRoleToolsEdgeCases 只读工具的边界:坏参数、无当前角色、不存在的角色、
// 以及坏角色目录是否出现在 list_roles 里(模型据此知道"有东西读不出来")。
func TestRoleToolsEdgeCases(t *testing.T) {
	h := newHarness(t, true)
	ctx := context.Background()
	// 没有当前角色且不带 id:给指路,不是空内容
	rr, err := h.tools.Execute(ctx, "read_role", "{}")
	if err != nil {
		t.Fatalf("read_role: %v", err)
	}
	if !strings.Contains(rr.Content, "当前未启用角色") {
		t.Fatalf("无当前角色应显式提示:%s", rr.Content)
	}
	// 坏参数:宿主转发前会把参数规范化,坏 JSON 到不了工具 —— 所以直接对工具本体断言
	// (工具不能假定调用方一定规范:这是它自己的防御)。
	if _, err := (&readRole{svc: h.svc}).Execute(ctx, "{"); err == nil || !strings.Contains(err.Error(), "read_role: args") {
		t.Fatalf("坏参数应报 read_role: args: %v", err)
	}
	// 不存在的角色
	rr, err = h.tools.Execute(ctx, "read_role", `{"id":"ghost"}`)
	if err != nil || !strings.Contains(rr.Content, "角色不存在: ghost") {
		t.Fatalf("不存在的角色应显式说明: %v %s", err, rr.Content)
	}
	// 坏角色目录:list_roles 要带 unreadable(模型才知道磁盘上有读不出来的角色)
	if err := os.MkdirAll(filepath.Join(h.home, "roles", "rotten"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Reload(); err != nil {
		t.Fatal(err)
	}
	lr, err := h.tools.Execute(ctx, "list_roles", "{}")
	if err != nil {
		t.Fatalf("list_roles: %v", err)
	}
	if !strings.Contains(lr.Content, "unreadable") || !strings.Contains(lr.Content, "rotten") {
		t.Fatalf("坏角色目录应出现在 unreadable:%s", lr.Content)
	}
}
