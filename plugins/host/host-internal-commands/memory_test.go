package hostintcmd

// /memory 命令面的行为契约(第一百零九批)。
//
// 用**真实插件装配**(host-system-prompt + host-memory),不用假实现:命令口径与插件
// 口径必须一起验 —— 假实现会让"序号到底按哪个顺序"这类问题在测试里看不出来。
//
// 重点:写入只由人发起、List 的展示序(新的在前)与 rm 的序号一致、--from 按来源整段删、
// 没装插件时显式说没装。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-memory"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-system-prompt"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

func memHost(t *testing.T) *Host {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	if _, err := (&hostsystemprompt.Plugin{}).Start(c, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (&hostmemory.Plugin{}).Start(c, nil); err != nil {
		t.Fatal(err)
	}
	return &Host{c: c}
}

func TestCmdMemoryAddListRemove(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"add", "交付前先跑一遍单测"})
	if err != nil || !strings.Contains(out, "已记住") {
		t.Fatalf("add: %v(%s)", err, out)
	}
	list, err := h.cmdMemory([]string{"list"})
	if err != nil || !strings.Contains(list, "交付前先跑一遍单测") {
		t.Fatalf("list 应看到刚记的: %v(%s)", err, list)
	}
	// 序号删除:List 里的编号
	if _, err := h.cmdMemory([]string{"rm", "1"}); err != nil {
		t.Fatalf("rm 1: %v", err)
	}
	list2, _ := h.cmdMemory([]string{"list"})
	if strings.Contains(list2, "交付前先跑一遍单测") {
		t.Fatalf("rm 后不应还在:\n%s", list2)
	}
	if !strings.Contains(list2, "还没有记忆") {
		t.Fatalf("删空后应显示空态:\n%s", list2)
	}
}

func TestCmdMemoryBareTextTreatedAsAdd(t *testing.T) {
	h := memHost(t)
	// 用户没打 add,直接说了内容 —— 善意当作 add(这是最自然的用法)
	if _, err := h.cmdMemory([]string{"周报里先写结论"}); err != nil {
		t.Fatalf("裸文本应被当作 add: %v", err)
	}
	list, _ := h.cmdMemory([]string{"list"})
	if !strings.Contains(list, "先写结论") {
		t.Fatalf("没记上:\n%s", list)
	}
}

func TestCmdMemoryRemoveBySource(t *testing.T) {
	h := memHost(t)
	_, _ = h.cmdMemory([]string{"add", "带来源的一条"})
	out, err := h.cmdMemory([]string{"rm", "--from", "sess-9"})
	if err != nil {
		t.Fatalf("--from 应可解析: %v", err)
	}
	// 手工记的没有来源,所以是"没有来自该会话的记忆" —— 回执要说清,不能谎称删掉了
	if !strings.Contains(out, "没有来自") {
		t.Fatalf("回执应如实说明没删到: %s", out)
	}
}

func TestCmdMemoryToggle(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"off"})
	if err != nil || !strings.Contains(out, "已关闭") {
		t.Fatalf("off: %v(%s)", err, out)
	}
	out2, err := h.cmdMemory([]string{"on"})
	if err != nil || !strings.Contains(out2, "开启") {
		t.Fatalf("on: %v(%s)", err, out2)
	}
}

func TestCmdMemoryStatusMentionsUsage(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory(nil)
	if err != nil {
		t.Fatalf("无参应给概览: %v", err)
	}
	for _, must := range []string{"/memory add", "/memory list", "/memory rm", "预算"} {
		if !strings.Contains(out, must) {
			t.Fatalf("概览应含用法 %q:\n%s", must, out)
		}
	}
}

func TestCmdMemoryBadIndex(t *testing.T) {
	h := memHost(t)
	if _, err := h.cmdMemory([]string{"rm", "abc"}); err == nil {
		t.Fatal("非数字序号应报错")
	}
	if _, err := h.cmdMemory([]string{"add"}); err == nil {
		t.Fatal("add 缺内容应给用法")
	}
}

func TestMemoryCommandRegistered(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	_, cmds := newHost(t)
	if _, ok := cmds.Get("memory"); !ok {
		t.Fatal("命令未注册: /memory")
	}
}

// TestCmdMemoryProjectView 项目级记忆为空时应说清"没有"而不是空白。
func TestCmdMemoryProjectView(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"project"})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if !strings.Contains(out, "还没有项目级记忆") {
		t.Fatalf("空项目记忆应如实说明:\n%s", out)
	}
}

