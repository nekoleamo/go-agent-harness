package hostagentloop

// 多会话并行(实现 sdk.SessionRunner)的行为测试。
//
// 这里钉的是**不变量**,不是函数覆盖率:
//  ① 不同会话的回合互不交错(各自事件流里 user/tool/assistant 严格配对);
//  ② 同一会话内两个回合仍**严格串行**(单写者不变量不能因为并行而丢);
//  ③ 取消按会话:停 A 不影响 B;
//  ④ 没装会话目录时,向指定会话跑回合**显式报错**而不是静默写进主会话。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/plugins/host/host-session-log"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// memDir 实现 sdk.SessionDir(测试用):按 id 给独立的内存日志。
type memDir struct {
	mu     sync.Mutex
	byID   map[string]sdk.SessionLog
	cur    string
	acqN   map[string]int
	relN   map[string]int
	failOn string // 非空:该 id 的 Acquire 一律失败(测「显式报错」)
}

func newMemDir(cur string) *memDir {
	return &memDir{byID: map[string]sdk.SessionLog{}, acqN: map[string]int{}, relN: map[string]int{}, cur: cur}
}

func (d *memDir) Acquire(id string) (sdk.SessionLog, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failOn == id {
		return nil, errors.New("会话文件不可读(模拟权限失败)")
	}
	if id == "" || id == d.cur {
		return nil, errors.New("不应向目录要主会话")
	}
	d.acqN[id]++
	if l, ok := d.byID[id]; ok {
		return l, nil
	}
	l := sessionlog.NewMemLog()
	d.byID[id] = l
	return l, nil
}

func (d *memDir) Release(id string) {
	d.mu.Lock()
	d.relN[id]++
	d.mu.Unlock()
}

func (d *memDir) Active() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.byID))
	for id := range d.byID {
		out = append(out, id)
	}
	return out
}

func (d *memDir) Spawn() (string, error) { return "", errors.New("测试里不用 spawn") }

// TestTwoSessionsRunInParallelNoInterleave 两个会话并行跑,各自历史互不串。
func TestTwoSessionsRunInParallelNoInterleave(t *testing.T) {
	e := buildEnv(t, `[{"text":"ok-A","finish":"stop"},{"text":"ok-B","finish":"stop"}]`)
	dir := newMemDir("cur-1")
	e.loop.sdir = dir
	e.loop.cs = nil // 归一化只看 sdir/空键

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, sid := range []string{"sA", "sB"} {
		wg.Add(1)
		go func(i int, sid string) {
			defer wg.Done()
			errs[i] = e.loop.RunInSession(context.Background(), sid, "任务-"+sid)
		}(i, sid)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("会话 %d 跑失败: %v", i, err)
		}
	}
	for _, sid := range []string{"sA", "sB"} {
		lg := dir.byID[sid].(*sessionlog.Log)
		evs := lg.Replay()
		if n := countKind(lg, sdk.EventUserMessage); n != 1 {
			t.Fatalf("会话 %s 应只有 1 条用户消息,got %d(%v)", sid, n, msKinds(evs))
		}
		if n := countKind(lg, sdk.EventAssistantMessage); n != 1 {
			t.Fatalf("会话 %s 应只有 1 条助手消息,got %d(%v)", sid, n, msKinds(evs))
		}
		// 引用必须原样落在这个会话里(串到别的会话就是最严重的错位)
		if !msHasUserText(evs, "任务-"+sid) {
			t.Fatalf("会话 %s 的历史里没有自己的输入: %v", sid, msKinds(evs))
		}
	}
	// 主会话必须干干净净(并行不该碰它)。
	if n := countKind(e.sessions.(*sessionlog.Log), sdk.EventUserMessage); n != 0 {
		t.Fatalf("主会话被污染:%v", msKinds(e.sessions.Replay()))
	}
}

// countingLLM 数**同时**处在 Complete 里的请求数(真正的并行证据)。
// blockLLM 只证明「挂住了」,不能区分「两个都在跑」与「第二个在等锁」。
type countingLLM struct {
	errLLM
	mu      sync.Mutex
	inside  int
	peak    int
	entered chan struct{}
}

func (c *countingLLM) Complete(ctx context.Context, _ *sdk.LLMRequest, _ func(sdk.LLMStreamEvent) error) (*sdk.LLMResponse, error) {
	c.mu.Lock()
	c.inside++
	if c.inside > c.peak {
		c.peak = c.inside
	}
	c.mu.Unlock()
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	c.mu.Lock()
	c.inside--
	c.mu.Unlock()
	return nil, ctx.Err()
}

func (c *countingLLM) peakConcurrency() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peak
}

