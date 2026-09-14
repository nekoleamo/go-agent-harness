// pathpolicy_win_test.go:Windows 路径大小写折叠用例(平台变量覆盖,便于在
// macOS/Linux 上验证;真机行为仍需 Windows 复核)。
package policyguard

import "testing"

// withCaseFold 临时切换路径大小写折叠开关(测试结束自动还原)。
func withCaseFold(t *testing.T, on bool) {
	t.Helper()
	old := pathCaseFold
	pathCaseFold = on
	t.Cleanup(func() { pathCaseFold = old })
}

// TestPathWithinCaseFold Windows 上路径比较大小写不敏感(盘符与段名),
// 否则 D:\Repo 与 d:\repo\sub 互为「根之外」而误拒。
func TestPathWithinCaseFold(t *testing.T) {
	withCaseFold(t, true)
	if !pathWithin("/a/B", "/a/b/c") {
		t.Fatal("大小写不敏感平台应判为在根内")
	}
	if !pathWithin("/a/B/C", "/a/b/c") {
		t.Fatal("根自身(大小写不同)应判为在根内")
	}
	if pathWithin("/a/B", "/a/bb/c") {
		t.Fatal("前缀段不完整不应误判(/a/bb 不是 /a/b 的子路径)")
	}
	withCaseFold(t, false)
	if pathWithin("/a/B", "/a/b/c") {
		t.Fatal("大小写敏感平台保持原语义(不得放松)")
	}
}