// TestCmdMemoryRemoveFromMissingSourceValue --from 后面没给会话 ⇒ 给用法(不静默删)。
func TestCmdMemoryRemoveFromMissingSourceValue(t *testing.T) {
	h := memHost(t)
	if _, err := h.cmdMemory([]string{"rm", "--from"}); err == nil {
		t.Fatal("--from 缺会话应报错并给用法")
	}
}

// TestCmdMemoryListEmptyGivesHint 空列表时给一句"怎么用",不是空白(对齐其它命令口径)。
func TestCmdMemoryListEmptyGivesHint(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"list"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "/memory add") {
		t.Fatalf("空列表应告诉用户怎么记:\n%s", out)
	}
}

// —— 自动调取的观测段(P0) ——
//
// status 要能回答"为什么没看到记忆":没跑过回合 / 有记忆但没命中 / 命中但被预算挤掉,
// 三种的下一步动作完全不同。

func TestCmdMemoryStatusShowsRetrieveMode(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "自动调取:per_step") || !strings.Contains(out, "还没跑过回合") {
		t.Fatalf("未跑过回合时应说明调取模式与无观测值:\n%s", out)
	}
}

func TestCmdMemoryStatusShowsLastInjection(t *testing.T) {
	h := memHost(t)
	if _, err := h.cmdMemory([]string{"add", "部署统一走 make deploy"}); err != nil {
		t.Fatal(err)
	}
	var sp sdk.SystemPromptService
	if err := h.c.Inject("ctx.systemPrompt", &sp); err != nil {
		t.Fatal(err)
	}
	cp, ok := sp.(sdk.ContextualSystemPrompt)
	if !ok {
		t.Fatal("host-system-prompt 应实现 ContextualSystemPrompt")
	}
	// 走真实组装链:系统提示(稳定块)+ 尾部追加(自动调取)。
	ctx := sdk.WithQuery(context.Background(), "怎么部署")
	msgs := cp.AssembleFor(ctx, nil, nil)
	var aug sdk.MessageAugmenter
	if err := h.c.Inject("ctx.messageAugment", &aug); err != nil || aug == nil {
		t.Fatalf("ctx.messageAugment 未装配: %v", err)
	}
	_ = aug.Augment(ctx, msgs)
	out, err := h.cmdMemory([]string{"status"})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	// 口径:稳定块条数与本轮追加条数分开报(只报一个会让人误判"记忆没生效")。
	if !strings.Contains(out, "稳定块 1 条") || !strings.Contains(out, "怎么部署") {
		t.Fatalf("status 应回显稳定块条数与本轮查询:\n%s", out)
	}
}

// bareMemory 只实现 MemoryService(模拟未支持自动调取的旧构建)。
type bareMemory struct{}

func (bareMemory) Enabled() bool              { return true }
func (bareMemory) SetEnabled(bool) bool       { return true }
func (bareMemory) Budget() int                { return 2048 }
func (bareMemory) Add(string, string) error   { return nil }
func (bareMemory) List() []string             { return nil }
func (bareMemory) Remove(int) (string, error) { return "", nil }
func (bareMemory) RemoveBySource(string) (int, error) {
	return 0, nil
}

func TestCmdMemoryStatusOmitsRetrieveForLegacyBuild(t *testing.T) {
	out := retrieveStatusLine(bareMemory{})
	if out != "" {
		t.Fatalf("未实现 MemoryRetrieveStats 的构建不该摆空壳段,got %q", out)
	}
}

// statsMemory 在 bareMemory 之上实现 sdk.MemoryRetrieveStats(演练观测段的四种口径)。
type statsMemory struct {
	bareMemory
	st   sdk.MemoryInjectStats
	mode string
}

func (m statsMemory) LastInject() sdk.MemoryInjectStats { return m.st }
func (m statsMemory) RetrieveMode() string              { return m.mode }

