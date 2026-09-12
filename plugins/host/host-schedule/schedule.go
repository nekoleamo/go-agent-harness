// Package hostschedule 提供 host-schedule 插件(NOND-W4):ctx.schedule 定时任务服务。
//
// 触发链路(**不新增执行路径**):cron 到点 → ctx.agentLoop.Run → 工具仍只经
// ctx.tools → policy-guard 路径/审批裁决 → 会话记录(不变量:模型可见即已记录)。
//
// 无人值守语义:定时任务没有在场的人回答确认弹窗,故触发时给 ctx 打
// sdk.WithUnattended 标记 → policy-guard 对需审批的动作**一律拒绝**(连 open 档
// 也不放行,见 sdk.WithUnattended 注释)。写类动作的沙箱范围仍按用户配置的档位。
//
// 到点时若正好有回合在跑:最多等 busy_wait_seconds 秒,仍忙则**本轮跳过**并记
// skipped(不排队、不叠加:计划的下次触发时间按 cron 照常推进)。
package hostschedule

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

const (
	// defaultBusyWait 到点时有回合在跑时的最长等待(默认 60s)。
	defaultBusyWait = 60 * time.Second
	// busyPoll 忙等待轮询间隔。
	busyPoll = 2 * time.Second
	// maxPromptRunes 任务描述长度上限(计划文件是人可读配置,超长即拒,不静默截断)。
	maxPromptRunes = 4000
	// maxNameRunes 计划名长度上限。
	maxNameRunes = 64
)

// Plugin 实现 host-schedule。requires ctx.agentLoop(回合入口);
// ctx.commands / ctx.turnControl 可选注入(未装配则跳过命令注册 / 不检查忙闲)。
type Plugin struct{}

func (p *Plugin) Name() string { return "host-schedule" }

// Start 加载计划、启动调度循环、提供 ctx.schedule 服务并注册 /schedule 命令。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var loop sdk.AgentLoop
	if err := c.Inject("ctx.agentLoop", &loop); err != nil {
		return nil, err // 缺回合入口 = 显式失败(定时任务不得自建执行路径)
	}
	var tc sdk.TurnControl
	_ = c.Inject("ctx.turnControl", &tc)
	var cmds sdk.CommandRegistry
	_ = c.Inject("ctx.commands", &cmds)

	s := New(loop, c.Logger())
	if tc != nil {
		s.SetTurnControl(tc)
	}
	if v, ok := m.Data["busy_wait_seconds"].(int); ok && v >= 0 {
		s.SetBusyWait(time.Duration(v) * time.Second)
	}
	s.SetNotify(func(ev sdk.ScheduleRunEvent) {
		c.Emit(context.Background(), sdk.EventScheduleRun, &ev, sdk.Emit)
	})
	// 服务先暴露再启动循环(卸载时由注册表归还服务键,见 core/plugin/registry)
	if err := c.Provide("ctx.schedule", s); err != nil {
		return nil, err
	}
	if err := s.launch(); err != nil {
		return nil, err
	}
	var disposers []sdk.Disposer
	if cmds != nil {
		d, err := cmds.Register(scheduleCommand(s))
		if err != nil {
			s.Stop()
			return nil, err
		}
		disposers = append(disposers, d)
	}
	return func() {
		s.Stop() // 先停循环并取消在跑回合(卸载即撤销),再注销命令
		for _, d := range disposers {
			d()
		}
	}, nil
}

// Scheduler 实现 sdk.ScheduleService。
type Scheduler struct {
	loop   sdk.AgentLoop
	tc     sdk.TurnControl // 可为 nil(未装配 ctx.turnControl)
	log    *slog.Logger
	now    func() time.Time
	notify func(sdk.ScheduleRunEvent) // 终态通知(schedule/run;可 nil)

	mu       sync.Mutex
	plans    map[string]*planEntry
	closed   bool
	started  bool // launch 只允许一次(防双循环/双 close(done))
	busyWait time.Duration

	wake chan struct{} // 计划变更信号(缓冲 1)
	ctx  context.Context
	stop context.CancelFunc
	done chan struct{}
	// doneOnce 保证 done 只关一次:launch 未成功时没有循环去关它,
	// Stop 需自己关(否则 Stop 会永久阻塞在 <-done)。
	doneOnce sync.Once
}

// planEntry 运行期计划(落盘结构 + 解析后的 cron + 排期状态)。
type planEntry struct {
	s       sdk.Schedule
	spec    *cronSpec
	next    time.Time
	rev     uint64 // 版本号:回合回写状态时校验计划未被改/删
	running bool
}

