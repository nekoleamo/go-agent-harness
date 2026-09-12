// schedule 单测:计划 CRUD/校验、载入排期、触发(无人值守 + 前缀 + 状态回写)、
// 忙跳过、卸载无残留(循环退出 + 拒绝新操作)。
package hostschedule

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 测试替身 ——

// fakeClock 可注入时钟(触发与排期计算全靠它,测试里不睡觉)。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t time.Time) *fakeClock { return &fakeClock{t: t} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// loopCall 一次回合调用记录。
type loopCall struct {
	input      string
	unattended bool
}

// fakeLoop 假 agent 循环(记录输入与无人值守标记;可注入错误/阻塞)。
type fakeLoop struct {
	mu      sync.Mutex
	calls   []loopCall
	err     error
	started chan loopCall
	release chan struct{}
}

func (f *fakeLoop) Run(ctx context.Context, input string) error {
	call := loopCall{input: input, unattended: sdk.UnattendedOf(ctx)}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
	if f.started != nil {
		select {
		case f.started <- call:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return f.err
}

func (f *fakeLoop) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeLoop) last() loopCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return loopCall{}
	}
	return f.calls[len(f.calls)-1]
}

// fakeTC 假回合控制(忙/闲可控)。
type fakeTC struct {
	mu      sync.Mutex
	running bool
}

func (t *fakeTC) Running() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

func (t *fakeTC) Cancel() {}

func (t *fakeTC) set(v bool) {
	t.mu.Lock()
	t.running = v
	t.mu.Unlock()
}