// TestRetrieveStatusLineBranches 四种观测形态必须说清不同的事 ——
// 全都退化成"没有记忆"就等于用户没法判断下一步该修什么。
func TestRetrieveStatusLineBranches(t *testing.T) {
	cases := []struct {
		name string
		st   sdk.MemoryInjectStats
		want string
	}{
		{"未跑过回合", sdk.MemoryInjectStats{Mode: "per_step"}, "还没跑过回合"},
		{"追加且截断", sdk.MemoryInjectStats{Mode: "per_step", Query: "部署", Stable: 2, Items: 3,
			Total: 9, Bytes: 900, Truncated: true, At: time.Now()}, "其余超预算"},
		{"追加未截断", sdk.MemoryInjectStats{Mode: "per_step", Query: "部署", Stable: 2, Items: 2,
			Bytes: 400, Total: 2, At: time.Now()}, "本轮追加 2 条"},
		{"命中但未追加", sdk.MemoryInjectStats{Mode: "per_step", Query: "部署", Stable: 2,
			Total: 3, At: time.Now()}, "都已在稳定块里或放不进预算"},
		{"零命中", sdk.MemoryInjectStats{Mode: "per_step", Query: "数据库", Stable: 2,
			At: time.Now()}, "本轮未命中"},
		{"回退模式无查询", sdk.MemoryInjectStats{Mode: "fallback", Stable: 1,
			At: time.Now()}, "回退全量注入"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := retrieveStatusLine(statsMemory{st: c.st, mode: c.st.Mode})
			if !strings.Contains(got, c.want) {
				t.Fatalf("应含 %q,got %q", c.want, got)
			}
		})
	}
}

func TestClipQuery(t *testing.T) {
	long := strings.Repeat("部署", 40)
	if got := clipQuery(long); !strings.HasSuffix(got, "…") {
		t.Fatalf("超长查询应截短并可辨认,got %q", got)
	}
	if got := clipQuery("  "); got != "(无查询:回退全量注入)" {
		t.Fatalf("空查询应说明回退,got %q", got)
	}
	if got := clipQuery("部署"); got != "部署" {
		t.Fatalf("短查询原样回显,got %q", got)
	}
}

// —— 自动提取的命令面(P1) ——

func TestCmdMemoryExtractToggle(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"extract", "off"})
	if err != nil || !strings.Contains(out, "已关闭") {
		t.Fatalf("extract off: %v(%s)", err, out)
	}
	if !prefs.Load().MemoryExtractOff {
		t.Fatal("off 应落到偏好位(跨端共享)")
	}
	out, err = h.cmdMemory([]string{"extract", "on"})
	if err != nil || !strings.Contains(out, "已开启") {
		t.Fatalf("extract on: %v(%s)", err, out)
	}
	if prefs.Load().MemoryExtractOff {
		t.Fatal("on 应清掉偏好位")
	}
	// 裸 /memory extract = 状态
	out, err = h.cmdMemory([]string{"extract"})
	if err != nil || !strings.Contains(out, "自动提取:") {
		t.Fatalf("extract 无参应回状态: %v(%s)", err, out)
	}
	// 未知参数显式报错(不静默当状态)
	if _, err := h.cmdMemory([]string{"extract", "maybe"}); err == nil {
		t.Fatal("未知参数应报错")
	}
}

func TestCmdMemoryExtractUnsupported(t *testing.T) {
	h := memHost(t)
	// bareMemory 只实现 MemoryService ⇒ 命令面必须显式说"不支持",不假装成功。
	if _, err := h.memoryExtract(bareMemory{}, "off"); err == nil {
		t.Fatal("未实现 MemoryExtractStats 的构建应显式报错")
	}
}

func TestExtractStatusTextBranches(t *testing.T) {
	base := sdk.MemoryExtractSnapshot{At: time.Now()}
	cases := []struct {
		name string
		snap sdk.MemoryExtractSnapshot
		on   bool
		want string
	}{
		{"未触发", sdk.MemoryExtractSnapshot{}, true, "还没触发过"},
		{"规则未命中", base, true, "规则闸未命中"},
		{"产出", sdk.MemoryExtractSnapshot{At: time.Now(), RuleHit: "长期指令",
			Produced: 2, Written: 1, Candidate: 1, Model: "m"}, true, "直写 1"},
		{"失败", sdk.MemoryExtractSnapshot{At: time.Now(), Skipped: "llm", Err: "端点 500"}, true, "没做成"},
		{"关闭", base, false, "自动提取:关"},
	}
	for _, c := range cases {
		var es sdk.MemoryExtractStats = fakeExtractStats{snap: c.snap, on: c.on}
		out := extractStatusText(es)
		if !strings.Contains(out, c.want) {
			t.Fatalf("%s: 应含 %q,got %q", c.name, c.want, out)
		}
	}
}

// fakeExtractStats 只实现提取观测(命令面分支判定用)。
type fakeExtractStats struct {
	snap sdk.MemoryExtractSnapshot
	on   bool
}

func (f fakeExtractStats) LastExtract() sdk.MemoryExtractSnapshot { return f.snap }
func (f fakeExtractStats) ExtractEnabled() bool                   { return f.on }
