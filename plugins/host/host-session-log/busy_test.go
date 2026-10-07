package sessionlog

// sdk.BusyMarker 实现测试:计数式标记(同会话重叠持有不会提前解除),
// 且 MarkIdle 幂等(清到 0 再清不能变负 —— 负计数会让后续 MarkBusy 后 IsBusy 仍为假的窗口)。
import "testing"

func TestBusyMarkerCountsAndFloorsAtZero(t *testing.T) {
	l := NewMemLog()
	if l.IsBusy() {
		t.Fatal("新实例不该 busy")
	}
	l.MarkBusy()
	if !l.IsBusy() {
		t.Fatal("MarkBusy 后应 busy")
	}
	l.MarkBusy()
	l.MarkIdle()
	if !l.IsBusy() {
		t.Fatal("两次置位只清一次时仍应 busy(计数式)")
	}
	l.MarkIdle()
	if l.IsBusy() {
		t.Fatal("清零后不该 busy")
	}
	l.MarkIdle() // 多余的一次
	l.MarkIdle()
	if l.busy.Load() != 0 {
		t.Fatalf("计数不得为负,got %d", l.busy.Load())
	}
	// 清零后重新置位必须能被看到(负计数会让这里读成 false)。
	l.MarkBusy()
	if !l.IsBusy() {
		t.Fatal("清零后再次置位应生效")
	}
}

func TestBusyIndependentPerInstance(t *testing.T) {
	// 多会话并行时每个会话一份实例:标记不能互相污染(否则 A 的回合会锁死 B 的切换)。
	a, b := NewMemLog(), NewMemLog()
	a.MarkBusy()
	if b.IsBusy() {
		t.Fatal("另一个会话实例不该被标记")
	}
}