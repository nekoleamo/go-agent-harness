// 定时任务 NOND-W4 端到端:tests/ 入口矩阵新增项。
//
// 覆盖四件事(对应 W4 验收点):
//  1. 到点触发走**既有回合入口**:经 ctx.agentLoop → ctx.tools,产出落会话记录
//     (不变量「模型可见即已记录」),输入带 [计划:<名称>] 前缀;
//  2. 无人值守安全:定时触发 = 无确认通道 → 需审批的动作**一律拒绝**,连 open 档
//     也不例外;同一命令在有人值守的 open 档下必须放行(灵敏度:证明拦截来自
//     无人值守标记而非其它层误伤);
//  3. 持久化:计划落 $GAH_HOME/schedules/*.yaml,重新装配(模拟重启)后仍在,
//     且下次触发时间重算正确;
//  4. 卸载无残留:DisposeAll 后调度循环退出、拒绝新操作、无 goroutine 泄漏。
package tests

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	baseb "github.com/nekoleamo/go-agent-harness/bundles/base"
	"github.com/nekoleamo/go-agent-harness/core/config"
	"github.com/nekoleamo/go-agent-harness/core/ctx"
	"github.com/nekoleamo/go-agent-harness/core/event"
	"github.com/nekoleamo/go-agent-harness/core/plugin"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// buildScheduleEnv 装配带 host-schedule 的最小 base(GAH_HOME 由调用方指定,
// 便于同一 home 上做「重启」)。
func buildScheduleEnv(t *testing.T, home string, approval string, script []any) (sdk.Ctx, *plugin.Registry) {
	t.Helper()
	t.Setenv("GAH_HOME", home)
	logger := slog.New(slog.DiscardHandler)
	c := ctx.New(logger, event.New(logger))
	reg := plugin.New()
	tree := config.NewTree()
	tree.Apply([]config.Entry{
		{ID: "host-session-log"},
		{ID: "host-llm"},
		{ID: "host-tools"},
		{ID: "host-system-prompt"},
		{ID: "host-commands"},
		{ID: "llm-mock", Data: map[string]any{"script": script}},
		{ID: "tool-shell"},
		{ID: "policy-guard", Data: map[string]any{"approval": approval, "sandbox": "full-access", "sync": false}},
		{ID: "host-agent-loop"},
		{ID: "host-schedule", Data: map[string]any{"busy_wait_seconds": 0}},
	})
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
	t.Cleanup(func() { reg.DisposeAll() }) // disposers 幂等:用例内提前 DisposeAll 后再跑一次无害
	return c, reg
}

// scheduleSvcOf 取 ctx.schedule(装配缺失即失败,不静默跳过)。
func scheduleSvcOf(t *testing.T, c sdk.Ctx) sdk.ScheduleService {
	t.Helper()
	var sched sdk.ScheduleService
	if err := c.Inject("ctx.schedule", &sched); err != nil {
		t.Fatalf("ctx.schedule 应由 host-schedule 提供: %v", err)
	}
	return sched
}

