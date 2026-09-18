package hostnotices

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// stubJobs 替身:只提供 Output(取失败原因)。
type stubJobs struct {
	job sdk.Job
	ok  bool
}

func (s stubJobs) Submit(string) (string, error)   { return "", nil }
func (s stubJobs) Run(sdk.JobFunc) (string, error) { return "", nil }
func (s stubJobs) List() []sdk.Job                 { return nil }
func (s stubJobs) Output(string) (sdk.Job, bool)   { return s.job, s.ok }
func (s stubJobs) Kill(string) error               { return nil }

// stubSched 替身:只提供 List(取计划名)。
type stubSched struct{ plans []sdk.Schedule }

func (s stubSched) List() []sdk.Schedule                        { return s.plans }
func (s stubSched) Add(p sdk.Schedule) (sdk.Schedule, error)    { return p, nil }
func (s stubSched) Update(p sdk.Schedule) (sdk.Schedule, error) { return p, nil }
func (s stubSched) Remove(string) error                         { return nil }
func (s stubSched) RunNow(string) error                         { return nil }

// TestPublishDedupeByKey 同 Key 在窗口内只留第一条(重试风暴不刷屏);
// 不同 Key / 窗口外照发;被去重的条数经 List().Suppressed 可查(不静默)。
func TestPublishDedupeByKey(t *testing.T) {
	svc := newService(10, nil)
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	now := base
	svc.now = func() time.Time { return now }

	id1 := svc.Publish(sdk.Notice{Title: "限流", Key: "agent/error:429"})
	id2 := svc.Publish(sdk.Notice{Title: "限流", Key: "agent/error:429"}) // 窗口内重复
	id3 := svc.Publish(sdk.Notice{Title: "超时", Key: "agent/error:timeout"})
	id4 := svc.Publish(sdk.Notice{Title: "无键提示"}) // Key 空 = 不去重

	if id1 == 0 || id2 != 0 || id3 == 0 || id4 == 0 {
		t.Fatalf("去重应只影响同 Key: %d %d %d %d", id1, id2, id3, id4)
	}
	if n := svc.List(0); len(n.Items) != 3 || n.Suppressed != 1 {
		t.Fatalf("缓冲与去重计数不符: %+v", n)
	}
	now = base.Add(sdk.NoticeDedupeWindow) // 窗口边界(恰好一条:不一定同一件事了)
	if id := svc.Publish(sdk.Notice{Title: "限流", Key: "agent/error:429"}); id == 0 {
		t.Fatal("超出去重窗口后应重新发布")
	}
	if n := svc.List(0); n.Suppressed != 1 || len(n.Items) != 4 {
		t.Fatalf("窗口外发布不应计作抑制: %+v", n)
	}
	// 同 Key 跨端可见性:被去重的条目不入缓冲(它本就不应该被看到两遍)
	for _, n := range svc.List(0).Items {
		if n.ID == id2 {
			t.Fatal("被去重的条目不得入缓冲")
		}
	}
}

// TestPublishAssignsIDAndTS 发布补齐 ID(单调)/TS/级别归一,并广播同一条。
func TestPublishAssignsIDAndTS(t *testing.T) {
	var got []sdk.Notice
	svc := newService(10, func(n sdk.Notice) { got = append(got, n) })
	id1 := svc.Publish(sdk.Notice{Title: "a", Level: "BOGUS"})
	id2 := svc.Publish(sdk.Notice{Title: "b"})
	if id1 != 1 || id2 != 2 {
		t.Fatalf("ID 应单调递增: %d %d", id1, id2)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("广播应带同一份补齐后的载荷: %+v", got)
	}
	if got[0].Level != sdk.NoticeInfo {
		t.Fatalf("非法级别应归一 info: %q", got[0].Level)
	}
	if got[0].TS.IsZero() {
		t.Fatal("TS 应由实现补齐")
	}
}

// TestListSinceAndGap List 只回 since 之后;缓冲丢弃必须显式标记 Gap(不假装完整)。
func TestListSinceAndGap(t *testing.T) {
	svc := newService(3, nil)
	for i := 0; i < 5; i++ { // 容量 3:1、2 被丢
		svc.Publish(sdk.Notice{Title: "n"})
	}
	all := svc.List(0)
	if len(all.Items) != 3 || all.Items[0].ID != 3 || all.MaxID != 5 {
		t.Fatalf("缓冲应保留最近 3 条且 MaxID=5: %+v", all)
	}
	if !all.Gap {
		t.Fatalf("从未看得 1、2 的新客户端(since=0):已有丢弃 → 必须如实标 gap: %+v", all)
	}
	if mid := svc.List(2); mid.Gap {
		t.Fatalf("客户端游标已到丢弃区末尾(2)→ 回填连续,不得标 gap: %+v", mid)
	}
	if tail := svc.List(4); tail.Gap || len(tail.Items) != 1 || tail.Items[0].ID != 5 {
		t.Fatalf("since=4 应只回 5 且无 gap: %+v", tail)
	}
	if empty := svc.List(5); empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("无新提示应回空数组(不是 null): %+v", empty)
	}
	fresh := newService(3, nil)
	fresh.Publish(sdk.Notice{Title: "n"})
	if p := fresh.List(0); p.Gap {
		t.Fatalf("从未发生丢弃时不得标 gap: %+v", p)
	}
}

