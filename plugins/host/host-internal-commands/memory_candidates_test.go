package hostintcmd

// 候选池命令面的行为契约(第一百一十三批 · M2 前置三件事)。
//
// 重点三条,都是「不提示就会出事」的那种:
//   ① 候选**不进上下文** —— 回执必须说清这件事(否则用户以为「提了就已经记住了」);
//   ② 限流是**显式报错**(把内部错误原样带出来,不是「没反应」);
//   ③ 批量确认的半截状态要报出来(已转正几条 + 错在哪),不能报「都好了」。
//
// 沿用 memHost(真实插件装配):候选能力在 host-memory 上,命令与插件必须一起验。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/plugins/host/host-cwd-sessions"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-memory"
	hostsessionlog "github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestCmdMemoryCandidatesEmpty(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"candidates"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "候选池是空的") {
		t.Fatalf("空候选池的回执:\n%s", out)
	}
	// 额度也要说清(免得用户提了才知道今天提满了)
	if !strings.Contains(out, "今日") || !strings.Contains(out, "池子") {
		t.Fatalf("空态也应回显额度:\n%s", out)
	}
}

func TestCmdMemoryProposeAndCandidates(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"propose", "写周报先写结论"})
	if err != nil {
		t.Fatal(err)
	}
	// ① 回执必须说清「还没进上下文」
	if !strings.Contains(out, "还没进上下文") {
		t.Fatalf("propose 回执应说清候选未生效:\n%s", out)
	}
	if !strings.Contains(out, "写周报先写结论") {
		t.Fatalf("propose 后应能列出候选:\n%s", out)
	}
	// 记忆列表此刻**不该**有它
	list, _ := h.cmdMemory([]string{"list"})
	if strings.Contains(list, "写周报先写结论") {
		t.Fatalf("候选不该出现在记忆列表里:\n%s", list)
	}
	// 状态行回显待确认条数
	st, _ := h.cmdMemory(nil)
	if !strings.Contains(st, "待确认候选 1 条") {
		t.Fatalf("状态行应回显待确认候选:\n%s", st)
	}
	// 空内容的用法报错
	if _, err := h.cmdMemory([]string{"propose"}); err == nil {
		t.Fatal("空内容应报用法")
	}
}

func TestCmdMemoryProposeRateLimitSurfaced(t *testing.T) {
	h := memHost(t)
	// 提满当日额度后再提一条 ⇒ 错误必须**原样带出来**(用户看到「今天提满了」)
	var err error
	for i := 0; i < 8; i++ {
		if _, err = h.cmdMemory([]string{"propose", "候选 " + strings.Repeat("x", i+1)}); err != nil {
			break
		}
	}
	if err == nil {
		t.Fatal("提满后应报错")
	}
	if !strings.Contains(err.Error(), "上限") {
		t.Fatalf("错误应说明上限(否则用户不知道怎么办):%v", err)
	}
}

func TestCmdMemoryAcceptAndReject(t *testing.T) {
	h := memHost(t)
	_, _ = h.cmdMemory([]string{"propose", "候选甲"})
	_, _ = h.cmdMemory([]string{"propose", "候选乙"})

	// 展示序新的在前 ⇒ 1 是「候选乙」
	out, err := h.cmdMemory([]string{"accept", "1"})
	if err != nil || !strings.Contains(out, "候选乙") {
		t.Fatalf("accept 1 应转正最新的那条: %v(%s)", err, out)
	}
	if !strings.Contains(out, "已转正为记忆") {
		t.Fatalf("回执应说清已转正:\n%s", out)
	}
	list, _ := h.cmdMemory([]string{"list"})
	if !strings.Contains(list, "候选乙") {
		t.Fatalf("转正后应在记忆里:\n%s", list)
	}

	// 驳回:不进记忆
	out2, err := h.cmdMemory([]string{"reject", "1"})
	if err != nil || !strings.Contains(out2, "候选甲") {
		t.Fatalf("reject 1: %v(%s)", err, out2)
	}
	list2, _ := h.cmdMemory([]string{"list"})
	if strings.Contains(list2, "候选甲") {
		t.Fatalf("驳回的候选不该进记忆:\n%s", list2)
	}

	// 序号不是数字 / 越界 ⇒ 显式报错
	if _, err := h.cmdMemory([]string{"accept", "abc"}); err == nil {
		t.Fatal("非数字序号应报错")
	}
	if _, err := h.cmdMemory([]string{"accept", "99"}); err == nil {
		t.Fatal("越界序号应报错")
	}
	if _, err := h.cmdMemory([]string{"accept"}); err == nil {
		t.Fatal("缺序号应报用法")
	}
	if _, err := h.cmdMemory([]string{"reject"}); err == nil {
		t.Fatal("缺序号应报用法")
	}
}

