// Breakdown(S-P0-4 /context 成本分解)单测:分段口径、空块跳过、与 Assemble 事实源一致。
package hostsystemprompt

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestBreakdownSections 全段落:固定引导 + 全局 + 项目(多级) + 附加 + 片段 + 工具名清单,
// 顺序与 Assemble 一致,且每段的字符/字节数真实。
func TestBreakdownSections(t *testing.T) {
	s := &Service{
		globalInstr: "全局规则:中文回复",
		projectLevels: []projectLevel{
			{dir: "/w", file: "AGENTS.md", content: "项目规则:只改 src"},
			{dir: "/w/sub", file: "AGENTS.override.md", content: ""}, // 空级应跳过
		},
		extraInstr: []string{"附加一", "  "}, // 空附加应跳过
	}
	s.AddSection(sdk.SystemPromptSection{Name: "能力", Content: func() string { return "技能列表" }})
	s.AddSection(sdk.SystemPromptSection{Name: "空段", Content: func() string { return "  " }})

	tools := []sdk.ToolDefinition{{Name: "bash"}, {Name: "read_file"}}
	parts := s.Breakdown(tools)

	labels := make([]string, 0, len(parts))
	for _, p := range parts {
		labels = append(labels, p.Label)
		if p.Chars == 0 || p.Bytes < p.Chars {
			t.Errorf("段落计数异常: %+v", p)
		}
	}
	wantOrder := []string{
		"固定引导(身份+规则)",
		"全局指令(用户级 AGENTS.md)",
		"项目指令 /w/AGENTS.md",
		"附加指令 1",
		"片段 能力",
		"工具名清单(2 个)",
	}
	if len(parts) != len(wantOrder) {
		t.Fatalf("段数不符(空块未跳过?): %v", labels)
	}
	for i, w := range wantOrder {
		if labels[i] != w {
			t.Fatalf("第 %d 段应为 %q,实为 %q(全量 %v)", i, w, labels[i], labels)
		}
	}
	// 分段口径与 Assemble 同源:各段内容确实出现在组装串中,
	// 且分段字符总和不超过组装串总长(分段是子集:分隔符不计入任何段)。
	full := s.Assemble(nil, tools)[0].Content
	for _, want := range []string{"全局规则:中文回复", "项目规则:只改 src", "附加一", "技能列表", "bash"} {
		if !strings.Contains(full, want) {
			t.Errorf("Assemble 未见内容 %q", want)
		}
	}
	// 注:片段即使内容为空白,Assemble 仍保留标题行(见 TestAssembleNilHistoryAndEmptySection),
	// 所以这里只校验「内容不出现」,不断言标题缺席。
	if strings.Contains(full, "技能列表\n\n片段 空段") { // 空片段不得挤掉后续段落
		t.Errorf("空片段影响了后续拼接")
	}
	total := 0
	for _, p := range parts {
		total += p.Chars
	}
	if fullChars := utf8.RuneCountInString(full); total > fullChars {
		t.Errorf("分段字符总和 %d 超过组装串 %d(口径漂移)", total, fullChars)
	}
	// 固定引导段落的字节数 = 常量长度(防止常量被改而诊断未跟)
	for _, p := range parts {
		if p.Label == "固定引导(身份+规则)" {
			if p.Bytes != len(guidanceText) {
				t.Errorf("固定引导字节数 %d != len(guidanceText) %d", p.Bytes, len(guidanceText))
			}
		}
	}
}

// TestBreakdownMinimal 无任何可选段落时只有固定引导;无工具则无工具名清单。
func TestBreakdownMinimal(t *testing.T) {
	s := &Service{}
	parts := s.Breakdown(nil)
	if len(parts) != 1 || parts[0].Label != "固定引导(身份+规则)" {
		t.Fatalf("最小集应只有固定引导: %+v", parts)
	}
	// 单级回退字段(projectInstr)也应被计入
	s.projectInstr = "项目级规则"
	parts = s.Breakdown([]sdk.ToolDefinition{{Name: "t"}})
	if len(parts) != 3 {
		t.Fatalf("应为 引导+项目+工具名: %+v", parts)
	}
	if parts[1].Label != "项目指令(项目级 AGENTS.md)" || parts[2].Label != "工具名清单(1 个)" {
		t.Fatalf("单级回退/工具名段不符: %+v", parts)
	}
}

// TestBreakdownEmptyStringSection 片段返回纯空白 → 不产生段(与 Assemble 的 no-op 一致)。
func TestBreakdownEmptyStringSection(t *testing.T) {
	s := &Service{}
	s.AddSection(sdk.SystemPromptSection{Name: "空", Content: func() string { return "\n\t " }})
	if parts := s.Breakdown(nil); len(parts) != 1 {
		t.Fatalf("空白片段应跳过: %+v", parts)
	}
}
