// Package hostnotices 提供 host-notices 插件:ctx.notices 面向用户的提示通道(NOND-N1)。
//
// 职责(纯数据 + 广播,**不画端**):把「需要人回来的时刻」变成一条跨端提示 ——
// 无人值守任务失败/跳过、后台任务终态、回合报错,以及任何插件经 ctx.notices 自述的事件。
// 各端(Web toast / TUI 状态行 / 桌面壳系统通知)自行决定呈现强度。
//
// 三条设计纪律:
//  1. **单一事件源**:提示一经 Publish 即 Emit `sdk.EventNotice`(载荷 *sdk.Notice),
//     端只订阅事件;REST 回填走 List(sinceID) —— 不给任何端开第二条推送通道。
//  2. **不落盘、不进会话记录**:提示是瞬时信号(进程内环形缓冲),与「模型可见即已记录」无关;
//     重连/刷新靠 List 回填,缓冲丢弃必须显式记账(Gap),不假装完整。
//  3. **不让发布点重复造轮子**:自动生产(后台任务/定时计划/回合错误)集中在本插件,
//     新增无人值守场景只需在这里加一条订阅,不必在每端各加一个轮询器。
package hostnotices

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// bufferCap 提示环形缓冲容量(仅用于**回填**:实时路径不受影响)。
// 提示是低频信号(无人值守失败/终止),200 条足够覆盖「页面开着但人离开」的窗口。
const bufferCap = 200

// Plugin 实现 host-notices。
type Plugin struct{}

// Name 插件名。
func (p *Plugin) Name() string { return "host-notices" }

// Start 提供 ctx.notices 并订阅自动生产来源。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	svc := newService(bufferCap, func(n sdk.Notice) {
		c.Emit(context.Background(), sdk.EventNotice, &n, sdk.Emit)
	})
	if err := c.Provide("ctx.notices", svc); err != nil {
		return nil, err
	}
	// 自动生产来源(可选注入:没有这些服务时对应来源自然缺席,不影响提示通道本身):
	//   ctx.jobs     后台任务终态(取失败原因;未装配时只报任务 id)
	//   ctx.schedule 定时计划名(失败提示里用计划名而不是裸 id)
	var jobs sdk.JobService
	if err := c.Inject("ctx.jobs", &jobs); err != nil {
		jobs = nil
	}
	var sched sdk.ScheduleService
	if err := c.Inject("ctx.schedule", &sched); err != nil {
		sched = nil
	}

	ds := []sdk.Disposer{
		c.Subscribe(sdk.EventJobDone, func(_ context.Context, ev *sdk.Event) error {
			if e, ok := payloadJobDone(ev.Payload); ok {
				svc.NotifyJobDone(e, jobs)
			}
			return nil
		}),
		c.Subscribe(sdk.EventScheduleRun, func(_ context.Context, ev *sdk.Event) error {
			if e, ok := payloadScheduleRun(ev.Payload); ok {
				svc.NotifyScheduleRun(e, sched)
			}
			return nil
		}),
		c.Subscribe(sdk.EventAgentError, func(_ context.Context, ev *sdk.Event) error {
			svc.NotifyAgentError(errTextOf(ev.Payload))
			return nil
		}),
	}
	return func() {
		for _, d := range ds {
			d()
		}
	}, nil
}

// Service 实现 sdk.NoticeService:环形缓冲 + 广播(纯逻辑,可脱离 Ctx 测试)。
type Service struct {
	mu         sync.Mutex
	next       uint64               // 已分配的最后一个 ID
	droppedMax uint64               // 已丢弃的最大 ID(0 = 从未丢过;List 据此判 Gap)
	suppressed uint64               // 被去重丢弃的条数(可见可解释,不静默)
	keySeen    map[string]time.Time // Key → 上次发布时间(去重窗口)
	buf        []sdk.Notice         // ID 升序;超出 cap 丢最老(记 Gap)
	cap        int                  // 环形缓冲容量
	emit       func(sdk.Notice)     // 广播回调(装配时注入;nil = 只入缓冲,测试用)
	now        func() time.Time     // 时钟(测试可注入)
}

// newService 构造服务(cap<=0 → 默认容量)。
func newService(cap int, emit func(sdk.Notice)) *Service {
	if cap <= 0 {
		cap = bufferCap
	}
	return &Service{cap: cap, emit: emit, keySeen: map[string]time.Time{}, now: time.Now}
}

// Publish 发布一条提示(实现 sdk.NoticeService)。ID/TS/级别/字段裁剪由这里统一补齐。
// 返回 0 = 该条被 Key 去重丢弃(窗口内同一件事已提示过)。
func (s *Service) Publish(n sdk.Notice) uint64 {
	n = n.Normalize()
	now := s.now()
	s.mu.Lock()
	if n.Key != "" {
		if last, ok := s.keySeen[n.Key]; ok && now.Sub(last) < sdk.NoticeDedupeWindow {
			s.suppressed++
			s.mu.Unlock()
			// 去重不是丢数据:同一件事已经提示过(详情见缓冲/日志)。计数经 List().Suppressed 可见。
			slog.Debug("host-notices: 同一 Key 在窗口内重复提示,已去重",
				"key", n.Key, "suppressed_total", s.suppressed)
			return 0
		}
		s.keySeen[n.Key] = now
	}
	s.next++
	n.ID = s.next
	if n.TS.IsZero() {
		n.TS = now
	}
	if len(s.buf) >= s.cap {
		// 有界缓冲:丢最老一条。丢弃必须**显式记账**(否则回填端会把「缺一段」当「没有」)。
		dropped := s.buf[0]
		s.buf = append(s.buf[:0], s.buf[1:]...)
		if dropped.ID > s.droppedMax {
			s.droppedMax = dropped.ID
		}
		slog.Warn("host-notices: 提示缓冲已满,丢弃最老一条(回填将标记 gap)",
			"dropped_id", dropped.ID, "cap", s.cap)
	}
	s.buf = append(s.buf, n)
	s.mu.Unlock()
	if s.emit != nil {
		s.emit(n) // 广播在锁外:订阅回调不得持本锁(与 List 无死锁)
	}
	return n.ID
}

