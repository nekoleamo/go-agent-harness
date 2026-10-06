// 农历年度计划的生命周期测试:它与一次性的**关键区别**是「明年还要再跑」——
// 一次性跑完就停用,农历跑完要自己排到明年;这条错了用户就会以为「中秋提醒只响了一次」。
package hostschedule

import (
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 中秋(农历八月十五)在 2026 是 9 月 25 日(该值已跨源验证,见 lunar_test.go)。
func TestLunarPlanSchedules(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)

	p, err := s.Add(sdk.Schedule{Name: "中秋提醒", Cron: "0 20 * * *", Prompt: "给家里打电话",
		Enabled: true, LunarDate: "08-15"})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 25, 20, 0, 0, 0, time.UTC)
	if !p.NextRun.Equal(want) {
		t.Fatalf("中秋 2026 应排在 %s,得 %s", want, p.NextRun)
	}
	if len(p.NextRuns) != 3 {
		t.Fatalf("应给出未来三次预览,得 %d 次", len(p.NextRuns))
	}
	// 三次必须是三个不同年份(农历节日一年一次)
	years := map[int]bool{}
	for _, r := range p.NextRuns {
		if years[r.Year()] {
			t.Fatalf("预览出现重复年份: %v", p.NextRuns)
		}
		years[r.Year()] = true
	}
	if got, want := scheduleLabel(p), "每年中秋节(农历八月十五)20:00"; got != want {
		t.Fatalf("标签得 %q,期望 %q", got, want)
	}
}

// 触发后必须**排到明年**(不是停用),且那一年的日期由农历重新算 —— 2027 年中秋是 9/15。
func TestLunarPlanRepeatsNextYear(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 9, 25, 19, 59, 0, 0, time.UTC))
	loop := &fakeLoop{started: make(chan loopCall, 1)}
	s := newTestScheduler(t, loop, clock)
	p, err := s.Add(sdk.Schedule{Name: "中秋", Cron: "0 20 * * *", Prompt: "打电话",
		Enabled: true, LunarDate: "08-15"})
	if err != nil {
		t.Fatal(err)
	}
	advanceAndWake(clock, s, 2*time.Minute)
	waitCall(t, loop)
	after := waitIdle(t, s, p.ID)

	if !after.Enabled {
		t.Fatal("农历年度计划触发后**不该**停用(它不是一次性)")
	}
	wantNext := time.Date(2027, 9, 15, 20, 0, 0, 0, time.UTC)
	if !after.NextRun.Equal(wantNext) {
		t.Fatalf("触发后应排到 2027 年中秋 %s,得 %s", wantNext, after.NextRun)
	}
	// 明年再跑一次:推进到 2027-09-15
	advanceAndWake(clock, s, 355*24*time.Hour)
	waitCall(t, loop)
	if after2 := waitIdle(t, s, p.ID); after2.LastStatus != sdk.ScheduleRunOK {
		t.Fatalf("第二次触发应成功,得 %+v", after2)
	}
}

// 除夕用 "12-0" 表示「腊月最后一天」—— 腊月有时 29 天有时 30 天,写死会算不出来。
func TestLunarPlanNewYearEve(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)
	p, err := s.Add(sdk.Schedule{Name: "除夕", Cron: "0 18 * * *", Prompt: "年夜饭",
		Enabled: true, LunarDate: "12-0"})
	if err != nil {
		t.Fatal(err)
	}
	// 2026 年春节是 2/17 → 除夕 2/16
	want := time.Date(2026, 2, 16, 18, 0, 0, 0, time.UTC)
	if !p.NextRun.Equal(want) {
		t.Fatalf("除夕应排在 %s,得 %s", want, p.NextRun)
	}
	if got := scheduleLabel(p); !strings.Contains(got, "除夕") || !strings.Contains(got, "最后一天") {
		t.Fatalf("除夕标签应说清是腊月最后一天,得 %q", got)
	}
}

// 校验:农历与 cron 的日/月/周不能同时限定(否则文件有两种读法)。
func TestLunarPlanValidation(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)
	cases := []struct {
		name string
		in   sdk.Schedule
		want string
	}{
		{"cron 限定了日", sdk.Schedule{Name: "n", Cron: "0 20 15 * *", Prompt: "x", LunarDate: "08-15"}, "只能写时分"},
		{"cron 限定了月", sdk.Schedule{Name: "n", Cron: "0 20 * 8 *", Prompt: "x", LunarDate: "08-15"}, "只能写时分"},
		{"cron 限定了周", sdk.Schedule{Name: "n", Cron: "0 20 * * 1", Prompt: "x", LunarDate: "08-15"}, "只能写时分"},
		{"格式不对", sdk.Schedule{Name: "n", Cron: "0 20 * * *", Prompt: "x", LunarDate: "八月十五"}, "格式"},
		{"月份越界", sdk.Schedule{Name: "n", Cron: "0 20 * * *", Prompt: "x", LunarDate: "13-01"}, "月份"},
		{"农历没有 31 日", sdk.Schedule{Name: "n", Cron: "0 20 * * *", Prompt: "x", LunarDate: "08-31"}, "最多 30 天"},
		{"与一次性冲突", sdk.Schedule{Name: "n", Cron: "0 20 * * *", Prompt: "x", LunarDate: "08-15",
			Once: true, OnceDate: "2026-09-25"}, "不能同时"},
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
}

// 公历年度排期(每年某天)走 cron,不受农历改动影响 —— 这条守「新增分类没弄坏既有排期」。
func TestSolarAnnualPlanStillSchedules(t *testing.T) {
	clock := newFakeClock(time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC))
	s := newTestScheduler(t, &fakeLoop{}, clock)
	p, err := s.Add(sdk.Schedule{Name: "国庆", Cron: "0 9 1 10 *", Prompt: "发祝福", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2027, 10, 1, 9, 0, 0, 0, time.UTC)
	if !p.NextRun.Equal(want) {
		t.Fatalf("每年 10 月 1 日应排到 %s,得 %s", want, p.NextRun)
	}
	if got, want := scheduleLabel(p), "每年 10 月 1 日 09:00"; got != want {
		t.Fatalf("标签得 %q,期望 %q", got, want)
	}
}
