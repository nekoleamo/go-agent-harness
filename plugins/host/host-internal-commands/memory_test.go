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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-cwd-sessions"
	"github.com/nekoleamo/go-agent-harness/plugins/host/host-memory"
	hostsessionlog "github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
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

// —— P2 治理面 ——

// 没有项目上下文时 `add --project` 必须显式报错,**不能**悄悄写进用户级(那会让"只在本项目
// 生效"的意图变成全局生效,用户完全看不出来)。
func TestCmdMemoryAddProjectWithoutContext(t *testing.T) {
	h := memHost(t)
	if _, err := h.cmdMemory([]string{"add", "--project", "只在本项目生效的一条"}); err == nil {
		t.Fatal("没有项目上下文应报错")
	}
	if out, _ := h.cmdMemory([]string{"list"}); strings.Contains(out, "只在本项目生效的一条") {
		t.Fatalf("失败时不该落进用户级:\n%s", out)
	}
}

func TestCmdMemoryAddProjectWritesProjectFile(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	for _, p := range []sdk.Plugin{&hostsystemprompt.Plugin{}, &hostsessionlog.Plugin{},
		&hostcwdsessions.Plugin{}, &hostmemory.Plugin{}} {
		if _, err := p.Start(c, nil); err != nil {
			t.Fatal(err)
		}
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
	out, err := h.cmdMemory([]string{"add", "--project", "接口层统一返回 struct"})
	if err != nil || !strings.Contains(out, "已记住(本项目)") {
		t.Fatalf("写项目级: %v(%s)", err, out)
	}
	raw, err := os.ReadFile(memory.ProjectPath(key))
	if err != nil || !strings.Contains(string(raw), "接口层统一返回 struct") {
		t.Fatalf("应落进项目级文件: %v %s", err, raw)
	}
	if got, _ := os.ReadFile(memory.UserPath()); strings.Contains(string(got), "接口层统一返回 struct") {
		t.Fatal("项目级写入不该污染用户级")
	}
}

// 自动写入的条目要一眼看得出类型与置信度(面板与 /memory list 共用同一份展示行)。
func TestCmdMemoryListShowsKindBadge(t *testing.T) {
	h := memHost(t)
	var ms sdk.MemoryService
	if err := h.c.Inject("ctx.memory", &ms); err != nil {
		t.Fatal(err)
	}
	if err := memory.AppendMeta(memory.UserPath(), "交付前先跑 gate.sh", "s1", "convention", 0.92); err != nil {
		t.Fatal(err)
	}
	out, err := h.cmdMemory([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "类型 convention 0.92") {
		t.Fatalf("展示行应带类型与置信度(自动写入要可辨认):\n%s", out)
	}
}

// —— P2 过滤命令行 ——

func TestCmdMemoryListFilters(t *testing.T) {
	h := memHost(t)
	var ms sdk.MemoryService
	if err := h.c.Inject("ctx.memory", &ms); err != nil {
		t.Fatal(err)
	}
	// 三条:两条自动(不同 kind)+ 一条手工
	for _, e := range []struct {
		text, kind string
		conf       float64
	}{
		{"以后都用 pnpm", "preference", 0.92},
		{"交付前跑 gate.sh", "convention", 0.9},
	} {
		if err := memory.AppendMeta(memory.UserPath(), e.text, "s1", e.kind, e.conf); err != nil {
			t.Fatal(err)
		}
	}
	if err := ms.Add("手工记的一条偏好", ""); err != nil {
		t.Fatal(err)
	}

	out, err := h.cmdMemory([]string{"list", "--kind", "convention"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "交付前跑 gate.sh") || strings.Contains(out, "以后都用 pnpm") {
		t.Fatalf("--kind 应只留约定那条:\n%s", out)
	}
	if !strings.Contains(out, "过滤中 1/3 条") {
		t.Fatalf("应回显过滤口径(N/M),否则用户以为记忆少了:\n%s", out)
	}

	out, err = h.cmdMemory([]string{"list", "--auto"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "手工记的一条偏好") || !strings.Contains(out, "过滤中 2/3 条") {
		t.Fatalf("--auto 应只留自动写入的两条:\n%s", out)
	}

	// 无符合条件时如实说明(不返回空白)
	out, err = h.cmdMemory([]string{"list", "--kind", "pitfall"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "没有符合条件的记忆") {
		t.Fatalf("空结果应说明原因并给总量:\n%s", out)
	}

	// 参数错误显式报错
	for _, bad := range [][]string{
		{"list", "--kind"},
		{"list", "--stale", "abc"},
		{"list", "--kind", "nope"},
		{"list", "--wat"},
	} {
		if _, err := h.cmdMemory(bad); err == nil {
			t.Fatalf("参数错误应报错: %v", bad)
		}
	}
}

// 未实现过滤能力的旧构建:显式说"不支持",不静默返回全部(那会让用户以为筛选生效了)。
func TestCmdMemoryListFilterUnsupported(t *testing.T) {
	h := memHost(t)
	if _, err := h.memoryListFiltered(bareMemory{}, []string{"--auto"}); err == nil {
		t.Fatal("旧构建应显式报错")
	}
}

func TestCmdMemoryStatusShowsToolRemember(t *testing.T) {
	h := memHost(t)
	// 未装 session-log ⇒ 0 ⇒ 不显示该行(不摆空壳)
	out, err := h.cmdMemory([]string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "模型自记") {
		t.Fatalf("没有自记时不该显示该行:\n%s", out)
	}
}

func TestCmdMemoryListStale(t *testing.T) {
	h := memHost(t)
	// 直接写一条 2025 年的老条目(append 只写今天,构造超期只能自己落行)
	if err := os.MkdirAll(filepath.Dir(memory.UserPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(memory.UserPath(), []byte("- [2025-01-01] 很老的一条\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := h.cmdMemory([]string{"list", "--stale", "180"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "很老的一条") || !strings.Contains(out, "过滤中 1/1 条") {
		t.Fatalf("--stale 180 应命中超期条目:\n%s", out)
	}
	// 天数必须是正数
	if _, err := h.cmdMemory([]string{"list", "--stale", "0"}); err == nil {
		t.Fatal("--stale 0 应报错")
	}
	// 组合:超期 + 只看自动写入 ⇒ 0 条(老条目是手工的)
	out, err = h.cmdMemory([]string{"list", "--stale", "180", "--auto"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "没有符合条件的记忆") {
		t.Fatalf("组合过滤应给出空结果说明:\n%s", out)
	}
}

// 过滤视图**不能带序号**:序号是 rm 的抓手,而它取自全量展示序 ——
// 过滤后重新编号会让"删第 1 条"删到另一条(删错数据)。
func TestCmdMemoryListFilterHasNoIndex(t *testing.T) {
	h := memHost(t)
	if err := memory.AppendMeta(memory.UserPath(), "交付前跑 gate.sh", "s1", "convention", 0.9); err != nil {
		t.Fatal(err)
	}
	out, err := h.cmdMemory([]string{"list", "--kind", "convention"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, " 1. [") {
		t.Fatalf("过滤视图不该编号(否则 rm 序号指错条):\n%s", out)
	}
	if !strings.Contains(out, "不编号") {
		t.Fatalf("应说明不编号及删除方式:\n%s", out)
	}
	// 全量视图仍编号(rm 仍可用)
	all, err := h.cmdMemory([]string{"list"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all, " 1. [") {
		t.Fatalf("全量视图应保持编号:\n%s", all)
	}
}

// 命令级联提示(继续完善项):/memory 的二级提示必须按子命令说清可用参数 ——
// 一句泛泛的"子命令后的内容(可选)"等于没提示,用户不知道 list 还支持过滤。
func TestMemoryCommandArgLevelHints(t *testing.T) {
	c, cmds := buildEnv(t)
	dis, err := (&Plugin{}).Start(c, &sdk.Manifest{})
	if err != nil {
		t.Fatal(err)
	}
	defer dis()
	spec, ok := cmds.Get("memory")
	if !ok {
		t.Fatal("/memory 未注册")
	}
	if len(spec.Args) < 2 {
		t.Fatalf("/memory 应有两级参数定义,got %d", len(spec.Args))
	}
	// 一级枚举里必须能选到后面新增的子命令(status / extract)
	var subNames []string
	for _, o := range spec.Args[0].Options([]string{"memory"}) {
		subNames = append(subNames, o.Value)
	}
	for _, want := range []string{"status", "list", "add", "extract"} {
		found := false
		for _, n := range subNames {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("子命令枚举缺 %q(只能手打):%v", want, subNames)
		}
	}
	// 二级提示按子命令区分
	cases := map[string]string{
		"list":    "--kind",
		"add":     "--project",
		"rm":      "--from",
		"extract": "on | off",
		"propose": "候选内容",
		"accept":  "候选序号",
		"reject":  "候选序号",
	}
	for sub, want := range cases {
		free := spec.Args[1].FreeArgs([]string{"memory", sub})
		if len(free) == 0 || !strings.Contains(strings.Join(free, " "), want) {
			t.Fatalf("%s 的提示应含 %q,got %v", sub, want, free)
		}
	}
	// 无参数子命令 ⇒ 不回提示(选中即执行)
	for _, sub := range []string{"status", "candidates", "on", "off", "project", "accept-all", "reject-all"} {
		if free := spec.Args[1].FreeArgs([]string{"memory", sub}); len(free) != 0 {
			t.Fatalf("%s 不该有参数提示,got %v", sub, free)
		}
	}
	// 未知子命令 ⇒ 回落到泛提示(不是 nil:用户手打的内容也要有"继续输入"的余地)
	if free := spec.Args[1].FreeArgs([]string{"memory", "whatever"}); len(free) == 0 {
		t.Fatal("未知子命令应回落泛提示")
	}
	// picked 不足两级(还没选子命令)⇒ 不回提示
	if free := spec.Args[1].FreeArgs([]string{"memory"}); len(free) != 0 {
		t.Fatalf("只选了命令名时不该有二级提示,got %v", free)
	}
}

// —— 收录范围(scope)——

// 没有项目上下文时 `/memory scope` 必须显式报错:范围行落在**项目文件**里,
// 悄悄写进用户级会让"只描述本项目"变成全局口径。
func TestCmdMemoryScopeWithoutContext(t *testing.T) {
	h := memHost(t)
	if _, err := h.cmdMemory([]string{"scope", "只收编译约定"}); err == nil {
		t.Fatal("没有项目上下文应报错")
	}
	if out, err := h.cmdMemory([]string{"scope"}); err != nil || !strings.Contains(out, "还没设收录范围") {
		t.Fatalf("读范围应给空态提示: %v(%s)", err, out)
	}
}

func TestCmdMemoryScopeSetShowClear(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	c, _ := buildEnv(t)
	for _, p := range []sdk.Plugin{&hostsystemprompt.Plugin{}, &hostsessionlog.Plugin{},
		&hostcwdsessions.Plugin{}, &hostmemory.Plugin{}} {
		if _, err := p.Start(c, nil); err != nil {
			t.Fatal(err)
		}
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
	scope := "只收编译/测试/发布约定,不收个人偏好"
	out, err := h.cmdMemory([]string{"scope", scope})
	if err != nil || !strings.Contains(out, "已设置本项目收录范围") {
		t.Fatalf("设置范围: %v(%s)", err, out)
	}
	// 落盘 + 回显 + 出现在 /memory project 的展示里(三处同一份口径)。
	got, err := memory.ScopeLine(memory.ProjectPath(key))
	if err != nil || got != scope {
		t.Fatalf("应落进项目文件: %q %v", got, err)
	}
	if out, _ := h.cmdMemory([]string{"scope"}); !strings.Contains(out, scope) {
		t.Fatalf("回显应含范围: %s", out)
	}
	if out, _ := h.cmdMemory([]string{"project"}); !strings.Contains(out, "收录范围:"+scope) {
		t.Fatalf("/memory project 应显示收录范围: %s", out)
	}
	// 清除
	if out, err := h.cmdMemory([]string{"scope", "--clear"}); err != nil || !strings.Contains(out, "已清除") {
		t.Fatalf("清除范围: %v(%s)", err, out)
	}
	if got, _ := memory.ScopeLine(memory.ProjectPath(key)); got != "" {
		t.Fatalf("清除后不该还在: %q", got)
	}
}

// —— 成本账与试查(B 批)——

// 累计账要能回答"这套记忆值不值它的 token":分母、命中、回退、字节、截断都要有一行口径。
func TestCmdMemoryStatusShowsCostLedger(t *testing.T) {
	h := memHost(t)
	out, err := h.cmdMemory([]string{"status"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "成本账(按请求)") {
		t.Fatalf("status 应有累计成本口径: %s", out)
	}
	if !strings.Contains(out, "还没有记账") {
		t.Fatalf("没跑过请求时应说清账本为空,而不是给一个 0%%: %s", out)
	}
}

// 试查:命中/未命中都要说清,且**只读**(不进上下文、不加账)。
func TestCmdMemoryProbe(t *testing.T) {
	h := memHost(t)
	for _, s := range []string{"部署统一走 make deploy", "发布前先跑 gate 与覆盖率门"} {
		if _, err := h.cmdMemory([]string{"add", s}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := h.cmdMemory([]string{"probe", "部署"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "试查「部署」") || !strings.Contains(out, "参与打分") {
		t.Fatalf("试查应给出命中与参与打分的词: %s", out)
	}
	unrelated, err := h.cmdMemory([]string{"probe", "量子色动力学胶子禁闭"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(unrelated, "零命中") {
		t.Fatalf("无关句子应零命中: %s", unrelated)
	}
	if _, err := h.cmdMemory([]string{"probe"}); err == nil {
		t.Fatal("缺参数应报用法(否则会给一个看起来'零命中'的假答案)")
	}
	// 试查是只读诊断:不能给成本账加分母。
	if out, _ := h.cmdMemory([]string{"status"}); !strings.Contains(out, "还没有记账") {
		t.Fatalf("试查不该改账本: %s", out)
	}
}
