// probetimeout_test.go:2026-10-03 —— 探测超时的**语义分离**。
//
// 触发:验证回合里在满载机器上看到 `tests/` 包 4 个失败,报 `tool-kit` 以 role="" 启动失败。
// 根因:`rolesOf` 超时后归一到 `legacyRole()`,于是「**没测出来**」被当成
// 「**测出来了,是空**」—— 而多角色二进制被当成单角色启动,必然握手失败。
//
// ⚠️ 本文件的测试纪律(上一轮踩过的坑,逐条照做):
//  1. **超时用例不得带 t.Skip** —— 要么确定性走到超时分支,要么这条用例不存在。
//     上一轮的 `TestProbeRolesTruncationIsReported` 带着 Skip,而夹具把 3000ms 写成
//     `sleep 0.30`(0.3 秒)⇒ 超时路径压根没触发 ⇒ 那条用例**静默跳过了好几轮门禁**。
//  2. **夹具的时间用 FormatFloat 算**,不要拼字符串。
//  3. **每条都要反向验证**:把实现退回旧形态,确认用例真的会红。
//     上一轮有三条 placebo 用例是靠这个发现的。
package hostbridge

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// noopToolRegistry 最小工具注册表替身(本组用例的失败都发生在握手之前)。
type noopToolRegistry struct{}

func (noopToolRegistry) Register(sdk.Tool) sdk.Disposer { return func() {} }
func (noopToolRegistry) List() []sdk.ToolDefinition     { return nil }
func (noopToolRegistry) Get(string) (sdk.ToolDefinition, bool) {
	return sdk.ToolDefinition{}, false
}
func (noopToolRegistry) Execute(context.Context, string, string) (*sdk.ToolResult, error) {
	return &sdk.ToolResult{}, nil
}

// slowProbePlugin 造一个「`--roles` 要睡 ms 毫秒才回角色数组」的桩。
//
// 它是本次现象的最小复现物:一个**完全正常**、只是启动慢的多角色二进制。
func slowProbePlugin(t *testing.T, dir, name string, roleMs int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("桩脚本是 sh,Windows 上不跑(该路径由真机清单 W511 验)")
	}
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\n" +
		"sleep " + strconv.FormatFloat(float64(roleMs)/1000.0, 'f', -1, 64) + "\n" +
		"if [ \"$1\" = \"--roles\" ]; then echo '[\"tool-basic\",\"tool-mcp\"]'; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// withRolesProbeTimeout 临时改小角色探测超时,返回还原函数。
//
// 为什么要这个缝:超时分支是本批最难测的一条,而唯一确定性的造法是
// 「超时值 < 桩的睡眠」—— 一旦有人调大 rolesProbeTimeout,固定睡眠的桩会**静默地不再超时**,
// 用例照样绿(本轮已踩过一次:3s→10s 之后 sleep 4s 的桩失效)。
// 把超时本身做成可调,用例就与产品值的具体大小**解耦**。
func withRolesProbeTimeout(t *testing.T, d time.Duration) func() {
	t.Helper()
	old := rolesProbeTimeout
	rolesProbeTimeout = d
	return func() { rolesProbeTimeout = old }
}

// TestRolesOfTimeoutIsDistinguishable 「超时」与「不支持」必须可区分。
//
// 这是整批的核心断言:**第二个返回值**就是那个区分。
// 超时 ⇒ uncertain=true;非零退出(不认 --roles)⇒ uncertain=false。
func TestRolesOfTimeoutIsDistinguishable(t *testing.T) {
	dir := t.TempDir()

	// ① 不认 --roles 的二进制:快速非零退出 ⇒ 「不支持」,结论可靠。
	notSupport := filepath.Join(dir, "tool-plain")
	if err := os.WriteFile(notSupport, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Skip("桩脚本需要 POSIX shell")
	}
	roles, uncertain := rolesOf(notSupport)
	if uncertain {
		t.Error("快速非零退出不该判成「超时」—— 那会把「不支持」与「没测出来」混回去")
	}
	if len(roles) != 1 || roles[0] != "" {
		t.Errorf("不认 --roles ⇒ legacyRole,得 %v", roles)
	}

	// ② 睡得比超时还久的二进制 ⇒ 超时 ⇒ uncertain=true,且**仍然** legacyRole。
	//
	// 压小超时而不是把桩睡到 11 秒:后者会让单测变成 11 秒起步(CI 上不可接受),
	// 而且**调大 rolesProbeTimeout 时它会静默失效** —— 本轮就踩过一次:
	// 把超时从 3s 提到 10s 后,一个 sleep 4s 的桩**不再超时**,用例于是测了个寂寞。
	// 超时值变了而用例还绿,比用例红更危险。
	defer withRolesProbeTimeout(t, 50*time.Millisecond)()
	slow := slowProbePlugin(t, dir, "tool-slow", 300)
	roles, uncertain = rolesOf(slow)
	if !uncertain {
		t.Error("探测超时必须被标为 uncertain —— 否则与「不支持」不可区分")
	}
	// ⚠️ 关键:方向**不许变**。超时仍降级 legacyRole —— 第三方单角色插件在超时下
	// 恰好得到正确结果;若改成「超时就跳过」,那才是回归。
	if len(roles) != 1 || roles[0] != "" {
		t.Errorf("超时时**仍**应降级 legacyRole(兼容不破),得 %v", roles)
	}
}