// TestTwoSessionsRunInParallel 真并行证据:两个会话的回合**同时**在跑。
//
// 判据是「同时处在 Complete 的请求数达到 2」—— 只看事件不交错是不够的:
// 串行也满足不交错,那样这个测试就验不出锁到底按没按会话分。
func TestTwoSessionsRunInParallel(t *testing.T) {
	e := buildEnv(t, `[{"text":"x","finish":"stop"}]`)
	dir := newMemDir("cur-1")
	e.loop.sdir = dir
	cl := &countingLLM{entered: make(chan struct{}, 8)}
	e.loop.llm = cl

	var wg sync.WaitGroup
	for _, sid := range []string{"sA", "sB"} {
		wg.Add(1)
		go func(sid string) {
			defer wg.Done()
			_ = e.loop.RunInSession(context.Background(), sid, "任务-"+sid)
		}(sid)
	}
	// 等两个都进入 Complete
	for i := 0; i < 2; i++ {
		select {
		case <-cl.entered:
		case <-time.After(3 * time.Second):
			t.Fatalf("第 %d 个会话没能进入模型请求(峰值并发 %d)", i+1, cl.peakConcurrency())
		}
	}
	if got := cl.peakConcurrency(); got < 2 {
		t.Fatalf("两个会话应真并行(同时在 Complete),峰值并发只有 %d", got)
	}
	// 两个会话都应登记在 RunningSessions 里
	rs := e.loop.RunningSessions()
	if len(rs) < 2 {
		t.Fatalf("RunningSessions 应含两个会话,got %v", rs)
	}
	// 收尾:按会话取消
	e.loop.CancelSession("sA")
	e.loop.CancelSession("sB")
	wg.Wait()
}

// TestSameSessionStillSerial 同会话并发提交仍串行(单写者不变量)。
func TestSameSessionStillSerial(t *testing.T) {
	e := buildEnv(t, `[{"text":"1","finish":"stop"},{"text":"2","finish":"stop"},{"text":"3","finish":"stop"},{"text":"4","finish":"stop"}]`)
	dir := newMemDir("cur-1")
	e.loop.sdir = dir

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = e.loop.RunInSession(context.Background(), "same", fmt.Sprintf("第 %d 条", i))
		}(i)
	}
	wg.Wait()
	evs := dir.byID["same"].(*sessionlog.Log).Replay()
	// 串行判据一:回合边界严格交替(交错会出现 turn/start 嵌套或 turn/end 早退)
	depth := 0
	for _, e := range evs {
		switch e.Kind {
		case sdk.EventTurnStart:
			if depth != 0 {
				t.Fatalf("交错:turn/start 嵌套(%v)", msKinds(evs))
			}
			depth++
		case sdk.EventTurnEnd:
			if depth != 1 {
				t.Fatalf("交错:turn/end 不配对(%v)", msKinds(evs))
			}
			depth--
		}
	}
	if depth != 0 {
		t.Fatalf("回合未闭合:%v", msKinds(evs))
	}
	// 串行判据二:user 与 assistant 严格交替(user₁→assistant₁→user₂→assistant₂…)
	// ——交错的话会看到两个 user 连着、或某个 assistant 先于其 user。
	wantUser := true
	nUser, nAsst := 0, 0
	for _, e := range evs {
		switch e.Kind {
		case sdk.EventUserMessage:
			if !wantUser {
				t.Fatalf("两个 user 连着(交错?):%v", msKinds(evs))
			}
			wantUser = false
			nUser++
		case sdk.EventAssistantMessage:
			if wantUser {
				t.Fatalf("assistant 先于 user(交错?):%v", msKinds(evs))
			}
			wantUser = true
			nAsst++
		}
	}
	if nUser != 4 || nAsst != 4 {
		t.Fatalf("应有 4 组 user/assistant,got %d/%d", nUser, nAsst)
	}
}

