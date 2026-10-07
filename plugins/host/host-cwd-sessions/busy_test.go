package hostcwdsessions

// 「切走闸门」测试:当前会话正被回合写着时,切走/删除/换工作区必须**显式拒绝**。
//
// 钉的是不变量:拒绝发生在**任何状态变更之前**(当前会话 / 落盘路径 / cwd / key 都不许变),
// 否则就是半切换 —— 比直接报错更难收拾,尤其是 Delete:文件已删而回合还在往它写。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectedWhileCurrentSessionBusy(t *testing.T) {
	svc, _, main := startSvc(t)
	before := svc.CurrentSession()
	beforePath := svc.Path()

	main.MarkBusy()
	err := svc.Open("20260101-010101")
	if err == nil {
		t.Fatal("当前会话有回合在跑时切走必须被拒(否则回合后半截写进新会话)")
	}
	if !strings.Contains(err.Error(), "运行回合") {
		t.Fatalf("错误应说明原因,got %v", err)
	}
	if svc.CurrentSession() != before || svc.Path() != beforePath {
		t.Fatalf("拒绝后当前会话不该变:current=%q path=%q", svc.CurrentSession(), svc.Path())
	}

	main.MarkIdle()
	if err := svc.Open("20260101-010101"); err != nil {
		t.Fatalf("回合结束后应可切: %v", err)
	}
}

func TestDeleteCurrentSessionRejectedWhileBusy(t *testing.T) {
	svc, _, main := startSvc(t)
	cur := svc.CurrentSession()
	path := SessionPath(SessionsRoot(), svc.Current(), cur)
	// 当前会话可能从未落盘(空会话只在首次写入时建文件),先补一个文件让删除路径可走。
	if !fileExists(path) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	main.MarkBusy()
	if err := svc.Delete(cur); err == nil {
		t.Fatal("删除正在跑的当前会话必须被拒")
	}
	// 关键:文件不能已被删(否则回合继续往被 unlink 的句柄写 = 数据进黑洞)。
	if !fileExists(path) {
		t.Fatal("拒绝后文件不应被删除")
	}
	main.MarkIdle()
	if err := svc.Delete(cur); err != nil {
		t.Fatalf("空闲时应可删: %v", err)
	}
}

func TestSwitchDirRejectedWhileBusyAndLeavesCwd(t *testing.T) {
	svc, _, main := startSvc(t)
	other := t.TempDir()
	cur := svc.CurrentSession()
	beforeKey := svc.Current()

	main.MarkBusy()
	if _, err := svc.SwitchDir(other); err == nil {
		t.Fatal("换工作区(会离开当前会话)必须有回合在跑时被拒")
	}
	wd, _ := os.Getwd()
	resolved, _ := filepath.EvalSymlinks(other)
	resolvedWD, _ := filepath.EvalSymlinks(wd)
	if resolvedWD == resolved {
		t.Fatal("拒绝后 cwd 不该已被改走(半切换)")
	}
	if svc.Current() != beforeKey || svc.CurrentSession() != cur {
		t.Fatalf("拒绝后 key/会话不该变:%q/%q", svc.Current(), svc.CurrentSession())
	}
}

func TestGuardPassesWhenLogHasNoBusyMarker(t *testing.T) {
	// 日志实现不带 sdk.BusyMarker(=不是 host-session-log 的 Log)时判为无人在写:
	// 不能因为能力缺失就把会话锁死。
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", tmp)
	fs := &fakeSessions{}
	svc := &Service{key: "key", path: filepath.Join(tmp, "key.jsonl"), sessions: fs}
	if err := svc.guardLeaveBusy(); err != nil {
		t.Fatalf("无 BusyMarker 时不该拦: %v", err)
	}
}

// 回归护栏:闸门判定必须落在**单例**上。当前会话的回合拿的是 ctx.sessions 单例
// (acquire("") 返回它),换成注册表实例判定就等于没判。
func TestBusyMarkerOnSingletonIsWhatGateReads(t *testing.T) {
	svc, _, main := startSvc(t)
	var marker sdkBusyMarker = main
	if marker.IsBusy() {
		t.Fatal("初始不应 busy")
	}
	marker.MarkBusy()
	if !marker.IsBusy() || svc.guardLeaveBusy() == nil {
		t.Fatal("单例置位后闸门必须拦")
	}
	marker.MarkBusy() // 嵌套两次
	marker.MarkIdle()
	if !marker.IsBusy() {
		t.Fatal("只清一次时仍应 busy(计数式)")
	}
	marker.MarkIdle()
	if marker.IsBusy() || svc.guardLeaveBusy() != nil {
		t.Fatal("全部清零后闸门应放行")
	}
}

// sdkBusyMarker 是断言用的最小接口面(等价 sdk.BusyMarker)。
type sdkBusyMarker interface {
	MarkBusy()
	MarkIdle()
	IsBusy() bool
}

// 删除正被别的视图持有的会话必须被拒:那个视图上的回合还在往这个文件里写,
// 删掉文件等于把别人的进行中会话扔进黑洞(写入 unlink 掉的 inode)。
// 与 Open 的 heldRefs 闸门同一形状 —— 上一批只给 Open 加了,删漏了。
func TestDeleteSessionHeldByOtherView(t *testing.T) {
	svc, _, _ := startSvc(t)
	key := svc.Current()
	other := "20260101-020202"
	if err := os.WriteFile(SessionPath(SessionsRoot(), key, other), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.dir.Acquire(other); err != nil {
		t.Fatalf("预持有: %v", err)
	}
	err := svc.Delete(other)
	if err == nil {
		t.Fatal("删正被其他视图持有的会话必须显式拒绝")
	}
	if !fileExists(SessionPath(SessionsRoot(), key, other)) {
		t.Fatal("拒绝后文件不应被删")
	}
	svc.dir.Release(other)
	if err := svc.Delete(other); err != nil {
		t.Fatalf("释放后应可删: %v", err)
	}
}
