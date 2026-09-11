// 命令执行路径补测(覆盖率补强):沙箱/设置/导出/停止/压缩/会话/指令重载 +
// 纯函数辅助。此前的测试只覆盖注册与 thinking/approval 两条执行路径。
package hostintcmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// startCmds 在给定 ctx 上启动命令插件并返回注册表(测试结束自动撤销)。
func startCmds(t *testing.T, c *ctx.Ctx) sdk.CommandRegistry {
	t.Helper()
	var cmds sdk.CommandRegistry
	if err := c.Inject("ctx.commands", &cmds); err != nil {
		t.Fatal(err)
	}
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dis)
	return cmds
}

// run 执行已注册命令。
func run(t *testing.T, cmds sdk.CommandRegistry, name string, args ...string) (string, error) {
	t.Helper()
	spec, ok := cmds.Get(name)
	if !ok {
		t.Fatalf("命令 %s 未注册", name)
	}
	return spec.Run(args)
}

// TestCommandsExecuteSandbox /sandbox:切档 + 持久化;非法档显式报错,未装配显式报错。
func TestCommandsExecuteSandbox(t *testing.T) {
	c, _ := buildEnv(t)
	t.Setenv("GAH_HOME", t.TempDir())
	sb := &stubSandbox{mode: sdk.SandboxWorkspace}
	if err := c.Provide("ctx.sandbox", sb); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)
	if _, err := run(t, cmds, "sandbox", "ro"); err != nil {
		t.Fatal(err)
	}
	if sb.mode != sdk.SandboxReadOnly {
		t.Fatalf("沙箱未切档: %s", sb.mode)
	}
	if p := prefs.Load(); p.Sandbox != string(sdk.SandboxReadOnly) {
		t.Fatalf("沙箱偏好未持久化: %+v", p)
	}
	if _, err := run(t, cmds, "sandbox", "bogus"); err == nil {
		t.Fatal("非法档位应显式报错")
	}
	if out, err := run(t, cmds, "sandbox"); err != nil || !strings.Contains(out, "沙箱: ") {
		t.Fatalf("无参应回显档位: out=%q err=%v", out, err)
	}
	if _, err := run(t, cmds, "sandbox", "ws"); err != nil {
		t.Fatal(err)
	}
	if sb.mode != sdk.SandboxWorkspace {
		t.Fatalf("ws 档未生效: %s", sb.mode)
	}
	if _, err := run(t, cmds, "sandbox", "full"); err != nil {
		t.Fatal(err)
	}
	if sb.mode != sdk.SandboxFullAccess {
		t.Fatalf("full 档未生效: %s", sb.mode)
	}
	// 未装配沙箱:显式报错(不静默)
	c2, _ := buildEnv(t)
	cmds2 := startCmds(t, c2)
	if _, err := run(t, cmds2, "sandbox", "full"); err == nil {
		t.Fatal("ctx.sandbox 未装配应显式报错")
	}
}

// TestCommandsExecuteSettings /settings history:off/unlimited/N/非法 + 持久化。
func TestCommandsExecuteSettings(t *testing.T) {
	c, _ := buildEnv(t)
	t.Setenv("GAH_HOME", t.TempDir())
	sl := newStubLog()
	if err := c.Provide("ctx.sessions", sdk.SessionLog(sl)); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)

	out, err := run(t, cmds, "settings", "history", "off")
	if err != nil || !strings.Contains(out, "off") {
		t.Fatalf("off: out=%q err=%v", out, err)
	}
	if sl.history == nil || *sl.history != -1 {
		t.Fatalf("off 应设 -1,得 %v", sl.history)
	}
	if _, err := run(t, cmds, "settings", "history", "unlimited"); err != nil {
		t.Fatal(err)
	}
	ph := prefs.Load().History
	if sl.history == nil || *sl.history != 0 || ph == nil || *ph != 0 {
		t.Fatalf("unlimited 应设 0 并持久化: %v / %v", sl.history, ph)
	}
	if _, err := run(t, cmds, "settings", "history", "12"); err != nil {
		t.Fatal(err)
	}
	if sl.history == nil || *sl.history != 12 {
		t.Fatalf("N 档未生效: %v", sl.history)
	}
	if _, err := run(t, cmds, "settings", "history", "-3"); err == nil {
		t.Fatal("负值应显式报错")
	}
	if _, err := run(t, cmds, "settings", "history"); err == nil {
		t.Fatal("缺参数应显式报错")
	}
}

