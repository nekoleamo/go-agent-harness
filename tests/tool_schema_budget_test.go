// 固定前缀体积棘轮(2026-10-20 A5):工具 schema 的总体积不许悄悄变大。
//
// 为什么要有这条门:工具 schema 是**每轮都发**的固定前缀(见交付分析 §1.1 的实测账:
// 27 个工具 = 16.9KB ≈ 4.2K token/轮)。它是**沉默的成本** —— 加一个工具、改一段描述,
// 用户看不到任何提示,只是每轮多付一点钱;等几个月后回头看,固定前缀已经涨了一倍。
//
// 所以把它变成门:**超基线必须显式改基线**(并说明为什么值得),而不是"顺手就加了"。
//
// 基线怎么来的:本文件下方的常量是 2026-10-20 实测值(瘦身之后)。
// 改基线时必须同步改本文件里的注释,写清「为什么这次值得」。
//
// 口径:json.Marshal(tools) 的字节数(模型侧看到的就是这份 JSON 的等价物)。
package tests

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// toolSchemaBudgetBytes 工具 schema JSON 的字节上限(2026-10-20 实测 18333 字节 / 29 个工具)。
//
// 改动说明(留档):
//   - 2026-10-20:A5 描述瘦身。subagent 2416→1611 · image_ops 2298→1985 ·
//     auto_plan 1745→1483 · todo 1452→1263;同一套工具合计 19902→18333 字节
//     (≈ -1.6K 字节/轮 ≈ -400 token/轮)。
//     注:file_read 因 A1 增加了 offset/limit 两个参数(+265 字节 schema),这是**有意**的
//     代价 —— 换来「一次读取不再灌爆上下文」,净值仍为负。
const toolSchemaBudgetBytes = 18333

// windows 上多一个 powershell 工具(NOND-W1b;POSIX 没有等价执行器,所以 macOS/Linux 的
// 基线里没有它)。2026-10-24 实测:Windows 合计 18920 = 18333 + powershell 586(+ 逗号 1)。
// 这条门此前只在 macOS/Linux 上量过 —— Windows CI 因此一直红在 18920 vs 18333。
func toolSchemaBudget() int {
	if runtime.GOOS == "windows" {
		return 18920
	}
	return toolSchemaBudgetBytes
}

// budgetToolPlugins 参与预算的**仓库自带**工具插件(固定清单)。
//
// 为什么是固定清单:外部插件(extplugins/*)的 schema 不在本仓库控制内,放进基线会让门
// 随外部升级漂移;而本仓库自带的这些是「加一个就涨一次」的主体,正是要盯住的那部分。
//
// 为什么需要 host-fanout:tool-subagent 依赖 ctx.fanout(缺它显式失败,不给静默跳过)。
var budgetToolPlugins = []string{
	"host-fanout", "host-schedule", "host-agent-loop", "host-roles", "host-skills",
	"tool-shell", "tool-files", "tool-web", "tool-memory", "tool-ask",
	"tool-todo", "tool-image", "tool-auto-plan", "tool-schedule",
	"tool-session-search", "tool-subagent",
}

// buildBudgetEnv 装配一份带全部自带工具的宿主(只用于量体积;不发模型请求)。
func buildBudgetEnv(t *testing.T) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	entries := []config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "llm-mock"},
	}
	for _, id := range budgetToolPlugins {
		entries = append(entries, config.Entry{ID: id})
	}
	tree.Apply(entries)
	if err := c.Provide("system.registry", reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("system.catalogue", catalogueInfoForTest()); err != nil {
		t.Fatal(err)
	}
	if err := baseb.RegisterAll(reg, tree); err != nil {
		t.Fatal(err)
	}
	if err := reg.StartSubset(c, enabledSetForTest(tree)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reg.DisposeAll() })
	return c
}

// TestToolSchemaBudget 工具 schema 总体积不许超基线。
//
// 反向验证:把基线改成 1 字节,本用例必须红(证明它真的在量体积)。
func TestToolSchemaBudget(t *testing.T) {
	c := buildBudgetEnv(t)
	var reg sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &reg); err != nil {
		t.Fatal(err)
	}
	tools := reg.List()
	blob, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) > toolSchemaBudget() {
		t.Fatalf("工具 schema 体积 %d 字节 > 基线 %d(每轮都发,涨了就是每轮多付钱)\n"+
			"要加就显式改基线(tests/tool_schema_budget_test.go 里写清为什么值得),别默默涨。\n逐工具:\n%s",
			len(blob), toolSchemaBudget(), perToolSizes(tools))
	}
}

// perToolSizes 逐工具体积(失败信息用:让人一眼看出是谁变大)。
func perToolSizes(tools []sdk.ToolDefinition) string {
	out := ""
	for _, t := range tools {
		b, err := json.Marshal(t)
		if err != nil {
			continue
		}
		out += fmt.Sprintf("    %-22s %6d 字节\n", t.Name, len(b))
	}
	return out
}
