// /context 与 /recap 单测(S 组三端优化 S-P0-4 / S-P0-5)。
package hostintcmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 纯函数 ——

func TestEstTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abcd", 1},      // ASCII 4 字符 = 1
		{"abcdefgh", 2},  // 8 字符 = 2
		{"中文", 2},        // CJK 1 字符 ≈ 1 token
		{"中文测试", 4},      // 4 CJK
		{"a中", 1 + 1},    // 1 ASCII(向上取整 1) + 1 CJK
		{"aaaa中", 1 + 1}, // 4 ASCII = 1 + 1 CJK
	}
	for _, c := range cases {
		if got := estTokensStr(c.in); got != c.want {
			t.Errorf("estTokensStr(%q) = %d, want %d", c.in, got, c.want)
		}
	}
	// 传参口径:runes 与 bytes 一致时按 ASCII 处理
	if got := estTokens(4, 4); got != 1 {
		t.Errorf("estTokens(4,4) = %d, want 1", got)
	}
	if got := estTokens(0, 0); got != 0 {
		t.Errorf("estTokens(0,0) = %d, want 0", got)
	}
	// 非法(字节 < rune)不 panic、不退化为负数
	if got := estTokens(4, 0); got < 0 {
		t.Errorf("estTokens(4,0) = %d, want >= 0", got)
	}
}

func TestContextBar(t *testing.T) {
	if got := contextBar(0, 100); got != strings.Repeat("░", 20) {
		t.Errorf("0%% 应为全空: %q", got)
	}
	if got := contextBar(100, 100); got != strings.Repeat("█", 20) {
		t.Errorf("100%% 应为全满: %q", got)
	}
	half := contextBar(50, 100)
	if strings.Count(half, "█") != 10 || strings.Count(half, "░") != 10 {
		t.Errorf("50%% 应为 10/10: %q", half)
	}
	// 溢出与未知窗口不得 panic
	if got := contextBar(200, 100); strings.Count(got, "█") != 20 {
		t.Errorf("溢出应全满: %q", got)
	}
	if got := contextBar(10, 0); got != strings.Repeat("░", 20) {
		t.Errorf("窗口未知应全空: %q", got)
	}
}

// —— 桩 ——

type stubUsage struct{ st sdk.UsageStats }

func (s *stubUsage) Stats() sdk.UsageStats { return s.st }
func (s *stubUsage) Reset()                {}

type stubTools struct{ defs []sdk.ToolDefinition }

func (s *stubTools) Register(sdk.Tool) sdk.Disposer        { return func() {} }
func (s *stubTools) List() []sdk.ToolDefinition            { return s.defs }
func (s *stubTools) Get(string) (sdk.ToolDefinition, bool) { return sdk.ToolDefinition{}, false }
func (s *stubTools) Execute(context.Context, string, string) (*sdk.ToolResult, error) {
	return &sdk.ToolResult{}, nil
}

// stubInspector 实现 sdk.SystemPromptInspector 的系统提示桩。
type stubInspector struct {
	text  string
	parts []sdk.PromptPart
}

func (s *stubInspector) AddSection(sdk.SystemPromptSection) sdk.Disposer { return func() {} }
func (s *stubInspector) Assemble(_ []sdk.LLMMessage, tools []sdk.ToolDefinition) []sdk.LLMMessage {
	return []sdk.LLMMessage{{Role: sdk.RoleSystem, Content: s.text}}
}
func (s *stubInspector) Breakdown([]sdk.ToolDefinition) []sdk.PromptPart { return s.parts }

// —— /context ——

func TestCmdContextNoServices(t *testing.T) {
	c, cmds := buildEnv(t)
	startCmds(t, c)
	out, err := run(t, cmds, "context")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"未装配", "上下文窗口", "口径:"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
}