// TestCommandsExecuteExport /export:jsonl / html / 无路径(仅报事件数)。
func TestCommandsExecuteExport(t *testing.T) {
	c, _ := buildEnv(t)
	sl := newStubLog()
	sl.events = []sdk.SessionEvent{
		{Seq: 1, Kind: "user/message", TS: time.Unix(1000, 0), Payload: sdk.UserMessage{Content: "hi"}},
		{Seq: 2, Kind: "assistant/message", TS: time.Unix(2000, 0), Payload: "ok"},
	}
	if err := c.Provide("ctx.sessions", sdk.SessionLog(sl)); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)

	dir := t.TempDir()
	jl := filepath.Join(dir, "out.jsonl")
	out, err := run(t, cmds, "export", jl)
	if err != nil || !strings.Contains(out, "2 条事件") {
		t.Fatalf("export jsonl: out=%q err=%v", out, err)
	}
	raw, rerr := os.ReadFile(jl)
	if rerr != nil {
		t.Fatal(rerr)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl 应逐行一事件,得 %d 行", len(lines))
	}
	var back sdk.SessionEvent
	if err := json.Unmarshal([]byte(lines[0]), &back); err != nil || back.Seq != 1 {
		t.Fatalf("jsonl 首行不可回解: %v %+v", err, back)
	}
	// .html → 自包含网页(渲染器输出含 html 结构)
	hp := filepath.Join(dir, "out.html")
	if _, err := run(t, cmds, "export", hp); err != nil {
		t.Fatal(err)
	}
	html, herr := os.ReadFile(hp)
	if herr != nil {
		t.Fatal(herr)
	}
	if !strings.Contains(strings.ToLower(string(html)), "<html") {
		t.Fatal("html 导出应为自包含网页")
	}
	// 无参:无 cwdSessions → 仅回事件数(不落盘)
	if out, err := run(t, cmds, "export"); err != nil || !strings.Contains(out, "会话事件数: 2") {
		t.Fatalf("无路径导出: out=%q err=%v", out, err)
	}
	// 无 ctx.sessions:显式报错
	c2, _ := buildEnv(t)
	cmds2 := startCmds(t, c2)
	if _, err := run(t, cmds2, "export", filepath.Join(dir, "x.jsonl")); err == nil {
		t.Fatal("ctx.sessions 未装配应显式报错")
	}
}

// TestCommandsExecuteStop /stop:无运行回合给明确回执;有运行回合触发取消。
func TestCommandsExecuteStop(t *testing.T) {
	c, _ := buildEnv(t)
	tc := &stubTurn{running: false}
	if err := c.Provide("ctx.turnControl", sdk.TurnControl(tc)); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)
	if out, err := run(t, cmds, "stop"); err != nil || !strings.Contains(out, "没有运行中") {
		t.Fatalf("空闲回合: out=%q err=%v", out, err)
	}
	tc.running = true
	if out, err := run(t, cmds, "stop"); err != nil || !strings.Contains(out, "停止") {
		t.Fatalf("运行中: out=%q err=%v", out, err)
	}
	if !tc.cancelled {
		t.Fatal("运行中回合应触发 Cancel")
	}
	// 未装配:显式错误
	c2, _ := buildEnv(t)
	cmds2 := startCmds(t, c2)
	if _, err := run(t, cmds2, "stop"); err == nil {
		t.Fatal("ctx.turnControl 未装配应显式报错")
	}
}

