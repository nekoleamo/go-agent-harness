package hostcwdsessions

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// testCtx 最小 ctx:只提供 Inject 的会话日志/注册表,其余为 no-op。
type testCtx struct {
	sdk.Ctx
	sessions sdk.SessionLog
	logs     sdk.SessionLogs
	provides map[string]any
}

func (c *testCtx) Inject(key string, target any) error {
	switch k := key; {
	case k == "ctx.sessions":
		p, ok := target.(*sdk.SessionLog)
		if !ok {
			return errInject
		}
		*p = c.sessions
	case k == "ctx.sessionLogs":
		p, ok := target.(*sdk.SessionLogs)
		if !ok {
			return errInject
		}
		*p = c.logs
	default:
		return errInject
	}
	return nil
}

func (c *testCtx) Provide(key string, v any) error {
	if c.provides == nil {
		c.provides = map[string]any{}
	}
	c.provides[key] = v
	return nil
}

// Emit 覆盖:嵌入的 nil Ctx 会被调用到(Start 里的 emitWS/emitSession 闭包)。
func (c *testCtx) Emit(_ context.Context, _ string, _ any, _ sdk.DispatchMode) (any, error) {
	return nil, nil
}

type injectErr struct{}

func (injectErr) Error() string { return "inject 未命中" }

var errInject = injectErr{}

// memLog 极简 SessionLog:只记 path(够本文件断言「拿到哪个会话」)。
type memLog struct {
	path string
	evs  []sdk.SessionEvent
	// busy 模拟 sdk.BusyMarker(切走闸门要读它):测试替身实现全套,才验得到真判定路径。
	busy atomic.Int32
}

func (m *memLog) MarkBusy() { m.busy.Add(1) }
func (m *memLog) MarkIdle() {
	for {
		n := m.busy.Load()
		if n <= 0 {
			return
		}
		if m.busy.CompareAndSwap(n, n-1) {
			return
		}
	}
}
func (m *memLog) IsBusy() bool { return m.busy.Load() > 0 }

func (m *memLog) Append(ev sdk.SessionEvent) error              { m.evs = append(m.evs, ev); return nil }
func (m *memLog) DeriveMessages() []sdk.LLMMessage              { return nil }
func (m *memLog) Replay() []sdk.SessionEvent                    { return m.evs }
func (m *memLog) Flush() error                                  { return nil }
func (m *memLog) SetPath(p string)                              { m.path = p }
func (m *memLog) Load(p string) error                           { m.path = p; return nil }
func (m *memLog) SetHistory(int)                                {}
func (m *memLog) RegisterCompressor(int, sdk.SessionCompressor) {}

var _ sdk.BusyMarker = (*memLog)(nil)

// recLogs 记录 Acquire/Release 的注册表(断言 id 与 path 配对)。
type recLogs struct {
	byPath map[string]*memLog
	acq    []string
	rel    []string
}

func newRecLogs() *recLogs { return &recLogs{byPath: map[string]*memLog{}} }

func (r *recLogs) Acquire(path, id string) (sdk.SessionLog, error) {
	if _, bad := r.byPath["!"+path]; bad {
		return nil, os.ErrPermission
	}
	r.acq = append(r.acq, id+"="+filepath.Base(path))
	if l, ok := r.byPath[path]; ok {
		return l, nil
	}
	l := &memLog{path: path}
	r.byPath[path] = l
	return l, nil
}
func (r *recLogs) Release(path string) { r.rel = append(r.rel, filepath.Base(path)) }
func (r *recLogs) Active() []string {
	out := make([]string, 0, len(r.byPath))
	for p := range r.byPath {
		out = append(out, filepath.Base(p))
	}
	return out
}