// TestProbeBudgetExceedsSingleTimeout 不变量:**总预算必须 > 单探测超时**。
//
// 违反它 ⇒ `time.After(rolesProbeBudget)` 早于**任何**一次探测完成就返回
// ⇒ 预算机制事实上失效(每次都「只探测了少数几个」)。这条在调任一个常量时都会被守住。
func TestProbeBudgetExceedsSingleTimeout(t *testing.T) {
	if rolesProbeBudget <= rolesProbeTimeout {
		t.Fatalf("总预算(%v)必须大于单探测超时(%v):否则预算机制事实上失效",
			rolesProbeBudget, rolesProbeTimeout)
	}
}

// TestProbeRolesTimeoutIsLogged 超时的候选必须**留痕**(本批的全部可观测性落点)。
//
// 改前:超时被完全静默降级,日志一行都没有 ⇒ 多角色插件以缺角色参数的方式失败,
// 而日志里只有插件自己打的用法提示,与真实原因毫无关系。
func TestProbeRolesTimeoutIsLogged(t *testing.T) {
	dir := t.TempDir()
	slow := slowProbePlugin(t, dir, "tool-slow", 4000)
	fast := filepath.Join(dir, "tool-fast")
	if err := os.WriteFile(fast, []byte("#!/bin/sh\necho '[\"\"]'\n"), 0o755); err != nil {
		t.Skip("桩脚本需要 POSIX shell")
	}
	defer withRolesProbeTimeout(t, 50*time.Millisecond)()
	var buf syncBuffer
	b := &Bridge{dir: dir, entries: map[string]*extEntry{},
		lg: slog.New(slog.NewTextHandler(&buf, nil))}
	b.probeRoles([]string{slow, fast})
	logged := buf.String()
	if !strings.Contains(logged, "探测超时") {
		t.Errorf("探测超时必须留痕:\n%s", logged)
	}
	// 文案要说清「它仍然按单角色启动」,否则用户会以为插件没加载
	if !strings.Contains(logged, "按单角色启动") {
		t.Errorf("留痕文案要说明降级方向:\n%s", logged)
	}
	// 且要指名是哪一个二进制
	if !strings.Contains(logged, "tool-slow") {
		t.Errorf("留痕要指名候选路径:\n%s", logged)
	}
}

// TestLoadFailureReachesRejectedPanel 加载失败必须**进面板**(不再只在日志里)。
//
// 改前 `loadOne` 失败只 logErr ⇒ 面板只能显示被完整性闸拦下的那些,
// 「我装的插件不见了」在界面上**无解释**。
func TestLoadFailureReachesRejectedPanel(t *testing.T) {
	dir := t.TempDir()
	// 一个必然加载失败的二进制:能被 exec,但第一行不是合法握手行。
	// 刻意**不用 sleep 30**:那要等满 30s 握手超时(单测不可接受),而且「等满超时」
	// 与「立刻失败」验的是不同的东西 —— 这里要验的是「加载失败会进 rejected 面」。
	broken := filepath.Join(dir, "tool-broken")
	if err := os.WriteFile(broken, []byte("#!/bin/sh\necho 'not-a-handshake'\nexit 1\n"), 0o755); err != nil {
		t.Skip("桩脚本需要 POSIX shell")
	}
	// 工具注册表给个最小替身:失败发生在握手,走不到注册那一层。
	b := &Bridge{dir: dir, entries: map[string]*extEntry{},
		lg: slog.New(slog.DiscardHandler), tools: &noopToolRegistry{}}
	// 白名单不存在 ⇒ 不强制,让失败发生在「启动/握手」而不是「完整性闸」
	if err := b.loadEntries(); err != nil {
		t.Fatal(err)
	}
	var found sdk.RejectedPlugin
	for _, r := range b.Rejected() {
		if r.Name == "tool-broken" {
			found = r
		}
	}
	if found.Name == "" {
		t.Fatalf("加载失败的插件应出现在 Rejected() 里(否则面板上它只是「不见了」): %+v", b.Rejected())
	}
	if found.Kind != sdk.RejectedKindLoad {
		t.Errorf("Kind 应为 load(信任它也没用,要看的不是白名单): %q", found.Kind)
	}
	if found.Reason == "" {
		t.Error("必须带原因")
	}
}