// TestCommandsExecuteCompact /compact:折叠 0(无可压缩)/ 正常摘要 / 错误透传 / 非 CompactService。
func TestCommandsExecuteCompact(t *testing.T) {
	c, _ := buildEnv(t)
	sl := newStubLog()
	if err := c.Provide("ctx.sessions", sdk.SessionLog(sl)); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)
	if out, err := run(t, cmds, "compact", "指示词"); err != nil || !strings.Contains(out, "无可压缩") {
		t.Fatalf("folded=0: out=%q err=%v", out, err)
	}
	sl.fold, sl.summary = 3, "  折叠后的\n摘要  "
	out, err := run(t, cmds, "compact")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已折叠 3 条事件") || !strings.Contains(out, "折叠后的 摘要") {
		t.Fatalf("摘要回显不符(应归一空白): %q", out)
	}
	sl.cerr = errString("引擎炸了")
	if _, err := run(t, cmds, "compact"); err == nil {
		t.Fatal("压缩错误应透传")
	}
	// 未实现 CompactService → 显式提示不可用
	c2, _ := buildEnv(t)
	plain := &stubLogOnly{}
	if err := c2.Provide("ctx.sessions", sdk.SessionLog(plain)); err != nil {
		t.Fatal(err)
	}
	cmds2 := startCmds(t, c2)
	if _, err := run(t, cmds2, "compact"); err == nil {
		t.Fatal("非 CompactService 应显式报错")
	}
}

// TestCommandsExecuteSession /session:list/current/new/switch(main 归主会话)/非法子命令。
func TestCommandsExecuteSession(t *testing.T) {
	c, _ := buildEnv(t)
	cs := &stubCwd{cur: "proj", list: []string{"proj", "other"}, curSession: "s1", path: "/tmp/s.jsonl"}
	if err := c.Provide("ctx.cwdSessions", sdk.CwdSessions(cs)); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)

	out, err := run(t, cmds, "session", "list")
	if err != nil || !strings.Contains(out, "proj") || !strings.Contains(out, "other") {
		t.Fatalf("list: out=%q err=%v", out, err)
	}
	out, err = run(t, cmds, "session", "current")
	if err != nil || !strings.Contains(out, "s1") || !strings.Contains(out, "/tmp/s.jsonl") {
		t.Fatalf("current: out=%q err=%v", out, err)
	}
	if out, err := run(t, cmds, "session", "new"); err != nil || !strings.Contains(out, "n1") {
		t.Fatalf("new: out=%q err=%v", out, err)
	}
	if _, err := run(t, cmds, "session", "switch", "s2"); err != nil {
		t.Fatal(err)
	}
	if cs.curSession != "s2" {
		t.Fatalf("switch 未生效: %s", cs.curSession)
	}
	// main → 主会话(id 归一为空串)
	if _, err := run(t, cmds, "session", "switch", "main"); err != nil {
		t.Fatal(err)
	}
	if cs.opened != "" {
		t.Fatalf("main 应归一为主会话(空 id),得 %q", cs.opened)
	}
	// 当前会话为空 → current 回显"主会话"兜底
	cs.curSession = ""
	if out, err := run(t, cmds, "session", "current"); err != nil || !strings.Contains(out, "主会话") {
		t.Fatalf("空会话名应兜底主会话: out=%q err=%v", out, err)
	}
	// 显示名优先(sessionLabel)
	cs.name = "我的会话"
	if out, err := run(t, cmds, "session", "current"); err != nil || !strings.Contains(out, "我的会话") {
		t.Fatalf("应优先显示会话名: out=%q err=%v", out, err)
	}
	if _, err := run(t, cmds, "session", "bogus"); err == nil {
		t.Fatal("非法子命令应显式报错")
	}
	if _, err := run(t, cmds, "session"); err == nil {
		t.Fatal("缺子命令应显式报错")
	}
	// 未装配 ctx.cwdSessions:显式报错
	c2, _ := buildEnv(t)
	cmds2 := startCmds(t, c2)
	if _, err := run(t, cmds2, "session", "list"); err == nil {
		t.Fatal("ctx.cwdSessions 未装配应显式报错")
	}
}

// TestCommandsExecuteReload /reload:实现 ReloadableInstructions 时热重载,否则显式不可用。
func TestCommandsExecuteReload(t *testing.T) {
	c, _ := buildEnv(t)
	sp := &stubPrompt{}
	if err := c.Provide("ctx.systemPrompt", sdk.SystemPromptService(sp)); err != nil {
		t.Fatal(err)
	}
	cmds := startCmds(t, c)
	if out, err := run(t, cmds, "reload"); err != nil || !strings.Contains(out, "已热重载") {
		t.Fatalf("reload: out=%q err=%v", out, err)
	}
	if !sp.reloaded {
		t.Fatal("ReloadInstructions 未调用")
	}
	sp.err = errString("文件坏了")
	if _, err := run(t, cmds, "reload"); err == nil {
		t.Fatal("重载失败应显式报错(旧值保留)")
	}
	// 未实现 ReloadableInstructions
	c2, _ := buildEnv(t)
	plain := &stubPromptPlain{}
	if err := c2.Provide("ctx.systemPrompt", sdk.SystemPromptService(plain)); err != nil {
		t.Fatal(err)
	}
	cmds2 := startCmds(t, c2)
	if _, err := run(t, cmds2, "reload"); err == nil {
		t.Fatal("未实现 ReloadableInstructions 应显式报错")
	}
}

