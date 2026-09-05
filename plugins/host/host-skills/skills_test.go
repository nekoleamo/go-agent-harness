// 技能加载测试:扫描/解析 frontmatter/工具可用/prompt 索引注入。
package hostskills

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
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
