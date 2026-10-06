// 一次性计划(once)触发链路单测 —— 本文件是 B 段的**红线清单**。
//
// 一次性的全部风险集中在触发链路:5 字段 cron 没有年字段,`0 9 20 11 *` 明年还会命中;
// 而「什么时候算跑完」只有两种记法(Enabled=false 与 LastRunAt),多写一个 OnceDone
// 就多一处可能不一致。所以每条用例都对应一个「如果这里错了会怎样」。
package hostschedule

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 小工具 ——

// advanceAndWake 推进假时钟并**显式唤醒调度循环**。
//
// 为什么必须唤醒:假时钟推进不会让真实的 time.Timer 到期 —— 循环还睡在「距下次触发 1 分钟」
// 上。不唤醒的话测试只能靠「循环还没来得及算 snapshot」这个竞态撞对,于是
// `go test -race`(慢)下必然超时。唤醒后 snapshot 会重新算,发现的计划立刻触发。
func advanceAndWake(clock *fakeClock, s *Scheduler, d time.Duration) {
	clock.Advance(d)
	s.signal()
}

// waitCall 等一次触发落到 fakeLoop(触发是异步的,不等会读到旧状态)。
func waitCall(t *testing.T, loop *fakeLoop) loopCall {
	t.Helper()
	select {
	case c := <-loop.started:
		return c
	case <-time.After(3 * time.Second):
		t.Fatal("等待触发超时")
		return loopCall{}
	}
}

// findByID 从服务层列表里取一条计划。
func findByID(t *testing.T, s *Scheduler, id string) sdk.Schedule {
	t.Helper()
	for _, p := range s.List() {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("计划不存在: %s", id)
	return sdk.Schedule{}
}

// waitIdle 等一次触发的**收尾**(已触发 + 不再跑 + 状态已落盘)。
// waitCall 只保证 loop.Run 已被调用,那之后还有状态回写与落盘 —— 不等就会读到旧快照,
// 症状是「明明该自动停用了却还是启用」这类假失败。
//
// 预算说明:这条要连等四步(触发 → 执行 → 回写内存 → 落盘)。原先写死 3s,本地 0.3s 就过,
// 但 test-windows 带 -race 且几十个包并行时不够 —— run 37442904845 就是在这里红的
// (同一份代码在 v0.5.4 的 Windows job 是绿的,本地连跑 6 次也全绿 ⇒ 负载型 flake,不是逻辑问题)。
// 用同文件已有的 waitFor(测试里唯一允许的等待入口)而不是再开一个裸轮询循环。
func waitIdle(t *testing.T, s *Scheduler, id string) sdk.Schedule {
	t.Helper()
	const budget = 15 * time.Second
	waitFor(t, budget, func() bool {
		p := findByID(t, s, id)
		if p.LastRunAt.IsZero() || runningLocked(s, id) {
			return false
		}
		// 还要等落盘:execute 是「先回写内存、runting=false,再落盘」,
		// 只看内存会读到还没写下去的状态。
		disk := loadOneQuiet(id)
		return disk != nil && disk.Enabled == p.Enabled
	})
	return findByID(t, s, id)
}

// runningLocked 该计划此刻是否仍在跑(读运行态,不用重新 List 一遍)。
func runningLocked(s *Scheduler, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.plans[id]; ok {
		return e.running
	}
	return false
}

// loadOneQuiet 读盘里的计划;不存在或读失败返回 nil(等待中重试用,不 Fatal)。
func loadOneQuiet(id string) *sdk.Schedule {
	plans, _, err := loadPlans()
	if err != nil {
		return nil
	}
	for i := range plans {
		if plans[i].ID == id {
			return &plans[i]
		}
	}
	return nil
}

// restartScheduler 用同一个 GAH_HOME 重建调度器(模拟重启:数据在盘上,进程重来)。
func restartScheduler(t *testing.T, loop sdk.AgentLoop, clock *fakeClock, home string) *Scheduler {
	t.Helper()
	t.Setenv("GAH_HOME", home)
	s := New(loop, slog.New(slog.DiscardHandler))
	s.SetClock(clock.Now)
	if err := s.launch(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

// loadOne 直接从落盘文件读一条(验证状态确实写下去了,而不只是内存里有)。
func loadOne(t *testing.T, id string) sdk.Schedule {
	t.Helper()
	plans, _, err := loadPlans()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.ID == id {
			return p
		}
	}
	t.Fatalf("落盘文件里没有计划 %s", id)
	return sdk.Schedule{}
}

//  1. 一次性到点后**自动停用**:触发一轮 → Enabled=false、无下次触发。
//     若没停用,明年同一天会再跑一次 —— 而用户只要求跑一次。
func TestOnceFiresThenAutoDisables(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 8, 59, 0, 0, time.UTC))
	loop := &fakeLoop{started: make(chan loopCall, 1)}
	s := newTestScheduler(t, loop, clock)

	p, err := s.Add(sdk.Schedule{Name: "一次性提醒", Cron: "0 9 14 11 *", Prompt: "提醒我",
		Enabled: true, Once: true, OnceDate: "2026-11-14"})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC); !p.NextRun.Equal(want) {
		t.Fatalf("一次性计划的下次触发应是 %s,得 %s", want, p.NextRun)
	}

	advanceAndWake(clock, s, 2*time.Minute) // 到 09:01
	waitCall(t, loop)
	after := waitIdle(t, s, p.ID)
	if after.Enabled {
		t.Fatal("一次性跑完后应自动停用,否则明年会再跑一次")
	}
	if !after.NextRun.IsZero() {
		t.Fatalf("跑完后不该有下次触发,得 %s", after.NextRun)
	}
	if after.LastRunAt.IsZero() || after.LastStatus != sdk.ScheduleRunOK {
		t.Fatalf("应记运行结果,得 %+v", after)
	}
	// 状态必须落盘(否则重启后会当成没跑过)
	onDisk := loadOne(t, p.ID)
	if onDisk.Enabled {
		t.Fatal("停用状态应已落盘")
	}
}