func TestCmdContextFull(t *testing.T) {
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.usageStats", sdk.UsageStatsService(&stubUsage{st: sdk.UsageStats{
		PromptTokens: 12345, CompletionTokens: 678, CachedTokens: 4096, Requests: 5, Window: 65536,
	}})); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.tools", sdk.ToolRegistry(&stubTools{defs: []sdk.ToolDefinition{
		{Name: "bash", Description: "运行命令", InputSchema: map[string]any{"type": "object"}},
		{Name: "read_file", Description: "读文件", InputSchema: map[string]any{"type": "object"}},
	}})); err != nil {
		t.Fatal(err)
	}
	sp := &stubInspector{
		text: "你是 gah。中文中文",
		parts: []sdk.PromptPart{
			{Label: "固定引导(身份+规则)", Chars: 4, Bytes: 6},
			{Label: "片段 skills", Chars: 10, Bytes: 30},
		},
	}
	if err := c.Provide("ctx.systemPrompt", sdk.SystemPromptService(sp)); err != nil {
		t.Fatal(err)
	}
	sl := newStubLog()
	sl.events = []sdk.SessionEvent{
		{Kind: sdk.EventUserMessage, Seq: 1, Payload: sdk.UserMessage{Content: "你好"}},
		{Kind: sdk.EventAssistantMessage, Seq: 2, Payload: sdk.AssistantMessage{Content: "答"}},
	}
	if err := c.Provide("ctx.sessions", sdk.SessionLog(sl)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)

	out, err := run(t, cmds, "context")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"上下文窗口: 65536 token",
		"18%",            // 12345/65536
		"缓存命中 4096(33%)", // 4096/12345
		"系统提示(组装后真串)",    // 与 usageStats 分层的估算段
		"固定引导(身份+规则)",    // inspector 分解
		"片段 skills",
		"工具定义 schema(2 个)",
		"会话投影历史", // stubLog.DeriveMessages 为 nil → 0 条
		"下一轮请求本地估算下限",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	// 逐工具清单只在 all 展开
	if strings.Contains(out, "逐工具 schema 成本") {
		t.Errorf("非 all 模式不应展开逐工具:\n%s", out)
	}
	outAll, err := run(t, cmds, "context", "all")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"逐工具 schema 成本", "bash", "read_file"} {
		if !strings.Contains(outAll, want) {
			t.Errorf("all 输出缺少 %q:\n%s", want, outAll)
		}
	}
	// 确定性:同一状态两次调用输出一致(估算不引入随机)
	out2, _ := run(t, cmds, "context")
	if out2 != out {
		t.Errorf("两次 /context 输出不一致(应确定性):\n--- 1 ---\n%s\n--- 2 ---\n%s", out, out2)
	}
}

func TestCmdContextUnknownWindow(t *testing.T) {
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.usageStats", sdk.UsageStatsService(&stubUsage{st: sdk.UsageStats{
		PromptTokens: 100, Requests: 1, Window: 0,
	}})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "context")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "未识别模型 → 默认值") {
		t.Errorf("窗口未知时应显式标注来源:\n%s", out)
	}
}

func TestCmdContextNoRequests(t *testing.T) {
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.usageStats", sdk.UsageStatsService(&stubUsage{})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "context")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "本会话尚无请求") {
		t.Errorf("无请求时应显式说明:\n%s", out)
	}
}

// —— /recap ——

func sessionEvents() []sdk.SessionEvent {
	base := time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
	return []sdk.SessionEvent{
		{Kind: sdk.EventUserMessage, Seq: 1, TS: base, Payload: sdk.UserMessage{Content: "帮我改 main.go\n第二行"}},
		{Kind: sdk.EventToolCall, Seq: 2, TS: base.Add(time.Second), Payload: sdk.ToolCallEvent{
			ID: "c1", Name: "read_file", Arguments: `{"path":"/w/main.go"}`,
		}},
		{Kind: sdk.EventToolResult, Seq: 3, TS: base.Add(2 * time.Second), Payload: sdk.ToolResultEvent{CallID: "c1", Name: "read_file", Content: "ok"}},
		{Kind: sdk.EventUsage, Seq: 4, TS: base.Add(3 * time.Second), Payload: sdk.UsageEvent{Model: "deepseek-chat"}},
		{Kind: sdk.EventUserMessage, Seq: 5, TS: base.Add(65 * time.Second), Payload: sdk.UserMessage{Content: "再改一处"}},
		{Kind: sdk.EventToolCall, Seq: 6, TS: base.Add(66 * time.Second), Payload: sdk.ToolCallEvent{
			ID: "c2", Name: "edit", Arguments: `{"path":"/w/main.go","content":"x"}`,
		}},
		{Kind: sdk.EventToolResult, Seq: 7, TS: base.Add(67 * time.Second), Payload: sdk.ToolResultEvent{CallID: "c2", Name: "edit", Error: "boom"}},
		{Kind: sdk.EventAssistantMessage, Seq: 8, TS: base.Add(68 * time.Second), Payload: sdk.AssistantMessage{Content: "已改完"}},
	}
}