// runPlanNow 立即触发并等终态(轮询 List:生产路径是异步的,这里只用有上限的等待)。
func runPlanNow(t *testing.T, sched sdk.ScheduleService, id string) sdk.Schedule {
	t.Helper()
	if err := sched.RunNow(id); err != nil {
		t.Fatalf("RunNow(%s) 失败: %v", id, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range sched.List() {
			if p.ID == id && p.LastStatus != "" {
				return p
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待定时回合终态超时")
	return sdk.Schedule{}
}

// TestScheduleTriggerRunsRoundAndRecords 触发即走既有回合入口,且产出落会话记录。
func TestScheduleTriggerRunsRoundAndRecords(t *testing.T) {
	home := t.TempDir()
	c, _ := buildScheduleEnv(t, home, "smart", []any{map[string]any{"text": "对账完成", "finish": "stop"}})
	sched := scheduleSvcOf(t, c)

	p, err := sched.Add(sdk.Schedule{Name: "每日对账", Cron: "0 8 * * *", Prompt: "整理昨日订单生成对账表", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.NextRun.IsZero() {
		t.Fatal("启用计划应有下次触发时间")
	}
	if got := p.NextRun.In(time.Local).Hour(); got != 8 {
		t.Fatalf("下次触发应为本地 08:00,cron=%s 得 %s(%d 时)", p.Cron, p.NextRun, got)
	}
	if fn := filepath.Join(home, "schedules", p.ID+".yaml"); !fileExists(fn) {
		t.Fatalf("计划应落 $GAH_HOME/schedules: %s", fn)
	}

	done := runPlanNow(t, sched, p.ID)
	if done.LastStatus != sdk.ScheduleRunOK {
		t.Fatalf("触发应成功,得 %s(%s)", done.LastStatus, done.LastError)
	}

	// 产出落会话记录:用户消息带计划前缀,助手回复可见(模型可见即已记录)
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	var userMsg, asstMsg string
	for _, m := range sessions.DeriveMessages() {
		switch m.Role {
		case sdk.RoleUser:
			userMsg += m.Content
		case sdk.RoleAssistant:
			asstMsg += m.Content
		}
	}
	if !strings.Contains(userMsg, "[计划:每日对账]") || !strings.Contains(userMsg, "整理昨日订单生成对账表") {
		t.Fatalf("触发输入应带计划前缀与描述,得 %q", userMsg)
	}
	if !strings.Contains(asstMsg, "对账完成") {
		t.Fatalf("定时回合的模型产出应落会话记录,得 %q", asstMsg)
	}
	// 运行记录落盘(重启后可见)
	raw, err := os.ReadFile(filepath.Join(home, "schedules", p.ID+".yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "last_status: ok") {
		t.Fatalf("运行记录应落盘: %s", raw)
	}
}

// TestScheduleUnattendedDeniesDangerousAction 无人值守下需审批动作一律拒(含 open 档),
// 有人值守同档位放行 —— 两侧对照证明归因。
func TestScheduleUnattendedDeniesDangerousAction(t *testing.T) {
	// 无人值守:计划触发 → 危险命令被拒,副作用不发生
	home := t.TempDir()
	victimA := filepath.Join(t.TempDir(), "victim-a.txt")
	writeFileT(t, victimA, "keep")
	cmdA := "rm -rf " + victimA
	c, _ := buildScheduleEnv(t, home, "open", []any{
		map[string]any{"tool": map[string]any{"name": "shell", "args": `{"command":` + jsonString(cmdA) + `}`}},
		map[string]any{"text": "已处理", "finish": "stop"},
	})
	sched := scheduleSvcOf(t, c)
	p, err := sched.Add(sdk.Schedule{Name: "清理", Cron: "* * * * *", Prompt: "删除临时文件", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	done := runPlanNow(t, sched, p.ID)
	if done.LastStatus != sdk.ScheduleRunOK {
		t.Fatalf("被拒是结构化结果,回合本身应正常结束,得 %s(%s)", done.LastStatus, done.LastError)
	}
	if !fileExists(victimA) {
		t.Fatal("无人值守下 open 档也不得执行危险命令(文件不该被删)")
	}
	var sessions sdk.SessionLog
	if err := c.Inject("ctx.sessions", &sessions); err != nil {
		t.Fatal(err)
	}
	var toolMsg string
	for _, m := range sessions.DeriveMessages() {
		if m.Role == sdk.RoleTool {
			toolMsg += m.Content
		}
	}
	if !strings.Contains(toolMsg, "无人值守") {
		t.Fatalf("拒绝原因应对模型可见且说明无人值守,得 %q", toolMsg)
	}

	// 有人值守对照:同一档位(open)同一命令 → 必须真正执行
	home2 := t.TempDir()
	victimB := filepath.Join(t.TempDir(), "victim-b.txt")
	writeFileT(t, victimB, "keep")
	cmdB := "rm -rf " + victimB
	c2, _ := buildScheduleEnv(t, home2, "open", []any{
		map[string]any{"tool": map[string]any{"name": "shell", "args": `{"command":` + jsonString(cmdB) + `}`}},
		map[string]any{"text": "已处理", "finish": "stop"},
	})
	var loop sdk.AgentLoop
	if err := c2.Inject("ctx.agentLoop", &loop); err != nil {
		t.Fatal(err)
	}
	if err := loop.Run(context.Background(), "删除临时文件"); err != nil {
		t.Fatal(err)
	}
	if fileExists(victimB) {
		t.Fatal("有人值守 open 档下同一命令应放行(否则说明拦截来自其它层,对照组失效)")
	}
}

// TestSchedulePersistsAcrossRestart 计划落盘,重启(重新装配)后保留且下次触发重算。
func TestSchedulePersistsAcrossRestart(t *testing.T) {
	home := t.TempDir()
	c1, reg1 := buildScheduleEnv(t, home, "smart", []any{map[string]any{"text": "noop", "finish": "stop"}})
	sched1 := scheduleSvcOf(t, c1)
	added, err := sched1.Add(sdk.Schedule{Name: "每月对账", Cron: "0 8 5 * *", Prompt: "生成月度对账", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sched1.Add(sdk.Schedule{Name: "停用的", Cron: "0 9 * * *", Prompt: "不该排期", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	reg1.DisposeAll() // 模拟进程退出

	c2, _ := buildScheduleEnv(t, home, "smart", []any{map[string]any{"text": "noop", "finish": "stop"}})
	sched2 := scheduleSvcOf(t, c2)
	plans := sched2.List()
	if len(plans) != 2 {
		t.Fatalf("重启后应保留 2 条计划,得 %d: %+v", len(plans), plans)
	}
	var got *sdk.Schedule
	for i := range plans {
		if plans[i].ID == added.ID {
			got = &plans[i]
		}
	}
	if got == nil {
		t.Fatal("重启后应按原 ID 找回计划")
	}
	if got.Name != "每月对账" || got.Cron != "0 8 5 * *" || got.Prompt != "生成月度对账" || !got.Enabled {
		t.Fatalf("字段未原样保留: %+v", *got)
	}
	loc := got.NextRun.In(time.Local)
	if got.NextRun.IsZero() || got.NextRun.Before(time.Now()) {
		t.Fatalf("下次触发应为未来时刻,得 %s", got.NextRun)
	}
	if loc.Day() != 5 || loc.Hour() != 8 || loc.Minute() != 0 {
		t.Fatalf("下次触发应为本地 5 号 08:00,得 %s", loc)
	}
	for _, p := range plans {
		if p.Name == "停用的" && !p.NextRun.IsZero() {
			t.Fatalf("停用计划不该排期: %+v", p)
		}
	}
}

// TestScheduleUnloadLeavesNoResidue 卸载即撤销:循环退出、拒绝新操作、无 goroutine 泄漏。
func TestScheduleUnloadLeavesNoResidue(t *testing.T) {
	before := runtime.NumGoroutine()
	home := t.TempDir()
	c, reg := buildScheduleEnv(t, home, "smart", []any{map[string]any{"text": "noop", "finish": "stop"}})
	sched := scheduleSvcOf(t, c)
	p, err := sched.Add(sdk.Schedule{Name: "清理", Cron: "* * * * *", Prompt: "清理临时文件", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	reg.DisposeAll()

	if _, err := sched.Add(sdk.Schedule{Name: "x", Cron: "* * * * *", Prompt: "y"}); err == nil {
		t.Fatal("卸载后应拒绝新增计划")
	}
	if err := sched.RunNow(p.ID); err == nil {
		t.Fatal("卸载后应拒绝触发")
	}
	if err := c.Inject("ctx.schedule", new(sdk.ScheduleService)); err == nil {
		t.Fatal("卸载后 ctx.schedule 应已撤销")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > before+3 {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+3 {
		t.Fatalf("疑似 goroutine/定时器泄漏:before=%d after=%d", before, after)
	}
}

// fileExists 文件是否存在(测试小工具)。
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// writeFileT 写测试文件(失败即终止用例)。
func writeFileT(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