// TestNotifyJobDone 后台任务终态 → 提示:失败/终止/完成给不同级别,失败带原因首行。
func TestNotifyJobDone(t *testing.T) {
	var got []sdk.Notice
	svc := newService(10, func(n sdk.Notice) { got = append(got, n) })
	jobs := stubJobs{job: sdk.Job{ID: "j1", State: sdk.JobFailed, Error: "exit status 1\n详情第二行"}, ok: true}

	svc.NotifyJobDone(&sdk.JobDoneEvent{ID: "j1", State: sdk.JobFailed}, jobs)
	svc.NotifyJobDone(&sdk.JobDoneEvent{ID: "j2", State: sdk.JobKilled}, nil)
	svc.NotifyJobDone(&sdk.JobDoneEvent{ID: "j3", State: sdk.JobDone}, nil)
	svc.NotifyJobDone(&sdk.JobDoneEvent{ID: "j4", State: sdk.JobRunning}, nil) // 非终态:防御性忽略
	svc.NotifyJobDone(nil, nil)

	if len(got) != 3 {
		t.Fatalf("应只生产 3 条(失败/终止/完成): %+v", got)
	}
	if got[0].Level != sdk.NoticeError || !strings.Contains(got[0].Title, "失败") {
		t.Fatalf("失败应为 error: %+v", got[0])
	}
	if !strings.Contains(got[0].Body, "exit status 1") || strings.Contains(got[0].Body, "第二行") {
		t.Fatalf("失败原因应取首行: %q", got[0].Body)
	}
	if got[1].Level != sdk.NoticeWarn || got[2].Level != sdk.NoticeInfo {
		t.Fatalf("终止应为 warn、完成应为 info: %+v", got)
	}
	if got[0].Source != "host-jobs" {
		t.Fatalf("来源应标 host-jobs: %q", got[0].Source)
	}
}

// TestNotifyScheduleRun 定时计划:failed/skipped 报(error/warn),ok 不报(避免每日噪音);
// 计划名经 ctx.schedule 解析,取不到时回落裸 id。
func TestNotifyScheduleRun(t *testing.T) {
	var got []sdk.Notice
	svc := newService(10, func(n sdk.Notice) { got = append(got, n) })
	sched := stubSched{plans: []sdk.Schedule{{ID: "p1", Name: "对账"}}}

	svc.NotifyScheduleRun(&sdk.ScheduleRunEvent{ID: "p1", State: sdk.ScheduleRunFailed, Error: "模型超时"}, sched)
	svc.NotifyScheduleRun(&sdk.ScheduleRunEvent{ID: "p1", State: sdk.ScheduleRunSkipped}, sched)
	svc.NotifyScheduleRun(&sdk.ScheduleRunEvent{ID: "p1", State: sdk.ScheduleRunOK}, sched)
	svc.NotifyScheduleRun(&sdk.ScheduleRunEvent{ID: "p9", State: sdk.ScheduleRunFailed}, nil) // 无 schedule 服务
	svc.NotifyScheduleRun(nil, nil)

	if len(got) != 3 {
		t.Fatalf("失败/跳过/无服务失败 = 3 条,ok 不发: %+v", got)
	}
	if !strings.Contains(got[0].Title, "对账") || got[0].Level != sdk.NoticeError || got[0].Body != "模型超时" {
		t.Fatalf("失败提示应带计划名与原因: %+v", got[0])
	}
	if got[1].Level != sdk.NoticeWarn || !strings.Contains(got[1].Title, "跳过") {
		t.Fatalf("跳过应为 warn: %+v", got[1])
	}
	if !strings.Contains(got[2].Title, "p9") {
		t.Fatalf("解析不到计划名应回落裸 id: %+v", got[2])
	}
	if got[0].Source != "host-schedule" {
		t.Fatalf("来源应标 host-schedule: %q", got[0].Source)
	}
}

// TestNotifyAgentError 回合出错:有文本才发(空文本不推什么都说明不了的提示);
// 同一种错误在去重窗口内只提示一次。
func TestNotifyAgentError(t *testing.T) {
	var got []sdk.Notice
	svc := newService(10, func(n sdk.Notice) { got = append(got, n) })
	svc.NotifyAgentError("HTTP 429: 限流")
	svc.NotifyAgentError("   \n  ")
	svc.NotifyAgentError("HTTP 429: 限流") // 同一种错误 → 去重
	svc.NotifyAgentError("context deadline exceeded")
	if len(got) != 2 || got[0].Level != sdk.NoticeError || got[0].Body != "HTTP 429: 限流" {
		t.Fatalf("空文本应跳过、同错误应去重: %+v", got)
	}
	if got[0].Key != "agent/error:HTTP 429: 限流" {
		t.Fatalf("去重键应含错误首行: %q", got[0].Key)
	}
}

// TestPayloadShapes 事件总线不保证值/指针形态:两种都要接住。
func TestPayloadShapes(t *testing.T) {
	if _, ok := payloadJobDone(sdk.JobDoneEvent{ID: "x", State: sdk.JobDone}); !ok {
		t.Fatal("值形态 job/done 未接住")
	}
	if _, ok := payloadScheduleRun(sdk.ScheduleRunEvent{ID: "x"}); !ok {
		t.Fatal("值形态 schedule/run 未接住")
	}
	if _, ok := payloadJobDone("nope"); ok {
		t.Fatal("非法载荷不应被当事件")
	}
	if got := errTextOf(nil); got != "" {
		t.Fatalf("nil 载荷应得空文本: %q", got)
	}
	if got := errTextOf(context.DeadlineExceeded); !strings.Contains(got, "deadline") {
		t.Fatalf("error 载荷应取 Error() 文本: %q", got)
	}
}