// TestPureHelpers 纯函数回归:摘要行截断、打码、来源短名、模型备注、默认值、时间格式。
func TestPureHelpers(t *testing.T) {
	if got := compactSummaryLine("a b", 2); !strings.Contains(got, "a b") {
		t.Fatalf("摘要行: %q", got)
	}
	long := strings.Repeat("字", 300)
	line := compactSummaryLine(long, 1)
	if n := len([]rune(line)); n > 200 || !strings.HasSuffix(line, "…") {
		t.Fatalf("超长摘要应截断到 160 字 + 省略号: %d 字", n)
	}
	if got := orDefault("", "d"); got != "d" {
		t.Fatalf("orDefault 空应取默认: %q", got)
	}
	if got := orDefault("x", "d"); got != "x" {
		t.Fatalf("orDefault 非空应原样: %q", got)
	}
	if got := maskKey(""); got != "" {
		t.Fatalf("空 key 应原样: %q", got)
	}
	if got := maskKey("short"); got != "***" {
		t.Fatalf("短 key 全掩: %q", got)
	}
	if got := maskKey("sk-1234567890abcdef"); !strings.HasPrefix(got, "sk-1") || !strings.HasSuffix(got, "cdef") || !strings.Contains(got, "…") {
		t.Fatalf("长 key 应保留前后 4: %q", got)
	}
	cases := map[string]string{
		"https://api.siliconflow.cn/v1": "siliconflow",
		"https://api.deepseek.com":      "deepseek",
		"not a url":                     "not a url",
	}
	for in, want := range cases {
		if got := providerShortFromURL(in); got != want {
			t.Errorf("providerShortFromURL(%q)=%q, want %q", in, got, want)
		}
	}
	if got := modelDesc("deepseek/deepseek-chat", "deepseek", "deepseek"); got != "deepseek/deepseek-chat (来源 deepseek)" {
		t.Fatalf("归属前缀一致时不应重复标注: %q", got)
	}
	if got := modelDesc("other/m1", "owner", "src"); !strings.Contains(got, "来源 src/归属 owner") {
		t.Fatalf("归属不一致应附带: %q", got)
	}
	if got := errString("boom").Error(); got != "boom" {
		t.Fatalf("errString: %q", got)
	}
	// sessionDesc:主会话/命名会话/带时间与条数
	if got := sessionDesc(sdk.SessionInfo{ID: ""}); got != "主会话" {
		t.Fatalf("主会话描述: %q", got)
	}
	if got := sessionDesc(sdk.SessionInfo{ID: "s1"}); !strings.HasPrefix(got, "会话 s1") {
		t.Fatalf("未命名会话描述: %q", got)
	}
	got := sessionDesc(sdk.SessionInfo{ID: "s1", Name: "n", MTime: time.Now().Unix(), Frames: 3})
	if !strings.Contains(got, "n ·") || !strings.Contains(got, "3 条") {
		t.Fatalf("命名会话描述应带时间/条数: %q", got)
	}
	if got := sessionDesc(sdk.SessionInfo{ID: "", Name: "m", Frames: -1}); got != "m(主会话)" {
		t.Fatalf("主会话带名描述: %q", got)
	}
	// workspaceTimeFmt:今日 HH:MM(5 字符),更早 MM-DD HH:MM(11 字符)
	if got := workspaceTimeFmt(time.Now().Unix()); len(got) != 5 {
		t.Fatalf("今日时间格式应为 HH:MM: %q", got)
	}
	if got := workspaceTimeFmt(time.Now().Add(-72 * time.Hour).Unix()); len(got) != 11 {
		t.Fatalf("更早时间格式应为 MM-DD HH:MM: %q", got)
	}
}