func TestCmdMemoryAcceptAllAndRejectAll(t *testing.T) {
	h := memHost(t)
	_, _ = h.cmdMemory([]string{"propose", "甲一"})
	_, _ = h.cmdMemory([]string{"propose", "乙一"})
	out, err := h.cmdMemory([]string{"accept-all"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已转正 2 条") {
		t.Fatalf("accept-all 回执应带条数:\n%s", out)
	}
	list, _ := h.cmdMemory([]string{"list"})
	if !strings.Contains(list, "甲一") || !strings.Contains(list, "乙一") {
		t.Fatalf("两条都该在记忆里:\n%s", list)
	}

	// 空池上再 accept-all:明说「没东西可转正」,不是报错也不是假成功
	out2, err := h.cmdMemory([]string{"accept-all"})
	if err != nil || !strings.Contains(out2, "候选池是空的") {
		t.Fatalf("空池 accept-all: %v(%s)", err, out2)
	}

	// reject-all
	_, _ = h.cmdMemory([]string{"propose", "丙一"})
	out3, err := h.cmdMemory([]string{"reject-all"})
	if err != nil || !strings.Contains(out3, "已丢弃 1 条") {
		t.Fatalf("reject-all: %v(%s)", err, out3)
	}
	list3, _ := h.cmdMemory([]string{"list"})
	if strings.Contains(list3, "丙一") {
		t.Fatalf("丢弃的候选不该进记忆:\n%s", list3)
	}
}

// TestCmdMemoryLegacyBranches 补本包**既有**低覆盖路径(与候选无关,但同属这条命令的
// 行为面):用法错、非数字序号、越界、--from 无来源、--from 无命中、project 空态、on/off。
// 为什么现在补:本包棘轮 93%,而第一批候选命令面加了约 70 行新代码(稀释门当场报
// 跌破棘轮)⇒ 按纪律补测到棘轮之上,顺带把这条命令的其余分支钉住。
func TestCmdMemoryLegacyBranches(t *testing.T) {
	h := memHost(t)

	// rm 的三种用法错
	if _, err := h.cmdMemory([]string{"rm"}); err == nil {
		t.Fatal("rm 缺序号应报用法")
	}
	if _, err := h.cmdMemory([]string{"rm", "abc"}); err == nil {
		t.Fatal("rm 非数字应报错")
	}
	if _, err := h.cmdMemory([]string{"rm", "99"}); err == nil {
		t.Fatal("rm 越界应报错")
	}
	// --from 缺来源 / 无命中
	if _, err := h.cmdMemory([]string{"rm", "--from"}); err == nil {
		t.Fatal("--from 缺来源应报用法")
	}
	out, err := h.cmdMemory([]string{"rm", "--from", "不存在的会话"})
	if err != nil || !strings.Contains(out, "没有来自该会话的记忆") {
		t.Fatalf("--from 无命中应明说(而不是报成功却什么都没发生):%v(%s)", err, out)
	}

	// project:空态
	p, err := h.cmdMemory([]string{"project"})
	if err != nil || !strings.Contains(p, "本项目还没有项目级记忆") {
		t.Fatalf("project 空态:%v(%s)", err, p)
	}

	// on/off 两条(注入开关)
	if out, err := h.cmdMemory([]string{"off"}); err != nil || !strings.Contains(out, "已关闭") {
		t.Fatalf("off:%v(%s)", err, out)
	}
	if out, err := h.cmdMemory([]string{"on"}); err != nil || !strings.Contains(out, "开启") {
		t.Fatalf("on:%v(%s)", err, out)
	}

	// 状态行:两个开关口径都在
	st, _ := h.cmdMemory(nil)
	// 注意:状态行里只有第一个词带 `/memory` 前缀,后面几个子命令写成「/ accept-all」这种缩写形式。
	for _, want := range []string{"注入预算", "/memory propose", "/ accept-all", "/ reject"} {
		if !strings.Contains(st, want) {
			t.Fatalf("状态行应含 %q:\n%s", want, st)
		}
	}
}

// TestCmdMemorySourceRemoval 真的按来源删到东西(回执带条数)—— 上一条用例只钉了
// 「无命中」那条分支;有命中的那条是用户真正要的治理动作,不能没被钉过。
func TestCmdMemorySourceRemoval(t *testing.T) {
	h := memHost(t)
	// 直接写两条带来源的记忆(与 `/memory rm --from` 治理的来源形态一致)
	p := filepath.Join(os.Getenv("GAH_HOME"), "memory", "user.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "- [2026-10-01] 来自会话 A 的结论(来源: 会话 sess-A)\n" +
		"- [2026-10-01] 来自会话 A 的另一条(来源: 会话 sess-A)\n" +
		"- [2026-10-02] 手工记的一条\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.cmdMemory([]string{"rm", "--from", "sess-A"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已删掉来自会话 sess-A 的 2 条") {
		t.Fatalf("回执应带条数与来源:\n%s", out)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "sess-A") {
		t.Fatalf("该来源的记忆应删干净:\n%s", raw)
	}
	if !strings.Contains(string(raw), "手工记的") {
		t.Fatalf("其它来源不受影响:\n%s", raw)
	}
}

// TestCmdMemoryNotAssembled 没装 host-memory 时显式说没装(而不是崩或回落到空态)。
func TestCmdMemoryNotAssembled(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	h := &Host{c: c}
	_, err := h.cmdMemory([]string{"list"})
	if err == nil {
		t.Fatal("未装配 ctx.memory 应报错")
	}
	if !strings.Contains(err.Error(), "未装配") {
		t.Fatalf("错误要说清是「没装插件」:%v", err)
	}
}

// TestCmdMemoryBlankSubcommand 敲了空白子命令 ⇒ 当作查状态(不是记一条空记忆,
// 也不是报错)。这条之前没有任何用例覆盖,是最容易被顺手改坏的一处。
func TestCmdMemoryBlankSubcommand(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"   "})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "记忆:") {
		t.Fatalf("空白子命令应回落成查状态:\n%s", out)
	}
	if strings.Contains(out, "已记住") {
		t.Fatal("空白子命令不该被当成 add")
	}
}

