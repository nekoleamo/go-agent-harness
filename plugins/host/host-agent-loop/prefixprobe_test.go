package hostagentloop

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestPrefixProbeDetectsChangedMessage 探针的核心义务:指纹变了必须能说出**哪一条**变了。
//
// 钉它的理由:没有"哪一条"的探针只是又一个数字,诊断价值为零 —— 定位不到就只能猜,
// 而缓存问题一旦猜错,改的东西与病因完全无关。
func TestPrefixProbeDetectsChangedMessage(t *testing.T) {
	p := &prefixProbe{}
	tools := []sdk.ToolDefinition{{Name: "read_file", Description: "读文件"}}
	base := []sdk.LLMMessage{
		{Role: sdk.RoleSystem, Content: "你是 gah"},
		{Role: sdk.RoleUser, Content: "第一条用户消息"},
	}

	p.record(base, tools)
	got := p.PrefixSamples(0)
	if len(got) != 1 || got[0].DiffToPrev != "first" {
		t.Fatalf("首条样本应标 first: %+v", got)
	}

	// 完全相同 → same
	p.record(append([]sdk.LLMMessage(nil), base...), tools)
	if s := p.PrefixSamples(0); len(s) != 2 || s[1].DiffToPrev != "same" {
		t.Fatalf("相同输入应标 same: %+v", s)
	}

	// 第 2 条(user)被改写 → 必须指名第 2 条且是 user
	changed := []sdk.LLMMessage{
		{Role: sdk.RoleSystem, Content: "你是 gah"},
		{Role: sdk.RoleUser, Content: "第二条用户消息,内容不同"},
	}
	p.record(changed, tools)
	s := p.PrefixSamples(0)
	last := s[len(s)-1]
	if last.DiffToPrev != "changed" {
		t.Fatalf("应标 changed: %+v", last)
	}
	if !strings.Contains(last.DiffWhere, "第 2 条") || !strings.Contains(last.DiffWhere, "user") {
		t.Fatalf("变化点应指名第 2 条 user,现为 %q", last.DiffWhere)
	}

	// 追加一条 → 指纹变,变化点是「多出 N 条」
	grown := append(append([]sdk.LLMMessage(nil), changed...),
		sdk.LLMMessage{Role: sdk.RoleAssistant, Content: "回答"})
	p.record(grown, tools)
	s = p.PrefixSamples(0)
	if last = s[len(s)-1]; last.DiffToPrev != "changed" || !strings.Contains(last.DiffWhere, "多出") {
		t.Fatalf("追加消息应报「多出」,现为 %+v / %q", last.DiffToPrev, last.DiffWhere)
	}
}

// TestPrefixProbeSeesToolChange 工具变化也算前缀变化 —— 模型侧把 tools 计入提示,
// 只看消息会把这一类原因漏掉,而漏掉的代价正是「改了代码命中率没动」。
func TestPrefixProbeSeesToolChange(t *testing.T) {
	p := &prefixProbe{}
	msgs := []sdk.LLMMessage{{Role: sdk.RoleSystem, Content: "你是 gah"}}
	p.record(msgs, []sdk.ToolDefinition{{Name: "a", Description: "甲"}})
	p.record(msgs, []sdk.ToolDefinition{{Name: "a", Description: "甲"}, {Name: "b", Description: "乙"}})
	s := p.PrefixSamples(0)
	if last := s[len(s)-1]; last.DiffToPrev != "changed" {
		t.Fatalf("工具集变化应被记为前缀变化: %+v", last)
	}
}

// TestPrefixProbeCapsSamples 样本环形上限:诊断长会话也不能把内存吃穿。
func TestPrefixProbeCapsSamples(t *testing.T) {
	p := &prefixProbe{}
	for i := 0; i < prefixProbeCap*3; i++ {
		p.record([]sdk.LLMMessage{{Role: sdk.RoleSystem, Content: strings.Repeat("x", i+1)}}, nil)
	}
	if n := len(p.PrefixSamples(0)); n != prefixProbeCap {
		t.Fatalf("样本应截到 %d 条,现为 %d", prefixProbeCap, n)
	}
	if p.PrefixTurns() != prefixProbeCap*3 {
		t.Fatalf("请求计数不该被样本上限截断: %d", p.PrefixTurns())
	}
	// limit 生效且不越界
	if n := len(p.PrefixSamples(3)); n != 3 {
		t.Fatalf("limit=3 应返回 3 条,现为 %d", n)
	}
	if n := len(p.PrefixSamples(9999)); n != prefixProbeCap {
		t.Fatalf("超上限的 limit 应退到样本数,现为 %d", n)
	}
}

// 零值可用:Loop 在单元测试里常不带 probe,record/两个读方法都必须能扛 nil。
func TestPrefixProbeNilSafe(t *testing.T) {
	var p *prefixProbe
	p.record([]sdk.LLMMessage{{Role: sdk.RoleSystem, Content: "x"}}, nil)
	if p.PrefixSamples(0) != nil || p.PrefixTurns() != 0 {
		t.Fatal("nil 探针应安静返回空")
	}
}
