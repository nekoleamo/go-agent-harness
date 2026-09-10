// delivery ledger 单测:滞留落盘重启恢复/取走删除/放回。
package uimqq

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLedgerRoundtrip Stash → 新实例(同路径=重启)恢复 → Take 清空并落盘。
func TestLedgerRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.yaml")
	l := newDeliveryLedger(path)
	l.Stash("OPENID1", "滞留内容A")
	l.Stash("MEMBER9", "群滞留B")
	if l.Count() != 2 {
		t.Fatalf("Count 应 2,got %d", l.Count())
	}
	// 重启:新实例读盘
	l2 := newDeliveryLedger(path)
	if !l2.Pending("OPENID1") || !l2.Pending("MEMBER9") {
		t.Fatal("重启后滞留应恢复")
	}
	if got, att := l2.Take("OPENID1"); got != "滞留内容A" || att != 1 {
		t.Fatalf("取走不符: %q attempts=%d", got, att)
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
	l.Stash("k", "v")
	if !l.Pending("k") {
		t.Fatal("内存模式应可滞留")
	}
}

// TestLedgerAttempts Stash 递增尝试次数;Take 返回原文与次数(P2 二期重投标记依据)。
func TestLedgerAttempts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.yaml")
	l := newDeliveryLedger(path)
	// 首次滞留(第 1 次投递失败)
	l.Stash("OPENID1", "内容")
	if got, att := l.Take("OPENID1"); got != "内容" || att != 1 {
		t.Fatalf("首次滞留 attempts 应 1: %q %d", got, att)
	}
	// 模拟:补发失败两次(放回原文)→ attempts 累计
	l.Stash("OPENID1", "内容")
	l.Stash("OPENID1", "内容")
	if got, att := l.Take("OPENID1"); got != "内容" || att != 2 {
		t.Fatalf("两次失败 attempts 应 2: %q %d", got, att)
	}
	// 取走后清空,再滞留重新从 1 计
	l.Stash("OPENID1", "新内容")
	// 重启(新实例读盘):计数应持久
	if got, att := newDeliveryLedger(path).Take("OPENID1"); got != "新内容" || att != 1 {
		t.Fatalf("重启后 attempts 应持久且清空后重置: %q %d", got, att)
	}
}

// TestLedgerLegacyFormat 一期旧格式(entries: {chat: "纯文本"})可无损加载。
func TestLedgerLegacyFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.yaml")
	legacy := "entries:\n  \"qq\\x00OPENID1\": \"旧格式滞留内容\"\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	l := newDeliveryLedger(path)
	got, att := l.Take("qq\x00OPENID1")
	if got != "旧格式滞留内容" || att != 0 {
		t.Fatalf("旧格式应兼容加载(文本保留、尝试次数 0): %q attempts=%d", got, att)
	}
}