// New 构造调度服务(不读盘;launch 才载入并启动循环)。
func New(loop sdk.AgentLoop, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		loop:     loop,
		log:      log,
		now:      time.Now,
		busyWait: defaultBusyWait,
		plans:    map[string]*planEntry{},
		wake:     make(chan struct{}, 1),
		ctx:      ctx,
		stop:     cancel,
		done:     make(chan struct{}),
	}
}

// SetTurnControl 注入回合控制(用于「有回合在跑就先等/跳过」)。
func (s *Scheduler) SetTurnControl(tc sdk.TurnControl) { s.tc = tc }

// SetBusyWait 设置忙等待上限(0 = 不等待,忙即跳过)。
func (s *Scheduler) SetBusyWait(d time.Duration) { s.busyWait = d }

// SetClock 注入时钟(单测用;默认 time.Now)。
func (s *Scheduler) SetClock(fn func() time.Time) { s.now = fn }

// SetNotify 注入触发终态通知回调(schedule/run 事件;可 nil 关闭)。
func (s *Scheduler) SetNotify(fn func(sdk.ScheduleRunEvent)) { s.notify = fn }

// launch 载入计划并启动调度循环(重复调用 = 显式错误,防双循环)。
func (s *Scheduler) launch() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("host-schedule: 服务已卸载")
	}
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("host-schedule: 调度循环已启动")
	}
	plans, warnings, err := loadPlans()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	for _, w := range warnings {
		s.log.Warn("host-schedule: 计划文件有问题", "detail", w)
	}
	now := s.now()
	for _, p := range plans {
		e := &planEntry{s: p}
		spec, perr := parseCron(p.Cron)
		if perr != nil {
			// 手改坏的 cron:保留在列表里(用户可见、可改),但不排期
			s.log.Warn("host-schedule: 计划 cron 非法,已停排", "id", p.ID, "cron", p.Cron, "err", perr)
		} else {
			e.spec = spec
			if n, ok := spec.next(now); ok {
				e.next = n
			}
		}
		s.plans[p.ID] = e
	}
	s.started = true
	s.mu.Unlock()
	go s.loopFn()
	return nil
}

// Stop 停止调度循环、取消在跑回合并拒绝新触发(卸载/退出调用;幂等且等待循环退出)。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	already := s.closed
	started := s.started
	s.closed = true
	s.mu.Unlock()
	if already {
		<-s.done
		return
	}
	s.stop()
	if !started {
		s.closeDone() // launch 从未成功:没有循环会关 done,自己关掉(免死等)
	}
	<-s.done
}

// closeDone 关闭 done(幂等;循环退出与 Stop 兜底共用)。
func (s *Scheduler) closeDone() { s.doneOnce.Do(func() { close(s.done) }) }

// —— sdk.ScheduleService ——

// List 全部计划(稳定顺序:创建时间升序),含宿主机算的下次触发时间。
func (s *Scheduler) List() []sdk.Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sdk.Schedule, 0, len(s.plans))
	for _, e := range s.plans {
		out = append(out, s.viewLocked(e))
	}
	sortPlans(out)
	return out
}

// Add 新增计划(ID 空则自动生成)。
func (s *Scheduler) Add(p sdk.Schedule) (sdk.Schedule, error) {
	if err := s.accept(); err != nil {
		return sdk.Schedule{}, err
	}
	if p.ID == "" {
		id, err := newPlanID()
		if err != nil {
			return sdk.Schedule{}, err
		}
		p.ID = id
	}
	if !validID(p.ID) {
		return sdk.Schedule{}, fmt.Errorf("host-schedule: 计划 ID 非法(只允许小写字母/数字/连字符): %q", p.ID)
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = s.now()
	}
	e, err := s.validate(p)
	if err != nil {
		return sdk.Schedule{}, err
	}
	if err := savePlan(e.s); err != nil {
		return sdk.Schedule{}, err
	}
	s.mu.Lock()
	if _, dup := s.plans[p.ID]; dup {
		s.mu.Unlock()
		return sdk.Schedule{}, fmt.Errorf("host-schedule: 计划已存在: %s", p.ID)
	}
	s.plans[p.ID] = e
	s.mu.Unlock()
	s.signal()
	return s.view(e.s.ID), nil
}