// List 取 ID 严格大于 sinceID 的提示(实现 sdk.NoticeService)。
// Gap=true 表示「sinceID 之后确有提示被丢弃」—— 回填不完整必须如实告知,不让调用方把
// 「缺一段」读成「没有」(宁可说不全,不可说没说)。
func (s *Service) List(sinceID uint64) sdk.NoticePage {
	s.mu.Lock()
	defer s.mu.Unlock()
	page := sdk.NoticePage{MaxID: s.next, Gap: s.droppedMax > sinceID, Suppressed: s.suppressed}
	for _, n := range s.buf {
		if n.ID > sinceID {
			page.Items = append(page.Items, n)
		}
	}
	if page.Items == nil {
		page.Items = []sdk.Notice{} // 空数组 ≠ null:前端不为两种空态写两条分支
	}
	return page
}

// NotifyJobDone 后台任务终态 → 提示(自动生产:只有非 ok 才是「需要人回来」的信号,
// 但成功完成同样值得通知 —— 无人值守跑完的长任务正是用户等待的对象)。
func (s *Service) NotifyJobDone(e *sdk.JobDoneEvent, jobs sdk.JobService) {
	if e == nil {
		return
	}
	level := sdk.NoticeInfo
	title := "后台任务已完成"
	switch e.State {
	case sdk.JobFailed:
		level, title = sdk.NoticeError, "后台任务失败"
	case sdk.JobKilled:
		level, title = sdk.NoticeWarn, "后台任务已终止"
	case sdk.JobRunning, "":
		// 非终态不该走 job/done(host-jobs 只在终态发);防御性忽略,不造假提示。
		return
	}
	body := "任务 " + e.ID
	if jobs != nil {
		if j, ok := jobs.Output(e.ID); ok {
			if j.Error != "" {
				body = "任务 " + e.ID + ":" + firstLine(j.Error)
			} else if j.Command != "" {
				body = "任务 " + e.ID + ":" + firstLine(j.Command)
			}
		}
	}
	s.Publish(sdk.Notice{Level: level, Title: title, Body: body, Source: "host-jobs", Key: "job:" + e.ID})
}

// NotifyScheduleRun 定时计划终态 → 提示(只报需要人介入的两态:failed / skipped;
// ok 每次成功都通知会变成每日噪音 —— 成功不代表需要人回来)。
func (s *Service) NotifyScheduleRun(e *sdk.ScheduleRunEvent, sched sdk.ScheduleService) {
	if e == nil {
		return
	}
	name := e.ID
	if sched != nil {
		for _, p := range sched.List() {
			if p.ID == e.ID && p.Name != "" {
				name = p.Name
				break
			}
		}
	}
	switch e.State {
	case sdk.ScheduleRunFailed:
		body := e.Error
		if body == "" {
			body = "详情见会话记录"
		}
		s.Publish(sdk.Notice{Level: sdk.NoticeError, Title: "计划「" + name + "」执行失败", Body: body,
			Source: "host-schedule", Key: "schedule:" + e.ID + ":failed"})
	case sdk.ScheduleRunSkipped:
		s.Publish(sdk.Notice{Level: sdk.NoticeWarn, Title: "计划「" + name + "」本轮跳过",
			Body:   "到点时有回合在跑(未排队,可调 busy_wait_seconds 或错开时间)",
			Source: "host-schedule", Key: "schedule:" + e.ID + ":skipped"})
	}
}

// NotifyAgentError 回合出错 → 提示(错误发生时人常常已经离开去干别的了;
// 空文本不发,免得推一条什么都说明不了的提示)。
// Key 取错误首行:同一种错误在去重窗口内只提示一次(重试风暴不得刷屏)。
func (s *Service) NotifyAgentError(msg string) {
	first := firstLine(msg)
	if first == "" {
		return
	}
	s.Publish(sdk.Notice{Level: sdk.NoticeError, Title: "回合出错", Body: msg,
		Source: "host-agent-loop", Key: "agent/error:" + clipRunes(first, 80)})
}

// clipRunes 去重键用的 rune 截断(无省略号:Key 只用于比较,不上屏)。
func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// payloadJobDone 提取 job/done 载荷(值/指针两种形态都接受;事件总线不保证形态)。
func payloadJobDone(p any) (*sdk.JobDoneEvent, bool) {
	switch v := p.(type) {
	case *sdk.JobDoneEvent:
		return v, true
	case sdk.JobDoneEvent:
		return &v, true
	}
	return nil, false
}

// payloadScheduleRun 提取 schedule/run 载荷(同上)。
func payloadScheduleRun(p any) (*sdk.ScheduleRunEvent, bool) {
	switch v := p.(type) {
	case *sdk.ScheduleRunEvent:
		return v, true
	case sdk.ScheduleRunEvent:
		return &v, true
	}
	return nil, false
}

// errTextOf 提取 agent/error 载荷的错误文本(支持 error / *sdk.LLMError / string)。
func errTextOf(p any) string {
	switch v := p.(type) {
	case error:
		return v.Error()
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(p)
	}
}

// firstLine 取首行(错误原文常是多行;提示里只放第一行,细节去日志/会话记录)。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
