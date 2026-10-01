package hostschedule

// 调度租约的语义(第一百零四批):多实例共享数据根时,同一 cron **只能被一个实例触发**。
//
// 判据取"第二个 Start 是否拿到租约",而不是"循环跑了几次" —— 后者要等真实时间,
// 且 busyWait 一变就不可靠。

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestLeaseExclusive(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	h1, busy, err := acquireLease()
	if err != nil {
		t.Fatalf("首次应取到租约: %v", err)
	}
	if busy {
		t.Fatal("首次不应 busy")
	}
	defer h1.Release()

	// 第二个"实例"(同进程也要能判 —— 跨进程由 flock 保证,同进程由 xlock 的进程内层保证)
	_, busy2, err2 := acquireLease()
	if err2 != nil {
		t.Fatalf("busy 不是错误: %v", err2)
	}
	if !busy2 {
		t.Fatal("已有持租约者时,第二个应报 busy(否则计划会跑两遍)")
	}
	// 释放后可接管(持租实例退出后,另一实例必须能接手)
	h1.Release()
	h3, busy3, err3 := acquireLease()
	if err3 != nil {
		t.Fatalf("释放后取租约失败: %v", err3)
	}
	if busy3 {
		t.Fatal("释放后应能取到租约")
	}
	h3.Release()
}

func TestLeasePathInSchedulesDir(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	p := leasePath()
	if filepath.Dir(p) != filepath.Join(sdk.Home(), "schedules") {
		t.Fatalf("租约应在 schedules 目录(随数据根迁移),got %s", p)
	}
	if filepath.Ext(p) != ".lease" {
		t.Fatalf("租约文件名应固定,got %s", p)
	}
	// 租约文件**不该被当成计划**读进来(loadPlans 扫的是 *.yaml,这里守它别炸)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	plans, _, err := loadPlans()
	if err != nil {
		t.Fatalf("租约文件不应让 loadPlans 失败: %v", err)
	}
	if len(plans) != 0 {
		t.Fatalf("租约文件被当成计划读进来了: %v", plans)
	}
}
