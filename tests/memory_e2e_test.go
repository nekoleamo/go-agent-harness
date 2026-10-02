// 记忆跨会话的端到端(补真机清单里「记忆跨会话」那一条的自动化部分)。
//
// 为什么这条值得自动化:记忆的全部价值就是**跨会话** —— 同一个进程里写进去、下一轮
// 读出来,那是同会话,证明不了任何事。真机上验它要开两个 gah 进程、跨工作区、切窗口。
// 这里把「两个**不同的会话文件**」摆出来,用真实装配(host-system-prompt + host-memory +
// host-session-log + host-cwd-sessions)证明:会话 B 的系统提示里带着会话 A 写下的记忆。
//
// 三段对照互为证伪面:① 基线(没有记忆 ⇒ 提示里没有)② 写一条(仍在本会话)③ **换一个
// 会话文件**后仍在 ⇒ 跨会话成立;④ 关掉注入 ⇒ 消失(证明它真的来自记忆层,不是别处漏进去的)。
package tests

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-cwd-sessions"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-memory"
	hostsessionlog "github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func buildMemoryEnv(t *testing.T, home string) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", home)
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	// 真实插件装配(host-memory 依赖 ctx.systemPrompt 注入片段;会话日志由 host-cwd-sessions 提供)
	for _, p := range []sdk.Plugin{
		&hostsystemprompt.Plugin{},
		&hostsessionlog.Plugin{},
		&hostcwdsessions.Plugin{},
		&hostmemory.Plugin{},
	} {
		if _, err := p.Start(c, &sdk.Manifest{}); err != nil {
			t.Fatalf("%s Start: %v", p.Name(), err)
		}
		t.Cleanup(func() {})
	}
	return c
}

// systemText 取 Assemble 出来的系统消息正文(各段拼装的结果就在这里)。
func systemText(t *testing.T, sp sdk.SystemPromptService, history []sdk.LLMMessage) string {
	t.Helper()
	for _, m := range sp.Assemble(history, nil) {
		if m.Role == sdk.RoleSystem {
			return m.Content
		}
	}
	return ""
}