//  2. once 绝不能靠 cron 排期:它的 cron 里带着「每年」的语义
//     (`0 9 14 11 *` 每年 11/14 都命中)。若 next 走了 spec.next,
//     跑完后 next 会指向明年 —— 正是这个坑。
func TestOnceDoesNotFallBackToCronSearch(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 8, 59, 0, 0, time.UTC))
	loop := &fakeLoop{started: make(chan loopCall, 1)}
	s := newTestScheduler(t, loop, clock)

	p, err := s.Add(sdk.Schedule{Name: "只这一次", Cron: "0 9 14 11 *", Prompt: "跑",
		Enabled: true, Once: true, OnceDate: "2026-11-14"})
	if err != nil {
		t.Fatal(err)
	}
	advanceAndWake(clock, s, 2*time.Minute)
	waitCall(t, loop)
	waitIdle(t, s, p.ID)

	// 推进一年,不该有任何新触发
	clock.Advance(365 * 24 * time.Hour)
	if calls := loop.count(); calls != 1 {
		t.Fatalf("一次性计划一年后不应再触发,实得 %d 次", calls)
	}
	if cur := findByID(t, s, p.ID); cur.Enabled || !cur.NextRun.IsZero() {
		t.Fatalf("跑完后应保持停用且无下次触发,得 %+v", cur)
	}
}

//  3. 重启不得重跑:启动时目标时刻已过 → 记 skipped 并**不再排期**。
//     否则每次启动都会看到一个永久到点的计划,循环空转。
func TestOnceExpiredAtLaunchNotRescheduled(t *testing.T) {
	base := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
	clock := newFakeClock(base)
	s := newTestScheduler(t, &fakeLoop{}, clock)
	p, err := s.Add(sdk.Schedule{Name: "会过期的一次性", Cron: "0 9 14 11 *", Prompt: "跑",
		Enabled: true, Once: true, OnceDate: "2026-11-14"})
	if err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("GAH_HOME")
	s.Stop()

	// 重启:同一个数据根 + 时钟已过目标时刻
	clock2 := newFakeClock(base.Add(3 * time.Hour))
	loop2 := &fakeLoop{started: make(chan loopCall, 1)}
	s2 := restartScheduler(t, loop2, clock2, home)

	cur := findByID(t, s2, p.ID)
	if !cur.NextRun.IsZero() {
		t.Fatalf("过期的一次性计划不该再排期,得 %s", cur.NextRun)
	}
	if cur.LastStatus != sdk.ScheduleRunSkipped {
		t.Fatalf("过期应记 skipped,得 %q", cur.LastStatus)
	}
	if cur.LastError == "" {
		t.Fatal("应说明为什么没跑(当时 gah 未运行),不能只留一个空状态")
	}
	// 不该真的触发
	if calls := loop2.count(); calls != 0 {
		t.Fatalf("过期计划不该触发,实得 %d 次", calls)
	}
}

