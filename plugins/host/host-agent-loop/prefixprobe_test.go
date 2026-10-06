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

}

// TestPrefixProbeAppendedIsNotAProblem 尾部追加**不是**故障,必须与改写分开报。
//
// 钉它的理由来自真机踩坑(2026-10-06 第一次 /cache 输出):第一版把每轮的正常追加
// 报成「✗ 前缀变化:多出 2 条消息」,看着像前缀每轮都在破坏,把人引到错的方向
// (实际那正是缓存该命中的情况)。三种结论必须分清:appended / changed / same。
func TestPrefixProbeAppendedIsNotAProblem(t *testing.T) {
	p := &prefixProbe{}
	base := []sdk.LLMMessage{
		{Role: sdk.RoleSystem, Content: "你是 gah"},
		{Role: sdk.RoleUser, Content: "第一条"},
	}
	p.record(base, nil)
	p.record(base, nil)
	p.record(append(append([]sdk.LLMMessage(nil), base...),
		sdk.LLMMessage{Role: sdk.RoleAssistant, Content: "回答"},
		sdk.LLMMessage{Role: sdk.RoleUser, Content: "追问"}), nil)

	last := p.PrefixSamples(0)
	s := last[len(last)-1]
	if s.DiffToPrev != "appended" {
		t.Fatalf("尾部追加应标 appended 而非 changed: %+v", s)
	}
	if s.MsgCount != 4 || s.SysCount != 1 {
		t.Fatalf("条数统计不对: %+v", s)
	}
	// sys 段没变 ⇒ 它的指纹必须保持不变(变了就说明有 bug)
	if p.lastSysHash != s.SysHash {
		t.Fatalf("尾部追加不应动到 sys 指纹")
	}
}

// TestPrefixProbeSegmentsPointAtCulprit 三段指纹要能指向「哪一段在变」。
//
// 真实场景:滚动摘要触发时会在第 2 位插一条 system,若只报「第 2 条变了」,人很可能去查
// 历史/用户消息;分段之后直接看到「system 变了」,对应的是摘要系统消息这件事本身。
func TestPrefixProbeSegmentsPointAtCulprit(t *testing.T) {
	p := &prefixProbe{}
	base := []sdk.LLMMessage{
		{Role: sdk.RoleSystem, Content: "你是 gah"},
		{Role: sdk.RoleUser, Content: "第一条"},
	}
	p.record(base, []sdk.ToolDefinition{{Name: "a", Description: "甲"}})
	// 摘要插入:多一条 system,且原历史仍在
	withSummary := []sdk.LLMMessage{
		{Role: sdk.RoleSystem, Content: "你是 gah"},
		{Role: sdk.RoleSystem, Content: "对先前对话的滚动摘要:…"},
		{Role: sdk.RoleUser, Content: "第一条"},
	}
	p.record(withSummary, []sdk.ToolDefinition{{Name: "a", Description: "甲"}})
	s := p.PrefixSamples(0)
	last := s[len(s)-1]
	if last.DiffToPrev != "changed" {
		t.Fatalf("插入 system 应算改写: %+v", last)
	}
	if !strings.Contains(last.DiffWhere, "system 变了") {
		t.Fatalf("应点名 system 段: %q", last.DiffWhere)
	}
	if last.SysCount != 2 {
		t.Fatalf("system 条数应为 2: %+v", last)
	}

	// 工具变 ⇒ 只报 tools,别把锅甩给消息
	p2 := &prefixProbe{}
	p2.record(base, []sdk.ToolDefinition{{Name: "a", Description: "甲"}})
	p2.record(base, []sdk.ToolDefinition{{Name: "a", Description: "甲"}, {Name: "b", Description: "乙"}})
	s2 := p2.PrefixSamples(0)
	if l := s2[len(s2)-1]; !strings.Contains(l.DiffWhere, "工具定义变了") {
		t.Fatalf("应点名工具段: %q", l.DiffWhere)
	}
}

// TestPrefixProbeSeesSystemBodyChange system 段用**全文**入指纹:长度相近、中间被改写
// 这种最常见的失配不能漏(第一版只取长度+头 48 字节,这类改动完全看不见)。
func TestPrefixProbeSeesSystemBodyChange(t *testing.T) {
	p := &prefixProbe{}
	a := sdk.LLMMessage{Role: sdk.RoleSystem, Content: "你是 gah。规则一:…要求 A。" + strings.Repeat("x", 40)}
	b := sdk.LLMMessage{Role: sdk.RoleSystem, Content: "你是 gah。规则一:…要求 B。" + strings.Repeat("x", 40)}
	p.record([]sdk.LLMMessage{a}, nil)
	p.record([]sdk.LLMMessage{b}, nil)
	s := p.PrefixSamples(0)
	if l := s[len(s)-1]; l.DiffToPrev != "changed" {
		t.Fatalf("同长度不同内容必须被发现: %+v", l)
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