func startSvc(t *testing.T) (*Service, *recLogs, *memLog) {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	home := sdk.Home()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	main := &memLog{}
	logs := newRecLogs()
	c := &testCtx{sessions: main, logs: logs}
	p := &Plugin{}
	if _, err := p.Start(c, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 取出 Service(Start 里 Provide 了 ctx.sessionDir / ctx.cwdSessions)
	svc, _ := c.provides["ctx.cwdSessions"].(*Service)
	if svc == nil {
		t.Fatal("未提供 ctx.cwdSessions")
	}
	return svc, logs, main
}

func TestAcquireEmptyIDReturnsMainSingleton(t *testing.T) {
	svc, logs, main := startSvc(t)
	got, err := svc.dir.Acquire("")
	if err != nil {
		t.Fatalf("Acquire(\"\"): %v", err)
	}
	if got != sdk.SessionLog(main) {
		t.Fatal("id 空应返回主单例(向后兼容的关键)")
	}
	if len(logs.acq) != 0 {
		t.Fatalf("id 空不应走注册表,got %v", logs.acq)
	}
}

func TestAcquireCurrentSessionReturnsMainSingleton(t *testing.T) {
	svc, logs, main := startSvc(t)
	cur := svc.CurrentSession()
	if cur == "" {
		t.Fatal("启动应新开会话")
	}
	got, err := svc.dir.Acquire(cur)
	if err != nil {
		t.Fatalf("Acquire(当前会话): %v", err)
	}
	if got != sdk.SessionLog(main) {
		t.Fatal("当前会话应返回主单例(否则两个 Log 写同一文件)")
	}
	if len(logs.acq) != 0 {
		t.Fatalf("当前会话不应走注册表,got %v", logs.acq)
	}
}

func TestAcquireOtherSessionIndependentInstance(t *testing.T) {
	svc, logs, _ := startSvc(t)
	// 造一个非当前会话的落盘文件。
	key := svc.Current()
	other := "20260101-000000"
	p := SessionPath(SessionsRoot(), key, other)
	_ = os.WriteFile(p, []byte(`{"kind":"user/message","seq":1,"payload":{}}`+"\n"), 0o600)
	got, err := svc.dir.Acquire(other)
	if err != nil {
		t.Fatalf("Acquire(其它会话): %v", err)
	}
	if got == nil {
		t.Fatal("应拿到独立实例")
	}
	if len(logs.acq) != 1 || logs.acq[0] != other+"="+filepath.Base(p) {
		t.Fatalf("应向注册表要 id+path,got %v", logs.acq)
	}
	// 记账:Active 报这个 id。
	act := svc.dir.Active()
	if len(act) != 1 || act[0] != other {
		t.Fatalf("Active 应含该 id,got %v", act)
	}
	// Release 归还 + 记账清零。
	svc.dir.Release(other)
	if len(svc.dir.Active()) != 0 {
		t.Fatalf("Release 后应清空,got %v", svc.dir.Active())
	}
	if len(logs.rel) != 1 {
		t.Fatalf("应向注册表 Release 一次,got %v", logs.rel)
	}
	// 幂等:重复 Release 不再记。
	svc.dir.Release(other)
	if len(logs.rel) != 1 {
		t.Fatalf("未持有时 Release 应 no-op,got %v", logs.rel)
	}
}

func TestAcquireRejectsIllegalID(t *testing.T) {
	svc, _, _ := startSvc(t)
	if _, err := svc.dir.Acquire("../escape"); err == nil {
		t.Fatal("非法 id(路径穿越)应显式报错")
	}
}

func TestOpenRejectsSessionHeldByOtherView(t *testing.T) {
	svc, _, _ := startSvc(t)
	key := svc.Current()
	other := "20260101-010101"
	if err := os.WriteFile(SessionPath(SessionsRoot(), key, other), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.dir.Acquire(other); err != nil {
		t.Fatalf("预持有: %v", err)
	}
	// 切向它必须被拒:否则主单例与注册表实例写同一文件。
	err := svc.Open(other)
	if err == nil {
		t.Fatal("切向正被其他视图持有的会话应显式拒绝")
	}
	if svc.CurrentSession() == other {
		t.Fatal("拒绝后当前会话不应变化")
	}
	// 释放后可正常切。
	svc.dir.Release(other)
	if err := svc.Open(other); err != nil {
		t.Fatalf("释放后应可切: %v", err)
	}
}

func TestSessionEventNameRegistered(t *testing.T) {
	// 防回归:事件名 helper 存在且前缀正确(消费方按它订阅)。
	if got := sdk.SessionEventName("abc"); got != "session/event/abc" {
		t.Fatalf("got %q", got)
	}
}