//  4. RunNow 消耗唯一性:手动跑一次也算「那一次」用掉了,不该再自动跑。
//     否则用户点了「立即执行」,到点还会再跑一遍。
func TestOnceRunNowConsumesTheOnce(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC))
	loop := &fakeLoop{started: make(chan loopCall, 1)}
	s := newTestScheduler(t, loop, clock)
	p, err := s.Add(sdk.Schedule{Name: "手动跑一次", Cron: "0 9 14 11 *", Prompt: "跑",
		Enabled: true, Once: true, OnceDate: "2026-11-14"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunNow(p.ID); err != nil {
		t.Fatal(err)
	}
	waitCall(t, loop)
	after := waitIdle(t, s, p.ID)
	if after.Enabled {
		t.Fatal("手动跑过之后应视为已完成(自动停用)")
	}
	clock.Advance(3 * time.Hour) // 越过原定 09:00
	if calls := loop.count(); calls != 1 {
		t.Fatalf("手动跑过之后不该再自动触发,实得 %d 次", calls)
	}
}

// 5. 改期后旧排期失效(走既有 rev 保护):改到明天 → 今天的排期不该还留着。
func TestOnceRescheduleDropsOldNext(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)
	p, err := s.Add(sdk.Schedule{Name: "改期", Cron: "0 9 14 11 *", Prompt: "跑",
		Enabled: true, Once: true, OnceDate: "2026-11-14"})
	if err != nil {
		t.Fatal(err)
	}
	upd, err := s.Update(sdk.Schedule{ID: p.ID, Name: p.Name, Cron: "0 9 16 11 *", Prompt: p.Prompt,
		Enabled: true, Once: true, OnceDate: "2026-11-16"})
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC); !upd.NextRun.Equal(want) {
		t.Fatalf("改期后下次触发应是 %s,得 %s", want, upd.NextRun)
	}
	if upd.LastRunAt.IsZero() == false {
		t.Fatal("没跑过就不该有 LastRunAt")
	}
}

// 6. 校验:一次性必须有合法未来日期。
func TestOnceValidation(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)
	cases := []struct {
		name string
		in   sdk.Schedule
		want string
	}{
		{"缺日期", sdk.Schedule{Name: "n", Cron: "0 9 14 11 *", Prompt: "跑", Once: true}, "日期"},
		{"日期非法", sdk.Schedule{Name: "n", Cron: "0 9 14 11 *", Prompt: "跑", Once: true, OnceDate: "下周三"}, "日期"},
		{"已过去", sdk.Schedule{Name: "n", Cron: "0 9 13 11 *", Prompt: "跑", Once: true, OnceDate: "2026-11-13"}, "已过"},
		{"今天但时刻已过", sdk.Schedule{Name: "n", Cron: "0 7 14 11 *", Prompt: "跑", Once: true, OnceDate: "2026-11-14"}, "已过"},
		{"太远", sdk.Schedule{Name: "n", Cron: "0 9 1 1 *", Prompt: "跑", Once: true, OnceDate: "2099-01-01"}, "太远"},
	}
	for _, tc := range cases {
		_, err := s.Add(tc.in)
		if err == nil {
			t.Fatalf("%s 应被拒", tc.name)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s:错误应含 %q,得 %q", tc.name, tc.want, err.Error())
		}
	}
	// 今天但时刻未过 → 放行
	if _, err := s.Add(sdk.Schedule{Name: "ok", Cron: "0 9 14 11 *", Prompt: "跑",
		Once: true, OnceDate: "2026-11-14"}); err != nil {
		t.Fatalf("今天稍晚的时刻应放行: %v", err)
	}
	// 循环计划不受 once 校验影响
	if _, err := s.Add(sdk.Schedule{Name: "loop", Cron: "0 8 * * *", Prompt: "跑"}); err != nil {
		t.Fatalf("循环计划不该受 once 校验影响: %v", err)
	}
}

//  7. once 从循环改过来 / 循环从 once 改回来:落盘字段必须跟着变,
//     否则重启后语义相反(界面说只跑一次、实际年年跑)。
func TestOnceToggledByUpdate(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)
	p, err := s.Add(sdk.Schedule{Name: "改", Cron: "0 9 14 11 *", Prompt: "跑",
		Enabled: true, Once: true, OnceDate: "2026-11-14"})
	if err != nil {
		t.Fatal(err)
	}
	// 改成循环:cron 换成循环形态,Once 归零
	upd, err := s.Update(sdk.Schedule{ID: p.ID, Name: p.Name, Cron: "0 9 * * *", Prompt: p.Prompt, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Once || upd.OnceDate != "" {
		t.Fatalf("改成循环后 once 字段应清空,得 %+v", upd)
	}
	if want := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC); !upd.NextRun.Equal(want) {
		t.Fatalf("改成循环后应是每天 09:00(基准 08:00,今天 09:00 还没到),得 %s", upd.NextRun)
	}
}