// TestCmdMemoryProjectListing 装上 host-cwd-sessions 后,项目级记忆两条分支都能走到
// (`project` 有内容 / `list` 带项目段)。没有它时这两条分支永远走不到 —— 那正是
// 「项目级记忆是只读展示」这个口径的证据。
func TestCmdMemoryProjectListing(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, nil); err != nil {
		t.Fatal(err)
	}
	// host-cwd-sessions 需要 ctx.sessions(会话日志),先起它
	if _, err := (&hostsessionlog.Plugin{}).Start(c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostcwdsessions.Plugin{}).Start(c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostmemory.Plugin{}).Start(c, nil); err != nil {
		t.Fatal(err)
	}
	h := &Host{c: c}
	var cs sdk.CwdSessions
	if err := c.Inject("ctx.cwdSessions", &cs); err != nil {
		t.Fatal(err)
	}
	key := cs.Current()
	if key == "" {
		t.Skip("拿不到项目键,跳过")
	}
	p := filepath.Join(os.Getenv("GAH_HOME"), "memory", "projects", key+".md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("- [2026-10-02] 本项目的口径约定\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.cmdMemory([]string{"project"})
	if err != nil || !strings.Contains(out, "本项目的口径约定") {
		t.Fatalf("project 应列出项目级记忆:%v(%s)", err, out)
	}
	// 用户级为空但项目级有记忆时,list 必须**说清是项目级的**(说「还没有记忆」
	// 会让用户以为刚看到的那条不存在)
	list, err := h.cmdMemory([]string{"list"})
	if err != nil || !strings.Contains(list, "本项目的口径约定") ||
		strings.Contains(list, "还没有记忆。用") {
		t.Fatalf("list 应如实说「用户级还没有,本项目有」:%v(%s)", err, list)
	}
	// 用户级也有一条时,走「本项目:」分段
	if _, err := h.cmdMemory([]string{"add", "用户级的一条"}); err != nil {
		t.Fatal(err)
	}
	list2, err := h.cmdMemory([]string{"list"})
	if err != nil || !strings.Contains(list2, "本项目:") || !strings.Contains(list2, "本项目的口径约定") {
		t.Fatalf("两级都有时 list 应分段:%v(%s)", err, list2)
	}
}
