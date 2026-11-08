// failures.go:重复失败的观察者(C 批)。
//
// 干什么:订阅 `tools/post-execute`,工具报错时把「工具 + 归一化错误首行」记进
// $GAH_HOME/memory/failures.json;同一签名到阈值**提一条 pitfall 候选**(不直写)。
//
// 为什么在宿主侧订阅而不是做进工具里:失败信息只在这里汇总得到(工具自己只知道自己
// 那一次),而落点必须是插件 —— 能关、能调阈值、卸了不留痕。
//
// 三条硬约束:
//   - **不改工具结果**:post-execute 是 waterfall,本观察者只读、返回 nil;
//   - **不花模型调用**:纯规则路径,账不记进 extract-state(那是"花了多少钱"的账);
//   - **只产候选**:pitfall 永远等人在候选池里确认(自动写一条坑 = 静默提权)。
package hostmemory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nekoleamo/go-agent-harness/internal/memory"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 失败触发的模式(与 data.extract / data.retrieve 同款:未知值显式失败)。
const (
	failTriggerOn  = "on"
	failTriggerOff = "off"

	// failFlushEvery 每累计几次失败落一次盘。失败本身不频繁,但一个循环里反复失败的
	// 工具会在几秒内打十几次 —— 每次一次原子写没必要,账本是信号不是账务。
	failFlushEvery = 4
)

// failureWatch 失败观察者(装配在 host-memory 里;off 时整体不存在)。
type failureWatch struct {
	c         sdk.Ctx
	svc       *Service
	threshold int

	mu   sync.Mutex
	st   memory.FailureState
	dirt int
}

// newFailureWatch 按 data.fail_trigger / data.fail_threshold 装配(off ⇒ nil,不订阅)。
func newFailureWatch(c sdk.Ctx, svc *Service, m *sdk.Manifest) (*failureWatch, error) {
	mode, threshold := failTriggerOn, 2
	if m != nil && m.Data != nil {
		if v, ok := m.Data["fail_trigger"].(string); ok && strings.TrimSpace(v) != "" {
			mode = strings.TrimSpace(v)
		}
		if v, ok := m.Data["fail_threshold"].(int); ok && v > 0 {
			threshold = v
		}
	}
	switch mode {
	case failTriggerOn, failTriggerOff:
	default:
		return nil, fmt.Errorf("host-memory: 不支持的 data.fail_trigger=%q(支持 %s|%s)", mode, failTriggerOn, failTriggerOff)
	}
	if mode == failTriggerOff {
		return nil, nil
	}
	// 阈值 1 等于"每次失败都提候选":候选池(12 条 / 日 5 条)半天就被灌满,
	// 而一条"刚失败一次"的候选对人没有信息量。显式拒绝而不是悄悄抬到 2。
	if threshold < 2 {
		return nil, fmt.Errorf("host-memory: data.fail_threshold=%d 太小(至少 2;1 会让候选池被单次失败灌满)", threshold)
	}
	st, err := memory.LoadFailures()
	if err != nil {
		c.Logger().Warn("host-memory: 失败账本读取失败(按空账本继续)", "err", err)
	}
	return &failureWatch{c: c, svc: svc, threshold: threshold, st: st}, nil
}

// start 订阅 post-execute,返回撤销函数(卸载时先取消防订阅、再落盘)。
func (f *failureWatch) start() sdk.Disposer {
	d := f.c.Subscribe("tools/post-execute", f.handle)
	return func() {
		d()
		f.flush(true)
	}
}

// handle 记录一次工具失败;到阈值时提一条候选。
func (f *failureWatch) handle(ctx context.Context, ev *sdk.Event) error {
	res, ok := ev.Payload.(*sdk.ToolResult)
	if !ok || res == nil || strings.TrimSpace(res.Error) == "" {
		return nil // 别的形态载荷 / 成功结果:不是我们的面(不猜、不改)
	}
	tool := sdk.ToolNameFrom(ctx)
	if tool == "" {
		return nil // 拿不到工具名就没法归组(宁可漏也不写一条"未知工具")
	}
	if memory.IsPolicyDenial(res.Error) {
		// 审批/策略/沙箱主动挡下的调用不是工具缺陷:用户点两次"拒绝"不该变成一条候选。
		return nil
	}
	project := f.svc.currentProjectKey()
	session := sdk.SessionFromContext(ctx)

	f.mu.Lock()
	st, hit := f.st.Record(tool, res.Error, project, session, f.threshold, time.Now())
	f.st = st
	f.dirt++
	flush := f.dirt >= failFlushEvery
	if flush {
		f.dirt = 0
	}
	snap := f.st
	f.mu.Unlock()
	if flush {
		f.persist(snap)
	}
	if !hit.Fire {
		return nil
	}
	f.propose(hit, session)
	return nil
}

// propose 提一条 pitfall 候选。失败不改变回合,只留日志 —— 候选池满了也是正常情况。
func (f *failureWatch) propose(hit memory.FailureHit, session string) {
	text := memory.FailureEntry{Tool: hit.Tool, Head: hit.Head, Count: hit.Count}.CandidateText()
	if err := memory.Propose(text, session); err != nil {
		f.c.Logger().Debug("host-memory: 失败触发未进候选池", "tool", hit.Tool, "err", err)
		return
	}
	f.c.Logger().Info("host-memory: 重复失败已进候选池(等人工确认)", "tool", hit.Tool, "count", hit.Count)
}

// FailureTriggerStats 实现 sdk.MemoryFailTrigger(累计失败次数 / 已提候选条数)。
func (f *failureWatch) FailureTriggerStats() (hits, fired int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.st.Items {
		hits += e.Count
		fired += e.Fired
	}
	return hits, fired
}

// persist 落盘(失败只影响信号精度,不影响回合)。
func (f *failureWatch) persist(snap memory.FailureState) {
	if err := snap.Save(); err != nil {
		f.c.Logger().Warn("host-memory: 失败账本落盘失败", "err", err)
	}
}

// flush 把账本立刻落盘(卸载时必调)。
func (f *failureWatch) flush(force bool) {
	f.mu.Lock()
	if !force && f.dirt == 0 {
		f.mu.Unlock()
		return
	}
	snap := f.st
	f.dirt = 0
	f.mu.Unlock()
	f.persist(snap)
}