// —— 落盘与向后兼容 ——

// 一次性计划必须原样落盘并读回;否则「重启后当循环计划年年跑」是静默的数据损坏。
func TestOnceStoreRoundTrip(t *testing.T) {
	home := setupHome(t)
	_ = home
	in := sdk.Schedule{
		ID: "sched-once0001", Name: "提醒", Cron: "0 9 20 11 *", Prompt: "跑",
		Enabled: true, CreatedAt: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC),
		Once: true, OnceDate: "2026-11-20",
	}
	if err := savePlan(in); err != nil {
		t.Fatal(err)
	}
	plans, warns, err := loadPlans()
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("不该有告警: %v", warns)
	}
	got := plans[0]
	if !got.Once || got.OnceDate != "2026-11-20" {
		t.Fatalf("once/once_date 应原样往返,得 %+v", got)
	}
}

// 老版本写下的计划文件没有 once 字段 —— 必须读成「循环计划」且**无告警**
// (静默迁移的前提是不打扰用户;反过来,若读成一次性会导致老计划只跑一次)。
func TestLegacyPlanFileHasNoOnceFields(t *testing.T) {
	home := setupHome(t)
	legacy := `# gah 定时计划(NOND-W4):由界面/` + "`/schedule`" + ` 命令维护;手改前请关闭 gah。
id: sched-legacy001
name: 每日对账
cron: 0 8 * * *
prompt: 生成昨日对账
enabled: true
created_at: 2026-11-14T07:00:00Z
`
	path := filepath.Join(home, "schedules", "sched-legacy001.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	plans, warns, err := loadPlans()
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Fatalf("老文件不该产生告警: %v", warns)
	}
	if len(plans) != 1 {
		t.Fatalf("应读出 1 条,得 %d", len(plans))
	}
	if plans[0].Once || plans[0].OnceDate != "" {
		t.Fatalf("缺字段应读成循环计划(once=false),得 %+v", plans[0])
	}
	// 重存一次后仍应是循环:savePlan 会补出 once: false,不能变成 true
	if err := savePlan(plans[0]); err != nil {
		t.Fatal(err)
	}
	back, _, err := loadPlans()
	if err != nil {
		t.Fatal(err)
	}
	if back[0].Once {
		t.Fatal("老计划重存后不该变成一次性")
	}
}

// /schedule add once <日期> <HH:MM> <描述>:CLI 与界面说的是同一件事。
func TestScheduleCommandAddOnce(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)

	out, err := scheduleCmd(s, []string{"add", "once", "2026-11-20", "09:00", "生成", "昨日对账"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2026-11-20") {
		t.Fatalf("回显应含目标日期,得 %q", out)
	}
	plans := s.List()
	if len(plans) != 1 || !plans[0].Once || plans[0].OnceDate != "2026-11-20" {
		t.Fatalf("应建出一条一次性计划,得 %+v", plans)
	}
	if plans[0].Cron != "0 9 20 11 *" {
		t.Fatalf("cron 应承载时分月日,得 %q", plans[0].Cron)
	}
	if want := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC); !plans[0].NextRun.Equal(want) {
		t.Fatalf("触发时刻应是 %s,得 %s", want, plans[0].NextRun)
	}
	// 列表里不暴露裸表达式,只说「仅执行一次」
	list := scheduleList(s)
	if strings.Contains(list, "cron ") {
		t.Fatalf("一次性计划不该在列表里显示 cron: %q", list)
	}
	if !strings.Contains(list, "仅执行一次") {
		t.Fatalf("列表应标明一次性,得 %q", list)
	}
	// 参数错误要说清用法
	for _, bad := range [][]string{
		{"add", "once"},
		{"add", "once", "下周", "09:00", "跑"},
		{"add", "once", "2026-11-20", "9点", "跑"},
		{"add", "once", "2026-11-20", "09:00"},
	} {
		if _, err := scheduleCmd(s, bad); err == nil {
			t.Fatalf("%v 应被拒", bad)
		}
	}
}