func TestCmdRecapFull(t *testing.T) {
	c, cmds := buildEnv(t)
	sl := newStubLog()
	sl.events = sessionEvents()
	if err := c.Provide("ctx.sessions", sdk.SessionLog(sl)); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(&stubCwd{cur: "proj-x", path: "/w/.gah/s.jsonl"})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)

	out, err := run(t, cmds, "recap")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"会话速览(本地统计,未调用模型)",
		"项目: proj-x",
		"模型: deepseek-chat",
		"轮次: 2 轮用户消息 / 1 条助手消息 / 2 次工具调用(失败 1)",
		"跨度: 1m08s",
		"read_file×1", "edit×1(失败 1)",
		"涉及文件(1)", "/w/main.go(edit/read_file)",
		"最近一问: 再改一处",
		"最近一答: 已改完",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
}

// TestCmdRecapNoModelDependency:不提供 ctx.llm / ctx.usageStats 也能完整工作
// (纯本地统计的硬约束 —— 一旦引入模型调用,本用例会因服务缺失而失败)。
func TestCmdRecapNoModelDependency(t *testing.T) {
	c, cmds := buildEnv(t)
	sl := newStubLog()
	sl.events = sessionEvents()
	if err := c.Provide("ctx.sessions", sdk.SessionLog(sl)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "recap")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "轮次: 2 轮") {
		t.Errorf("无模型服务时仍应给出统计:\n%s", out)
	}
	if strings.Contains(out, "项目:") {
		t.Errorf("未装配 cwdSessions 时不应输出项目行:\n%s", out)
	}
}

func TestCmdRecapEmptySession(t *testing.T) {
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.sessions", sdk.SessionLog(newStubLog())); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "recap")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "当前会话为空") {
		t.Errorf("空会话应显式提示:\n%s", out)
	}
}

func TestCmdRecapNoSessions(t *testing.T) {
	c, cmds := buildEnv(t)
	startCmds(t, c)
	if _, err := run(t, cmds, "recap"); err == nil {
		t.Fatal("ctx.sessions 未装配应显式报错(不静默)")
	}
}

func TestTruncateRunesAndHumanDur(t *testing.T) {
	if got := truncateRunes("中文中文中文", 3); got != "中文中…" {
		t.Errorf("truncateRunes = %q", got)
	}
	if got := truncateRunes("a\nb", 10); got != "a b" {
		t.Errorf("换行应折叠为空格: %q", got)
	}
	if got := truncateRunes("short", 10); got != "short" {
		t.Errorf("未超限不应加省略号: %q", got)
	}
	for _, c := range []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{2500 * time.Millisecond, "2.5s"},
		{68 * time.Second, "1m08s"},
		{3*time.Hour + 5*time.Minute, "3h05m"},
	} {
		if got := humanDur(c.d); got != c.want {
			t.Errorf("humanDur(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestRecapArgsFiles(t *testing.T) {
	if got := recapArgsFiles(""); len(got) != 0 {
		t.Errorf("空参数应无结果: %v", got)
	}
	if got := recapArgsFiles("not json"); len(got) != 0 {
		t.Errorf("非法 JSON 应无结果(不 panic): %v", got)
	}
	got := recapArgsFiles(`{"path":"/a","cmd":"rm -rf /","paths":["/b","/c"],"n":1}`)
	if len(got) != 3 {
		t.Fatalf("应提取 3 个路径(白名单只取 path/paths… 实为 path+paths 数组): %v", got)
	}
	// "cmd" 不在白名单:命令文本不得被误当路径
	for _, f := range got {
		if strings.Contains(f, "rm -rf") {
			t.Errorf("命令值被误当路径: %v", got)
		}
	}
}