// TestCancelSessionOnlyThatSession 取消按会话:停 A 不动 B。
func TestCancelSessionOnlyThatSession(t *testing.T) {
	e := buildEnv(t, `[{"text":"x","finish":"stop"}]`)
	dir := newMemDir("cur-1")
	e.loop.sdir = dir
	e.loop.llm = &blockLLM{} // 两个回合都会挂在 Complete 上,等取消

	doneA := make(chan error, 1)
	go func() { doneA <- e.loop.RunInSession(context.Background(), "sA", "A") }()
	waitRunning(t, e.loop, "sA")

	// 取消别的会话:不应命中
	if e.loop.CancelSession("sB") {
		t.Fatal("取消未运行的会话不应返回 true")
	}
	if !e.loop.CancelSession("sA") {
		t.Fatal("取消 sA 应命中")
	}
	select {
	case err := <-doneA:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("sA 应被取消,got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("sA 未被取消")
	}
}

// TestRunInSessionWithoutDirFailsLoudly 没装会话目录时显式报错(不静默写主会话)。
func TestRunInSessionWithoutDirFailsLoudly(t *testing.T) {
	e := buildEnv(t, `[{"text":"x","finish":"stop"}]`)
	e.loop.sdir = nil // 模拟 ctx.sessionDir 未装配
	err := e.loop.RunInSession(context.Background(), "nope", "任务")
	if err == nil {
		t.Fatal("没有会话目录时应显式报错")
	}
	if n := countKind(e.sessions.(*sessionlog.Log), sdk.EventUserMessage); n != 0 {
		t.Fatalf("不得把内容写进主会话:%v", msKinds(e.sessions.Replay()))
	}
}

// TestAcquireFailureIsReported 取日志失败要报错(不得当成空会话跑)。
func TestAcquireFailureIsReported(t *testing.T) {
	e := buildEnv(t, `[{"text":"x","finish":"stop"}]`)
	dir := newMemDir("cur-1")
	dir.failOn = "bad"
	e.loop.sdir = dir
	if err := e.loop.RunInSession(context.Background(), "bad", "任务"); err == nil {
		t.Fatal("取日志失败应报错")
	}
	if n := countKind(e.sessions.(*sessionlog.Log), sdk.EventUserMessage); n != 0 {
		t.Fatalf("失败也不该写主会话:%v", msKinds(e.sessions.Replay()))
	}
}

func TestRunningSessionsListsActive(t *testing.T) {
	e := buildEnv(t, `[{"text":"x","finish":"stop"}]`)
	dir := newMemDir("cur-1")
	e.loop.sdir = dir
	e.loop.llm = &blockLLM{}
	go func() { _ = e.loop.RunInSession(context.Background(), "sA", "A") }()
	waitRunning(t, e.loop, "sA")
	got := e.loop.RunningSessions()
	found := false
	for _, s := range got {
		if s == "sA" {
			found = true
		}
	}
	if !found {
		t.Fatalf("RunningSessions 应含 sA,got %v", got)
	}
	e.loop.CancelSession("sA")
}

// —— 小工具 ——

func msKinds(evs []sdk.SessionEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Kind)
	}
	return out
}

func msHasUserText(evs []sdk.SessionEvent, text string) bool {
	for _, e := range evs {
		if e.Kind != sdk.EventUserMessage {
			continue
		}
		if um, ok := e.Payload.(sdk.UserMessage); ok && um.Content == text {
			return true
		}
	}
	return false
}

func waitRunning(t *testing.T, l *Loop, want string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		for _, s := range l.RunningSessions() {
			if s == want {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("会话 %s 迟迟未进入运行态:%v", want, l.RunningSessions())
}

// TestSecondRoundSeesFirstRoundHistory 同会话跑第二轮时,模型必须能看到第一轮的全部消息。
//
// 为什么单独钉:并发压测(见 tests/multisession_parallel_e2e_test.go)里第二轮的模型请求
// 看到的是**第一轮的用户消息** —— 也就是"新输入没进投影"。那会让模型以为这轮没新东西,
// 直接收尾,用户表现为"第二轮说了话,模型当没听见"。这里用白盒把每次请求的完整消息抓出来断言。
func TestSecondRoundSeesFirstRoundHistory(t *testing.T) {
	e, llm := buildEnvScripted(t, []string{"第一轮答复", "第二轮答复"})
	if err := e.loop.Run(context.Background(), "第一轮的问题"); err != nil {
		t.Fatal(err)
	}
	if err := e.loop.Run(context.Background(), "第二轮的问题"); err != nil {
		t.Fatal(err)
	}
	if len(llm.requests) != 2 {
		t.Fatalf("每轮一次请求,应共 2 次,实得 %d", len(llm.requests))
	}
	second := llm.requests[1]
	var users []string
	for _, m := range second {
		if m.Role == sdk.RoleUser {
			users = append(users, m.Content)
		}
	}
	found := false
	for _, u := range users {
		if strings.Contains(u, "第二轮的问题") {
			found = true
		}
	}
	if !found {
		t.Fatalf("第二次请求的投影里必须有第二轮的用户输入,实得用户消息 %v", users)
	}
	// 第一轮的内容也要在(否则是"只带最新一句",历史断了)
	if len(users) < 2 || !strings.Contains(users[0], "第一轮的问题") {
		t.Fatalf("第二次请求应同时带第一轮与第二轮的用户消息,实得 %v", users)
	}
}
