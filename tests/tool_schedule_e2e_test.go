// NOND-W4b 端到端:模型侧 schedule 工具。
//
// 两条不变量(比「工具能用」更重要):
//  1. **默认空 = 行为零变化**:base bundle 默认不装配 tool-schedule(条目 enabled: false),
//     工具清单里没有 schedule —— 老用户升级后模型看不到任何新工具、行为不变。
//  2. 显式打开后走**真实管线**:schedule 工具经 ctx.tools 注册、经 ctx.schedule 落到
//     $GAH_HOME/schedules/*.yaml(不是测试替身),list 能回读、update 不抹掉未提字段。
package tests

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildToolScheduleEnv 装配带(或不带)tool-schedule 的最小 base。
func buildToolScheduleEnv(t *testing.T, home string, withTool bool) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", home)
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	entries := []config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "host-commands"},
		{ID: "llm-mock", Data: map[string]any{"script": []any{map[string]any{"text": "ok", "finish": "stop"}}}},
		{ID: "policy-guard", Data: map[string]any{"approval": "smart", "sandbox": "full-access", "sync": false}},
		{ID: "host-agent-loop"},
		{ID: "host-schedule"},
	}
	if withTool {
		entries = append(entries, config.Entry{ID: "tool-schedule"})
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

// toolsOf 取 ctx.tools(装配缺失即失败,不静默跳过)。
func toolsOf(t *testing.T, c sdk.Ctx) sdk.ToolRegistry {
	t.Helper()
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatalf("ctx.tools 应由 host-tools 提供: %v", err)
	}
	return tools
}

// execTool 经真实管线执行并解出结果(业务失败在 result.Error 里,不中断 turn)。
func execTool(t *testing.T, tools sdk.ToolRegistry, name, args string) map[string]any {
	t.Helper()
	res, err := tools.Execute(context.Background(), name, args)
	if err != nil {
		t.Fatalf("工具执行不应报错: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("工具返回错误: %s", res.Error)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(res.Content), &m); err != nil {
		t.Fatalf("结果不是 JSON: %q (%v)", res.Content, err)
	}
	return m
}

// TestToolScheduleAbsentByDefault 默认装配下模型看不到 schedule 工具(零行为变化)。
func TestToolScheduleAbsentByDefault(t *testing.T) {
	c := buildToolScheduleEnv(t, t.TempDir(), false)
	tools := toolsOf(t, c)
	if _, ok := tools.Get("schedule"); ok {
		t.Fatal("tool-schedule 默认应停用:模型可见工具清单里不应有 schedule")
	}
	// 停用 ≠ 报错:host-schedule 本身仍在(用户侧 /schedule 可用),只是模型没有工具
	if _, err := tools.Execute(context.Background(), "schedule", `{"action":"list"}`); err != nil {
		t.Fatalf("执行不存在的工具应回结构化错误而非 error: %v", err)
	}
}

// TestToolScheduleEnabledEndToEnd 显式启用:工具真注册、真落盘、list 回读、update 部分更新。
func TestToolScheduleEnabledEndToEnd(t *testing.T) {
	home := t.TempDir()
	c := buildToolScheduleEnv(t, home, true)
	tools := toolsOf(t, c)
	if _, ok := tools.Get("schedule"); !ok {
		t.Fatal("启用后 schedule 应进入模型可见工具清单")
	}

	m := execTool(t, tools, "schedule", `{"action":"add","name":"晨会简报","cron":"0 9 * * 1","prompt":"汇总上周提交"}`)
	created, ok := m["created"].(map[string]any)
	if !ok {
		t.Fatalf("add 应回 created: %+v", m)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("add 应回计划 id: %+v", created)
	}
	// 真落盘($GAH_HOME/schedules/<id>.yaml),不是内存替身
	if !fileExists(filepath.Join(home, "schedules", id+".yaml")) {
		t.Fatalf("计划应落 $GAH_HOME/schedules/%s.yaml", id)
	}

	// 部分更新:只给 cron,不得抹掉 name/prompt(服务层 Update 是整组覆盖,工具须先读回)
	execTool(t, tools, "schedule", `{"action":"update","id":"`+id+`","cron":"30 8 * * 1"}`)
	sched := scheduleSvcOf(t, c)
	var got sdk.Schedule
	for _, p := range sched.List() {
		if p.ID == id {
			got = p
		}
	}
	if got.Cron != "30 8 * * 1" || got.Name != "晨会简报" || got.Prompt != "汇总上周提交" || !got.Enabled {
		t.Fatalf("改 cron 不应影响其它字段: %+v", got)
	}

	// list 回读:字段齐全且含宿主派生的下次触发时间
	m = execTool(t, tools, "schedule", `{"action":"list"}`)
	plans, _ := m["plans"].([]any)
	if len(plans) != 1 {
		t.Fatalf("list 应回 1 条: %+v", m)
	}
	view := plans[0].(map[string]any)
	if view["id"] != id || view["cron"] != "30 8 * * 1" || view["name"] != "晨会简报" {
		t.Fatalf("list 字段不符: %+v", view)
	}
	if s, _ := view["next_run"].(string); s == "" || !strings.Contains(s, "08:30") {
		t.Fatalf("list 应含下次触发时间(本地 08:30): %+v", view)
	}

	// remove 后列表空(删除也要真落盘生效)
	execTool(t, tools, "schedule", `{"action":"remove","id":"`+id+`"}`)
	if n := len(sched.List()); n != 0 {
		t.Fatalf("remove 后应无计划,得 %d 条", n)
	}
	if fileExists(filepath.Join(home, "schedules", id+".yaml")) {
		t.Fatal("remove 应删掉落盘文件")
	}
	// 卸载无残留:工具随 Disposer 撤销
	if _, err := tools.Execute(context.Background(), "schedule", `{"action":"list"}`); err != nil {
		t.Fatalf("卸载后执行应回结构化错误: %v", err)
	}
}