// Update 按 ID 覆盖可变字段(Name/Cron/Prompt/Enabled),保留 CreatedAt 与运行记录。
func (s *Scheduler) Update(p sdk.Schedule) (sdk.Schedule, error) {
	if err := s.accept(); err != nil {
		return sdk.Schedule{}, err
	}
	s.mu.Lock()
	old, ok := s.plans[p.ID]
	s.mu.Unlock()
	if !ok {
		return sdk.Schedule{}, fmt.Errorf("host-schedule: 计划不存在: %s", p.ID)
	}
	merged := old.s
	merged.Name, merged.Cron, merged.Prompt, merged.Enabled = p.Name, p.Cron, p.Prompt, p.Enabled
	e, err := s.validate(merged)
	if err != nil {
		return sdk.Schedule{}, err
	}
	if err := savePlan(e.s); err != nil {
		return sdk.Schedule{}, err
	}
	s.mu.Lock()
	if cur, ok := s.plans[p.ID]; ok {
		e.rev = cur.rev + 1 // 使在跑回合的状态回写失效(避免旧结果覆盖新配置)
		e.running = cur.running
		s.plans[p.ID] = e
	}
	s.mu.Unlock()
	s.signal()
	return s.view(p.ID), nil
}

// Remove 删除计划。
func (s *Scheduler) Remove(id string) error {
	if err := s.accept(); err != nil {
		return err
	}
	s.mu.Lock()
	if _, ok := s.plans[id]; !ok {
		s.mu.Unlock()
		return fmt.Errorf("host-schedule: 计划不存在: %s", id)
	}
	delete(s.plans, id)
	s.mu.Unlock()
	if err := removePlanFile(id); err != nil {
		return err
	}
	s.signal()
	return nil
}

// RunNow 立即触发一次(不等下一个时点;不影响既有排期);异步执行,状态经 List 回读。
func (s *Scheduler) RunNow(id string) error {
	if err := s.accept(); err != nil {
		return err
	}
	s.mu.Lock()
	e, ok := s.plans[id]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("host-schedule: 计划不存在: %s", id)
	}
	if !e.s.Enabled {
		return fmt.Errorf("host-schedule: 计划已停用,先启用再运行: %s", id)
	}
	if !s.fire(id) {
		return fmt.Errorf("host-schedule: 计划正在运行中: %s", id)
	}
	return nil
}

// —— 内部 ——

// validate 校验并构造运行期计划(next 从当前时刻算)。
func (s *Scheduler) validate(p sdk.Schedule) (*planEntry, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Prompt = strings.TrimSpace(p.Prompt)
	p.Cron = strings.TrimSpace(p.Cron)
	if p.Name == "" {
		return nil, fmt.Errorf("host-schedule: 计划名称不能为空(用于在会话流与列表里辨认)")
	}
	if len([]rune(p.Name)) > maxNameRunes {
		return nil, fmt.Errorf("host-schedule: 计划名称过长(上限 %d 字)", maxNameRunes)
	}
	if p.Prompt == "" {
		return nil, fmt.Errorf("host-schedule: 任务描述不能为空(到点要交给模型的指令)")
	}
	if len([]rune(p.Prompt)) > maxPromptRunes {
		return nil, fmt.Errorf("host-schedule: 任务描述过长(上限 %d 字)", maxPromptRunes)
	}
	spec, err := parseCron(p.Cron)
	if err != nil {
		return nil, err
	}
	next, err := spec.nextOrError(s.now())
	if err != nil {
		return nil, err
	}
	return &planEntry{s: p, spec: spec, next: next}, nil
}

// accept 卸载后拒绝服务调用(幂等)。
func (s *Scheduler) accept() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return fmt.Errorf("host-schedule: 服务已卸载,拒绝新的计划操作")
	}
	return nil
}

