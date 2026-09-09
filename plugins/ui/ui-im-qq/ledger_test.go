// delivery ledger 单测:滞留落盘重启恢复/取走删除/放回。
package uimqq

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLedgerRoundtrip Set → 新实例(同路径=重启)恢复 → GetAndClear 清空并落盘。
func TestLedgerRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.yaml")
	l := newDeliveryLedger(path)
	l.Set("OPENID1", "滞留内容A")
	l.Set("MEMBER9", "群滞留B")
	if l.Count() != 2 {
		t.Fatalf("Count 应 2,got %d", l.Count())
	}
	// 重启:新实例读盘
	l2 := newDeliveryLedger(path)
	if !l2.Pending("OPENID1") || !l2.Pending("MEMBER9") {
		t.Fatal("重启后滞留应恢复")
	}
	if got := l2.GetAndClear("OPENID1"); got != "滞留内容A" {
		t.Fatalf("取走不符: %q", got)
	}
	if l2.Pending("OPENID1") {
		t.Fatal("取走后应不再 pending")
	}
	// 取走同样落盘:再重启不含 OPENID1
	l3 := newDeliveryLedger(path)
	if l3.Pending("OPENID1") || !l3.Pending("MEMBER9") {
		t.Fatalf("取走未持久化: OPENID1 pending=%v MEMBER9 pending=%v", l3.Pending("OPENID1"), l3.Pending("MEMBER9"))
	}
	// 文件权限 0600(密钥同级纪律)
	if fi, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Fatalf("落盘权限应 0600,got %v", fi.Mode().Perm())
	}
}

// TestLedgerEmpty 空/坏文件启动不 panic(空表)。
func TestLedgerEmpty(t *testing.T) {
	if l := newDeliveryLedger(filepath.Join(t.TempDir(), "no.yaml")); l.Count() != 0 {
		t.Fatal("无文件应空表")
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	_ = os.WriteFile(bad, []byte(": not: [yaml"), 0o600)
	if l := newDeliveryLedger(bad); l.Count() != 0 {
		t.Fatal("坏文件应空表")
	}
	// 空 path = 仅内存(不落盘不报错)
	l := newDeliveryLedger("")
	l.Set("k", "v")
	if !l.Pending("k") {
		t.Fatal("内存模式应可滞留")
	}
}