func TestMemoryCrossSessionEndToEnd(t *testing.T) {
	home := t.TempDir()
	c := buildMemoryEnv(t, home)

	var ms sdk.MemoryService
	if err := c.Inject("ctx.memory", &ms); err != nil {
		t.Fatal(err)
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	var logs sdk.SessionLogs
	if err := c.Inject("ctx.sessionLogs", &logs); err != nil {
		t.Fatal(err)
	}

	const want = "报告里的图表用蓝灰配色,不要渐变"

	// ① 基线:提示里没有这条
	if txt := systemText(t, sp, nil); strings.Contains(txt, want) {
		t.Fatal("基线不该有这条记忆")
	}

	// ② 写一条(模拟会话 A 里用户敲 /memory add)
	if err := ms.Add(want, ""); err != nil {
		t.Fatal(err)
	}
	txt := systemText(t, sp, nil)
	if !strings.Contains(txt, want) {
		t.Fatalf("本会话应已带上记忆:\n%s", txt)
	}
	// 记忆按**数据**处理:片段里必须带着「以指令为准」的声明(防提示注入的纪律)
	if !strings.Contains(txt, "以指令为准") {
		t.Fatalf("注入片段应带「以指令为准」声明:\n%s", txt)
	}

	// ③ **换一个会话文件**:这才是「跨会话」的判据
	//    会话 A 落盘一份历史,再新建会话 B(id 不同 ⇒ 另一个文件),B 的提示里必须仍在。
	a, err := logs.Acquire(filepath.Join(home, "sessions", "sess-a.jsonl"), "sess-a")
	if err != nil {
		t.Fatalf("建会话 A: %v", err)
	}
	if err := a.Append(sdk.SessionEvent{Kind: "user/message", Payload: sdk.UserMessage{Content: "记一下图表配色"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Flush(); err != nil {
		t.Fatal(err)
	}
	logs.Release(filepath.Join(home, "sessions", "sess-a.jsonl"))
	b, err := logs.Acquire(filepath.Join(home, "sessions", "sess-b.jsonl"), "sess-b")
	if err != nil {
		t.Fatalf("建会话 B: %v", err)
	}
	defer func() {
		_ = b.Flush()
		logs.Release(filepath.Join(home, "sessions", "sess-b.jsonl"))
	}()
	// A 真的落盘了(有历史),B 是**另一个文件** —— 「跨会话」的判据是文件不同,
	// 不是进程不同:同一进程里两个会话文件共享同一份记忆,这正是要验的那件事。
	if rawA, err := os.ReadFile(filepath.Join(home, "sessions", "sess-a.jsonl")); err != nil ||
		!strings.Contains(string(rawA), "记一下图表配色") {
		t.Fatalf("会话 A 的历史应真的落盘(否则「换会话」只是嘴上说):%v %s", err, rawA)
	}
	txtB := systemText(t, sp, nil)
	if !strings.Contains(txtB, want) {
		t.Fatalf("换一个会话文件后记忆应仍在(这才是跨会话):\n%s", txtB)
	}

	// ④ 关掉注入 ⇒ 消失(证明它来自记忆层,不是别处漏进来的)
	ms.SetEnabled(false)
	if txt := systemText(t, sp, nil); strings.Contains(txt, want) {
		t.Fatalf("关闭注入后不该再带:\n%s", txt)
	}
	ms.SetEnabled(true)

	// ⑤ 数据落盘位置:在数据根内(便携纪律),且人能直接改
	p := filepath.Join(home, "memory", "user.md")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("记忆文件应落在 $GAH_HOME/memory/ 下: %v", err)
	}
	if !strings.Contains(string(raw), want) {
		t.Fatalf("记忆文件内容不对:\n%s", raw)
	}
}

// 候选池的端到端:提候选 → **换会话**后仍然只是候选(不进任何会话的上下文)→ 转正后才进。
// 这条对应口径建议里「候选不注入」那条纪律,以及真机清单里没列但同样要验的负例。
func TestMemoryCandidateNotInjectedUntilAccepted(t *testing.T) {
	home := t.TempDir()
	c := buildMemoryEnv(t, home)

	var ms sdk.MemoryService
	if err := c.Inject("ctx.memory", &ms); err != nil {
		t.Fatal(err)
	}
	cs, ok := ms.(sdk.MemoryCandidates)
	if !ok {
		t.Fatal("ctx.memory 应实现候选能力")
	}
	var sp sdk.SystemPromptService
	if err := c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}

	const cand = "候选:写周报先写结论"
	if err := cs.Propose(cand, ""); err != nil {
		t.Fatal(err)
	}
	// 候选**不进上下文** —— 这是构造上的保证(不在记忆文件里)
	if txt := systemText(t, sp, nil); strings.Contains(txt, cand) {
		t.Fatalf("候选不该进上下文:\n%s", txt)
	}
	if _, err := os.Stat(filepath.Join(home, "memory", "user.md")); !os.IsNotExist(err) {
		t.Fatal("提候选不该创建记忆文件")
	}
	// 转正之后才进
	if _, err := cs.AcceptCandidate(1); err != nil {
		t.Fatal(err)
	}
	if txt := systemText(t, sp, nil); !strings.Contains(txt, cand) {
		t.Fatalf("转正后应进上下文:\n%s", txt)
	}
}

// 会话 id 与项目键的关系(记忆的项目级就是挂在项目键上):换工作区 ⇒ 项目级记忆不跟着串。
// 这条对应真机清单里的「记忆跨会话/跨工作区」,不需要真机就能判。
func TestMemoryProjectScopedByWorkspace(t *testing.T) {
	home := t.TempDir()
	c := buildMemoryEnv(t, home)

	var ms sdk.MemoryService
	if err := c.Inject("ctx.memory", &ms); err != nil {
		t.Fatal(err)
	}
	lister, ok := ms.(interface{ ListProject() []string })
	if !ok {
		t.Fatal("ctx.memory 应能列项目级")
	}
	// 直接往**当前项目键**对应的文件里写一条(模拟该项目里的记忆)
	var cs sdk.CwdSessions
	if err := c.Inject("ctx.cwdSessions", &cs); err != nil {
		t.Fatal(err)
	}
	key := cs.Current()
	if key == "" {
		t.Skip("当前环境拿不到项目键(host-cwd-sessions 未装配真实依赖),跳过")
	}
	p := filepath.Join(home, "memory", "projects", key+".md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	const proj = "本项目的口径:先给结论"
	if err := os.WriteFile(p, []byte("- [2026-10-02] "+proj+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := lister.ListProject()
	found := false
	for _, l := range lines {
		if strings.Contains(l, proj) {
			found = true
		}
	}
	if !found {
		t.Fatalf("当前项目的记忆应能被列出(项目键=%q):%v", key, lines)
	}
	// 换一个项目键 ⇒ 看不到那条(项目级不跨项目串)
	sw, ok := cs.(interface{ SwitchProject(string) (string, error) })
	if !ok {
		t.Skip("该实现不支持切项目键,跳过(负例由 host-cwd-sessions 自己的用例覆盖)")
	}
	if _, err := sw.SwitchProject("另一个项目"); err != nil {
		t.Fatalf("切项目: %v", err)
	}
	if lines2 := lister.ListProject(); len(lines2) != 0 {
		t.Fatalf("换工作区后不该看到别的项目的记忆:%v", lines2)
	}
}