// signal 唤醒调度循环重算下一个时点(缓冲 1,重复信号合并)。
func (s *Scheduler) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// loopFn 调度循环:算出「距最近的下次触发」并睡到点;计划变更即被 wake 叫醒重算。
func (s *Scheduler) loopFn() {
	defer s.closeDone()
	for {
		wait, due := s.snapshot()
		if len(due) > 0 {
			for _, id := range due {
				s.fire(id)
			}
			continue
		}
		timer := time.NewTimer(wait)
		select {
		case <-s.ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// snapshot 返回「已到点的计划 id」与「距最近下次触发的等待时长」。
// 无计划 = 一个很大的等待值(靠 wake 唤醒,不忙等)。
func (s *Scheduler) snapshot() (time.Duration, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var due []string
	wait := time.Duration(1<<62 - 1)
	for id, e := range s.plans {
		if !e.s.Enabled || e.next.IsZero() {
			continue
		}
		if !e.next.After(now) {
			due = append(due, id)
			continue
		}
		if d := e.next.Sub(now); d < wait {
			wait = d
		}
	}
	sort.Strings(due) // 稳定:同一时刻多计划按 id 顺序触发
	return wait, due
}

// fire 触发一个计划(排下一次 + 起 goroutine 跑回合);返回 false = 未触发。
func (s *Scheduler) fire(id string) bool {
	s.mu.Lock()
	e, ok := s.plans[id]
	if !ok || s.closed || e.running || !e.s.Enabled || e.spec == nil {
		s.mu.Unlock()
		return false
	}
	now := s.now()
	if n, err := e.spec.nextOrError(now); err == nil {
		e.next = n // 先推进排期,防同分钟重复触发
	} else {
		e.next = time.Time{}
	}
	e.running = true
	rev := e.rev
	input := scheduleInput(e.s)
	s.mu.Unlock()

	go s.execute(id, rev, input)
	return true
}

// execute 跑一轮(无人值守):等空闲 → 经 agentLoop.Run 走既有回合入口 → 回写状态。
func (s *Scheduler) execute(id string, rev uint64, input string) {
	state, errMsg := s.runTurn(input)
	now := s.now()

	s.mu.Lock()
	var saved sdk.Schedule
	if e, ok := s.plans[id]; ok && e.rev == rev {
		e.running = false
		e.s.LastRunAt = now
		e.s.LastStatus = state
		e.s.LastError = errMsg
		saved = e.s
	}
	s.mu.Unlock()

	if saved.ID != "" {
		if err := savePlan(saved); err != nil {
			s.log.Warn("host-schedule: 运行记录落盘失败", "id", id, "err", err)
		}
	}
	if s.notify != nil {
		s.notify(sdk.ScheduleRunEvent{ID: id, State: state, Error: errMsg, RunAt: now})
	}
	// 触发过不等于排期变了,但状态更新后 UI 需要新快照;唤醒一次无害(下次时点已算好)
	s.signal()
}

// runTurn 执行一轮:先等空闲(有回合在跑时最多等 busyWait),再以无人值守标记触发。
func (s *Scheduler) runTurn(input string) (sdk.ScheduleRunState, string) {
	if !s.waitIdle() {
		return sdk.ScheduleRunSkipped, fmt.Sprintf("到点时已有回合在运行,超过 %s 仍忙,本轮跳过(下次按 cron 照常)", s.busyWait)
	}
	ctx := sdk.WithUnattended(s.ctx)
	err := s.loop.Run(ctx, input)
	switch {
	case s.ctx.Err() != nil:
		return sdk.ScheduleRunSkipped, "gah 已退出或插件已卸载,回合中止"
	case err != nil:
		return sdk.ScheduleRunFailed, err.Error()
	}
	return sdk.ScheduleRunOK, ""
}

// waitIdle 等待「无运行中回合」;返回 false = 超时(调用方按跳过处理)。
func (s *Scheduler) waitIdle() bool {
	if s.tc == nil || !s.tc.Running() {
		return true
	}
	deadline := time.Now().Add(s.busyWait)
	for s.tc.Running() {
		if s.busyWait <= 0 || time.Now().After(deadline) {
			return false
		}
		select {
		case <-s.ctx.Done():
			return false
		case <-time.After(busyPoll):
		}
	}
	return true
}

// view 取一条计划视图(含 NextRun)。
func (s *Scheduler) view(id string) sdk.Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.plans[id]; ok {
		return s.viewLocked(e)
	}
	return sdk.Schedule{}
}

// viewLocked 计划视图(调用方持锁):附带派生 NextRun(不落盘的字段)。
func (s *Scheduler) viewLocked(e *planEntry) sdk.Schedule {
	out := e.s
	out.NextRun = time.Time{}
	if e.s.Enabled && !e.next.IsZero() {
		out.NextRun = e.next
	}
	return out
}

// sortPlans 计划列表稳定排序(创建时间升序,同刻按 ID)。
func sortPlans(ps []sdk.Schedule) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
}

// scheduleInput 到点交给模型的输入(带 [计划:名] 前缀,便于在会话流里辨认来源)。
func scheduleInput(p sdk.Schedule) string {
	name := p.Name
	if name == "" {
		name = p.ID
	}
	return fmt.Sprintf("[计划:%s] %s", name, p.Prompt)
}