// newTestScheduler 构造带假时钟的调度器并启动循环(测试结束自动 Stop)。
func newTestScheduler(t *testing.T, loop sdk.AgentLoop, clock *fakeClock) *Scheduler {
	t.Helper()
	setupHome(t)
	s := New(loop, slog.New(slog.DiscardHandler))
	s.SetClock(clock.Now)
	if err := s.launch(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// —— 用例 ——

func TestAddValidation(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)
	cases := []struct {
		name string
		in   sdk.Schedule
		want string
	}{
		{"名称空", sdk.Schedule{Name: "", Cron: "0 8 * * *", Prompt: "跑"}, "计划名称不能为空"},
		{"描述空", sdk.Schedule{Name: "对账", Cron: "0 8 * * *", Prompt: "  "}, "任务描述不能为空"},
		{"名称过长", sdk.Schedule{Name: strings.Repeat("字", maxNameRunes+1), Cron: "0 8 * * *", Prompt: "跑"}, "计划名称过长"},
		{"描述过长", sdk.Schedule{Name: "n", Cron: "0 8 * * *", Prompt: strings.Repeat("字", maxPromptRunes+1)}, "任务描述过长"},
		{"cron 非法", sdk.Schedule{Name: "n", Cron: "0 8 * *", Prompt: "跑"}, "5 字段"},
		{"cron 永不触发", sdk.Schedule{Name: "n", Cron: "0 0 30 2 *", Prompt: "跑"}, "4 年内无匹配"},
	}
	for _, tc := range cases {
		_, err := s.Add(tc.in)
		if err == nil {
			t.Fatalf("%s 应被拒绝", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s:错误信息应含 %q,得 %q", tc.name, tc.want, err.Error())
		}
	}
}

func TestAddListUpdateRemove(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)

	p, err := s.Add(sdk.Schedule{Name: "每日对账", Cron: "0 8 * * *", Prompt: "生成昨日对账", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !validID(p.ID) || !strings.HasPrefix(p.ID, "sched-") {
		t.Fatalf("应自动生成 ID,得 %q", p.ID)
	}
	if p.CreatedAt.IsZero() {
		t.Fatal("应补 CreatedAt")
	}
	if got, want := p.NextRun, time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("NextRun 应为当天 08:00,得 %s", got)
	}
	if len(s.List()) != 1 {
		t.Fatalf("List 应有 1 条: %+v", s.List())
	}
	// 重复 ID 显式拒绝
	if _, err := s.Add(sdk.Schedule{ID: p.ID, Name: "另一个", Cron: "0 9 * * *", Prompt: "跑"}); err == nil {
		t.Fatal("重复 ID 应被拒绝")
	}

	// Update:改 cron 后重算 NextRun
	upd, err := s.Update(sdk.Schedule{ID: p.ID, Name: "每周对账", Cron: "0 8 * * 1", Prompt: "生成上周对账", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Name != "每周对账" || upd.Cron != "0 8 * * 1" {
		t.Fatalf("字段未更新: %+v", upd)
	}
	if got, want := upd.NextRun, time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("改 cron 后 NextRun 应为下周一 08:00,得 %s", got)
	}
	if !upd.CreatedAt.Equal(p.CreatedAt) {
		t.Fatal("Update 不应改 CreatedAt")
	}
	// 停用 → 无下次触发
	off, err := s.Update(sdk.Schedule{ID: p.ID, Name: upd.Name, Cron: upd.Cron, Prompt: upd.Prompt, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if !off.NextRun.IsZero() {
		t.Fatalf("停用后不该有下次触发,得 %s", off.NextRun)
	}
	// 未知 ID
	if _, err := s.Update(sdk.Schedule{ID: "sched-none0000", Name: "x", Cron: "* * * * *", Prompt: "y"}); err == nil {
		t.Fatal("未知 ID 的 Update 应报错")
	}

	// 落盘 → 重新载入(模拟重启)应保留
	s2 := New(&fakeLoop{}, slog.New(slog.DiscardHandler))
	s2.SetClock(clock.Now)
	if err := s2.launch(); err != nil {
		t.Fatal(err)
	}
	defer s2.Stop()
	got := s2.List()
	if len(got) != 1 || got[0].ID != p.ID || got[0].Name != "每周对账" || got[0].Enabled {
		t.Fatalf("重启后计划未保留: %+v", got)
	}

	// Remove
	if err := s2.Remove(p.ID); err != nil {
		t.Fatal(err)
	}
	if len(s2.List()) != 0 {
		t.Fatal("删除后列表应为空")
	}
	if err := s2.Remove(p.ID); err == nil {
		t.Fatal("重复删除应报错(不静默成功)")
	}
}

// TestUpdateKeepsRunRecordAndInvalidatesInFlight 在跑回合的状态回写不得覆盖新配置。
func TestUpdateKeepsRunRecordAndInvalidatesInFlight(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC))
	loop := &fakeLoop{started: make(chan loopCall, 4), release: make(chan struct{})}
	s := newTestScheduler(t, loop, clock)
	p, err := s.Add(sdk.Schedule{Name: "对账", Cron: "0 8 * * *", Prompt: "跑", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunNow(p.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-loop.started:
	case <-time.After(3 * time.Second):
		t.Fatal("回合未启动")
	}
	// 回合在跑时改配置(rev 递增)→ 旧结果回写必须失效
	if _, err := s.Update(sdk.Schedule{ID: p.ID, Name: "新名", Cron: "0 9 * * *", Prompt: "新描述", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	close(loop.release)
	waitFor(t, 3*time.Second, func() bool {
		for _, x := range s.List() {
			if x.ID == p.ID {
				return x.LastStatus == ""
			}
		}
		return false
	})
	upd, _ := findPlan(s, p.ID)
	if upd.Name != "新名" || upd.LastStatus != "" {
		t.Fatalf("在跑的旧回合不该覆盖新配置/写状态: %+v", upd)
	}
	if upd.Cron != "0 9 * * *" {
		t.Fatalf("新 cron 应生效: %+v", upd)
	}
}

// TestLaunchLoadsSchedules 载入排期:停用无 NextRun,坏 cron 保留但不排期。
func TestLaunchLoadsSchedules(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC))
	setupHome(t)
	mustSave(t, sdk.Schedule{ID: "sched-on000001", Name: "启用", Cron: "0 8 * * *", Prompt: "跑", Enabled: true})
	mustSave(t, sdk.Schedule{ID: "sched-off00001", Name: "停用", Cron: "0 8 * * *", Prompt: "跑"})
	mustSave(t, sdk.Schedule{ID: "sched-bad00001", Name: "坏 cron", Cron: "nope", Prompt: "跑", Enabled: true})

	s := New(&fakeLoop{}, slog.New(slog.DiscardHandler))
	s.SetClock(clock.Now)
	if err := s.launch(); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	plans := s.List()
	if len(plans) != 3 {
		t.Fatalf("三条都应列出(含坏 cron 的那条,便于用户改): %+v", plans)
	}
	byID := map[string]sdk.Schedule{}
	for _, p := range plans {
		byID[p.ID] = p
	}
	if byID["sched-on000001"].NextRun.IsZero() {
		t.Fatal("启用计划应有 NextRun")
	}
	if !byID["sched-off00001"].NextRun.IsZero() {
		t.Fatal("停用计划不该有 NextRun")
	}
	if !byID["sched-bad00001"].NextRun.IsZero() {
		t.Fatal("cron 非法的计划不该排期")
	}
	// 双 launch 显式失败(防双循环)
	if err := s.launch(); err == nil {
		t.Fatal("重复 launch 应报错")
	}
}

func TestTriggerUnattendedAndPersist(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 7, 59, 0, 0, time.UTC))
	loop := &fakeLoop{}
	s := newTestScheduler(t, loop, clock)
	done := make(chan sdk.ScheduleRunEvent, 4)
	s.SetNotify(func(ev sdk.ScheduleRunEvent) { done <- ev })
	p, err := s.Add(sdk.Schedule{Name: "对账", Cron: "0 8 * * *", Prompt: "生成昨日对账", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(time.Minute) // 07:59 → 08:00 到点
	s.signal()
	ev := awaitEvent(t, done)
	if ev.State != sdk.ScheduleRunOK {
		t.Fatalf("触发应成功,得 %s(%s)", ev.State, ev.Error)
	}
	if ev.ID != p.ID {
		t.Fatalf("事件 ID 应为 %s,得 %s", p.ID, ev.ID)
	}
	call := loop.last()
	if !strings.HasPrefix(call.input, "[计划:对账] ") || !strings.Contains(call.input, "生成昨日对账") {
		t.Fatalf("回合输入应带计划前缀: %q", call.input)
	}
	if !call.unattended {
		t.Fatal("定时任务回合必须带无人值守标记(否则 policy-guard 会弹确认)")
	}
	// 同一分钟再 signal 一次不得重复触发(排期已推进到次日)
	s.signal()
	time.Sleep(150 * time.Millisecond)
	if n := loop.count(); n != 1 {
		t.Fatalf("同一时刻只应触发一次,得 %d 次", n)
	}
	after, _ := findPlan(s, p.ID)
	if after.LastStatus != sdk.ScheduleRunOK || after.LastRunAt.IsZero() {
		t.Fatalf("应回写运行记录: %+v", after)
	}
	if !after.NextRun.Equal(time.Date(2026, 11, 15, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("下次触发应为次日 08:00,得 %s", after.NextRun)
	}
	// 运行记录落盘(重启后可见)
	plans, _, err := loadPlans()
	if err != nil || len(plans) != 1 || plans[0].LastStatus != sdk.ScheduleRunOK {
		t.Fatalf("运行记录应落盘: %+v %v", plans, err)
	}
}

func TestTriggerFailureRecorded(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC))
	loop := &fakeLoop{err: errString("模型调用失败")}
	s := newTestScheduler(t, loop, clock)
	done := make(chan sdk.ScheduleRunEvent, 4)
	s.SetNotify(func(ev sdk.ScheduleRunEvent) { done <- ev })
	p, err := s.Add(sdk.Schedule{Name: "对账", Cron: "* * * * *", Prompt: "跑", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunNow(p.ID); err != nil {
		t.Fatal(err)
	}
	ev := awaitEvent(t, done)
	if ev.State != sdk.ScheduleRunFailed || !strings.Contains(ev.Error, "模型调用失败") {
		t.Fatalf("失败应记 failed + 原因,得 %+v", ev)
	}
	after, _ := findPlan(s, p.ID)
	if after.LastStatus != sdk.ScheduleRunFailed || !strings.Contains(after.LastError, "模型调用失败") {
		t.Fatalf("失败状态应回写: %+v", after)
	}
}

// TestTriggerSkippedWhenBusy 有回合在跑且超过等待上限 → 本轮跳过(不排队、不叠加)。
func TestTriggerSkippedWhenBusy(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC))
	loop := &fakeLoop{}
	s := newTestScheduler(t, loop, clock)
	tc := &fakeTC{running: true}
	s.SetTurnControl(tc)
	s.SetBusyWait(0)
	done := make(chan sdk.ScheduleRunEvent, 4)
	s.SetNotify(func(ev sdk.ScheduleRunEvent) { done <- ev })
	p, err := s.Add(sdk.Schedule{Name: "对账", Cron: "* * * * *", Prompt: "跑", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunNow(p.ID); err != nil {
		t.Fatal(err)
	}
	ev := awaitEvent(t, done)
	if ev.State != sdk.ScheduleRunSkipped || !strings.Contains(ev.Error, "跳过") {
		t.Fatalf("忙时应跳过并说明原因,得 %+v", ev)
	}
	if loop.count() != 0 {
		t.Fatal("跳过时不得触碰 agentLoop")
	}
	after, _ := findPlan(s, p.ID)
	if after.LastStatus != sdk.ScheduleRunSkipped {
		t.Fatalf("应记录 skipped: %+v", after)
	}
	if after.NextRun.IsZero() {
		t.Fatal("跳过不应让计划停止排期")
	}
	// 空闲后恢复:等空闲路径生效(忙等待轮询间隔内切闲)
	tc.set(false)
	if err := s.RunNow(p.ID); err != nil {
		t.Fatal(err)
	}
	ev2 := awaitEvent(t, done)
	if ev2.State != sdk.ScheduleRunOK {
		t.Fatalf("空闲后应正常执行,得 %+v", ev2)
	}
}

func TestRunNowErrors(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)
	if err := s.RunNow("sched-nope0000"); err == nil {
		t.Fatal("未知计划应报错")
	}
	p, err := s.Add(sdk.Schedule{Name: "停用的", Cron: "* * * * *", Prompt: "跑", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunNow(p.ID); err == nil || !strings.Contains(err.Error(), "先启用") {
		t.Fatalf("停用计划应拒绝立即运行,得 %v", err)
	}
}

// TestStopReleasesEverything 卸载即撤销:循环退出、取消在跑回合、拒绝新操作、无 goroutine 泄漏。
func TestStopReleasesEverything(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC))
	before := runtime.NumGoroutine()
	loop := &fakeLoop{started: make(chan loopCall, 4)}
	setupHome(t)
	s := New(loop, slog.New(slog.DiscardHandler))
	s.SetClock(clock.Now)
	if err := s.launch(); err != nil {
		t.Fatal(err)
	}
	p, err := s.Add(sdk.Schedule{Name: "对账", Cron: "0 8 * * *", Prompt: "跑", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	s.Stop()
	s.Stop() // 幂等
	select {
	case <-s.done:
	default:
		t.Fatal("Stop 返回后循环必须已退出")
	}
	if _, err := s.Add(sdk.Schedule{Name: "x", Cron: "* * * * *", Prompt: "y"}); err == nil {
		t.Fatal("卸载后应拒绝新增")
	}
	if err := s.RunNow(p.ID); err == nil {
		t.Fatal("卸载后应拒绝立即运行")
	}
	if s.Remove(p.ID) == nil {
		t.Fatal("卸载后应拒绝删除")
	}
	if err := s.launch(); err == nil {
		t.Fatal("卸载后不应能重新 launch")
	}
	// 到点也不得再触发(循环已退出)
	clock.Advance(24 * time.Hour)
	s.signal()
	time.Sleep(100 * time.Millisecond)
	if n := loop.count(); n != 0 {
		t.Fatalf("卸载后不得再触发起任务,得 %d 次", n)
	}
	// goroutine 不增长(循环已退出 + 无残留定时器)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runtime.NumGoroutine() > before+2 {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Fatalf("疑似 goroutine 泄漏:before=%d after=%d", before, after)
	}
}

func TestParseAddArgs(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		cron       string
		prompt     string
		wantErrSub string
	}{
		{name: "cron 一个参数", args: []string{"0 8 * * *", "生成昨日对账"}, cron: "0 8 * * *", prompt: "生成昨日对账"},
		{name: "cron 被空格拆散", args: []string{"0", "8", "*", "*", "*", "生成昨日对账"}, cron: "0 8 * * *", prompt: "生成昨日对账"},
		{name: "描述含空格", args: []string{"0", "8", "*", "*", "*", "生成", "昨日", "对账"}, cron: "0 8 * * *", prompt: "生成 昨日 对账"},
		{name: "缺描述", args: []string{"0 8 * * *"}, wantErrSub: "缺少任务描述"},
		{name: "字段不足", args: []string{"0", "8", "*"}, wantErrSub: "5 个字段"},
		{name: "cron 非法", args: []string{"0", "8", "*", "*", "9", "跑"}, wantErrSub: "cron 表达式非法"},
		{name: "无参数", args: nil, wantErrSub: "缺少 cron"},
	}
	for _, tc := range cases {
		cron, prompt, err := parseAddArgs(tc.args)
		if tc.wantErrSub != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("%s:应报含 %q 的错误,得 %v", tc.name, tc.wantErrSub, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if cron != tc.cron || prompt != tc.prompt {
			t.Fatalf("%s:得 (%q,%q),期望 (%q,%q)", tc.name, cron, prompt, tc.cron, tc.prompt)
		}
	}
}

func TestScheduleCommand(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)
	run := scheduleCommand(s).Run

	out, err := run(nil) // 空参数 = 列表
	if err != nil || !strings.Contains(out, "还没有定时计划") {
		t.Fatalf("空列表提示: %q %v", out, err)
	}
	out, err = run([]string{"add", "0", "8", "*", "*", "*", "生成昨日对账"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已创建计划") || !strings.Contains(out, "每天 08:00") {
		t.Fatalf("add 回显应含 ID 与人话: %q", out)
	}
	plans := s.List()
	if len(plans) != 1 {
		t.Fatalf("应有 1 条计划: %+v", plans)
	}
	id := plans[0].ID
	if plans[0].Name != "生成昨日对账" {
		t.Fatalf("名称应自动取描述: %q", plans[0].Name)
	}

	out, err = run([]string{"list"})
	if err != nil || !strings.Contains(out, id) || !strings.Contains(out, "启用") {
		t.Fatalf("list 应含该计划: %q %v", out, err)
	}
	if out, err = run([]string{"off", id}); err != nil || !strings.Contains(out, "已停用") {
		t.Fatalf("off: %q %v", out, err)
	}
	if out, err = run([]string{"on", id}); err != nil || !strings.Contains(out, "已启用") {
		t.Fatalf("on: %q %v", out, err)
	}
	if out, err = run([]string{"run", id}); err != nil || !strings.Contains(out, "已触发") {
		t.Fatalf("run: %q %v", out, err)
	}
	if out, err = run([]string{"rm", id}); err != nil || !strings.Contains(out, "已删除") {
		t.Fatalf("rm: %q %v", out, err)
	}
	if len(s.List()) != 0 {
		t.Fatal("删除后应为空")
	}
	// 错误路径
	for _, args := range [][]string{{"bogus"}, {"rm"}, {"on", "sched-nope0000"}, {"run", "sched-nope0000"}} {
		if _, err := run(args); err == nil {
			t.Fatalf("%v 应报错", args)
		}
	}
}

// —— 小工具 ——

type errString string

func (e errString) Error() string { return string(e) }

func mustSave(t *testing.T, p sdk.Schedule) {
	t.Helper()
	if err := savePlan(p); err != nil {
		t.Fatal(err)
	}
}

// waitFor 轮询等待条件成立(测试里唯一允许的等待:有上限,失败即报错)。
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时(%s)", d)
}

// awaitEvent 等一次触发终态(上限 3s,失败即报错 —— 不用固定 sleep 掩盖竞态)。
func awaitEvent(t *testing.T, ch <-chan sdk.ScheduleRunEvent) sdk.ScheduleRunEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("等待触发终态超时")
		return sdk.ScheduleRunEvent{}
	}
}

// TestStopWithoutLaunchNoDeadlock Stop 必须能在 launch 从未成功时安全返回
// (否则一次失败的启动会把卸载流程永久挂住)。
func TestStopWithoutLaunchNoDeadlock(t *testing.T) {
	setupHome(t)
	s := New(&fakeLoop{}, slog.New(slog.DiscardHandler))
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Stop()
		s.Stop() // 幂等
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop 在未启动时必须直接返回,不得死等 done")
	}
}
