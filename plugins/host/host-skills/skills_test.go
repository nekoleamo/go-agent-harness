// 技能加载测试:扫描/解析 frontmatter/工具可用/prompt 索引注入。
package hostskills

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/internal/embed"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-tools"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// writeSkill 写入一个 SKILL.md(frontmatter + 正文)。
func writeSkill(t *testing.T, dir, name, desc string, triggers []string, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString("---\nname: " + name + "\ndescription: " + desc + "\n")
	if len(triggers) > 0 {
		sb.WriteString("trigger:\n")
		for _, tg := range triggers {
			sb.WriteString("  - " + tg + "\n")
		}
	}
	sb.WriteString("---\n" + body)
	if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanAndParse(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "code-review", "代码审查技能", []string{"审查", "review"}, "执行全面代码审查,输出分级报告。")
	sc := &Scanner{}
	skills, err := sc.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Name != "code-review" {
		t.Fatalf("扫描结果不符: %+v", skills)
	}
	if skills[0].Description != "代码审查技能" || len(skills[0].Triggers) != 2 {
		t.Fatalf("frontmatter 解析不符: %+v", skills[0])
	}
	if !strings.Contains(skills[0].Body, "全面代码审查") {
		t.Fatalf("正文解析不符: %+v", skills[0].Body)
	}
}

func TestSkillToolsAndPromptIndex(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "skill-a", "技能A", nil, "正文A")

	logger := slog.New(slog.DiscardHandler)
	bus := event.New(logger)
	c := ctx.New(logger, bus)
	if _, err := (&hosttools.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, &sdk.Manifest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Plugin{}).Start(c, &sdk.Manifest{Data: map[string]any{
		"dirs": []any{dir},
	}}); err != nil {
		t.Fatal(err)
	}

	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	// list_skills
	res, err := tools.Execute(context.Background(), "list_skills", "{}")
	if err != nil || res.Error != "" {
		t.Fatalf("list_skills 失败: %v %+v", err, res)
	}
	if !strings.Contains(res.Content, "skill-a") {
		t.Fatalf("索引应含 skill-a: %s", res.Content)
	}
	// read_skill
	res2, err := tools.Execute(context.Background(), "read_skill", `{"name":"skill-a"}`)
	if err != nil || res2.Error != "" {
		t.Fatalf("read_skill 失败: %v %+v", err, res2)
	}
	if !strings.Contains(res2.Content, "正文A") {
		t.Fatalf("read_skill 应返回全文: %s", res2.Content)
	}
	// 缺失技能 → 结构化错误
	res3, _ := tools.Execute(context.Background(), "read_skill", `{"name":"nope"}`)
	if res3.Error == "" {
		t.Fatal("缺失技能应报结构化错误")
	}

	// system prompt 索引注入
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	msgs := sp.Assemble(nil, nil)
	if !strings.Contains(msgs[0].Content, "skill-a") || !strings.Contains(msgs[0].Content, "read_skill") {
		t.Fatalf("prompt 应含技能索引与读取指令: %.400s", msgs[0].Content)
	}
}

// TestRoleOfDirectChildSkillFile SKILL.md 直接放在角色私有技能根(手写/搬运)时,
// 同样是该角色的私有技能;判成共享会让它泄给基线与所有角色。
func TestRoleOfDirectChildSkillFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	cases := []struct {
		path string
		want string
	}{
		{filepath.Join(home, "roles", "finance", "skills", "tax", "SKILL.md"), "finance"}, // 正常布局
		{filepath.Join(home, "roles", "finance", "skills", "SKILL.md"), "finance"},        // 手写/搬运
		{filepath.Join(home, "skills", "tax", "SKILL.md"), ""},                            // 共享库
		{filepath.Join(home, "roles", "finance", "AGENTS.md"), ""},                        // 角色规则不是技能
	}
	for _, tc := range cases {
		if got := roleOf(tc.path); got != tc.want {
			t.Errorf("roleOf(%s) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestRescanPicksUpProjectSkillsAfterChdir 项目技能目录必须**每次扫描现算**:
// /workspace 会 os.Chdir,构造期快照会让 <新 cwd>/.gah/skills 永远进不来(既不在索引也读不到)。
func TestRescanPicksUpProjectSkillsAfterChdir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	reg := newRegistry(nil, nil, nil)
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if n := len(reg.List()); n != 0 {
		t.Fatalf("空环境不该有技能: %d", n)
	}
	proj := t.TempDir()
	t.Chdir(proj)
	writeSKILLMD(t, filepath.Join(proj, ".gah", "skills"), "proj-skill", "项目技能")
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(reg.IndexText(), "proj-skill") {
		t.Fatalf("切目录后项目技能未进索引:\n%s", reg.IndexText())
	}
}

// TestRescanWarnsDuplicates 重名告警不能只在 Start 报一次:写技能/切角色这些热路径都会重扫,
// 那里静默就等于用户看不到「技能没生效」(first-wins 去重本身不变)。
func TestRescanWarnsDuplicates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	var buf bytes.Buffer
	reg := newRegistry(slog.New(slog.NewTextHandler(&buf, nil)), nil, nil)
	writeSKILLMD(t, filepath.Join(home, "skills"), "dup-skill", "全局")
	proj := t.TempDir()
	t.Chdir(proj)
	writeSKILLMD(t, filepath.Join(proj, ".gah", "skills"), "dup-skill", "项目同名")
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "重名") {
		t.Fatalf("重扫遇重名应告警:\n%s", buf.String())
	}
	if n := len(reg.List()); n != 1 {
		t.Fatalf("重名应 first-wins 去重: %d", n)
	}
}

// TestPresetRolePrivateSkillsScanned 预置角色的私有技能必须被扫描器看到。
//
// 为什么单独钉:文件"释放出来了"与"扫得到"是两件事 —— 扫描器的目录集是每次现算的
// scanDirs(角色私有 → 全局 → 项目 → 扩展),一旦将来某处改动漏了角色私有目录,
// 症状是「面板上该角色挂了 2 个技能,切过去却一条都看不见」,而文件明明在磁盘上。
func TestPresetRolePrivateSkillsScanned(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	if _, err := embed.EnsureRoles(t.TempDir()); err != nil {
		// 这条只验扫描面;释放由 internal/embed 的契约测试负责,这里用真实装配。
		t.Logf("预置释放失败(本用例只关心扫描): %v", err)
	}
	home := os.Getenv("GAH_HOME")
	if _, err := embed.EnsureRoles(home); err != nil {
		t.Fatal(err)
	}
	// 造一个带私有技能的角色(用真实预置目录,保证与 seed 一致)
	dirs := scanDirs(nil)
	found := false
	for _, d := range dirs {
		if filepath.Base(d) != "skills" {
			continue
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() == "clarify-requirement" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("预置角色的私有技能没出现在扫描目录集里;scanDirs=%v", dirs)
	}
}
