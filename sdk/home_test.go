package sdk

import (
	"path/filepath"
	"testing"
)

// TestUserHomes 家目录候选的单一事实源(2026-09-27 第二轮审计 F-A)。
//
// 为什么要钉:候选集是凭据拒绝表(CredentialDenyDirs)、内核层 RW 白名单(DefaultRWPaths)与
// 审批层目录表三处的共同输入。**只认一家**在 Windows 上会让拒绝面退化 —— 桌面壳启动的
// gah.exe 没有 HOME、只有 %USERPROFILE%,而子进程里的 Git Bash 照样把 `~` 解析到 USERPROFILE。
func TestUserHomes(t *testing.T) {
	a := filepath.Join(t.TempDir(), "home-a")
	b := filepath.Join(t.TempDir(), "home-b")

	// 并集:两家都在 → 按优先级 HOME 在前
	t.Setenv("HOME", a)
	t.Setenv("USERPROFILE", b)
	if got := UserHomes(); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("UserHomes() = %v, want [%s %s]", got, a, b)
	}
	if got := UserHome(); got != a {
		t.Fatalf("UserHome() = %q, want %q(首选 = HOME,与 shell 的 `~` 同源)", got, a)
	}

	// 两家相同 → 去重(否则拒绝表/白名单会塞重复项)
	t.Setenv("USERPROFILE", a)
	if got := UserHomes(); len(got) != 1 || got[0] != a {
		t.Fatalf("同名候选未去重: %v", got)
	}

	// 只有 USERPROFILE(Windows 桌面壳的常态)→ 仍要有候选,不能退化成“无家目录”
	t.Setenv("HOME", "")
	if got := UserHomes(); len(got) == 0 || got[0] != a {
		t.Fatalf("HOME 缺失时未回退到 USERPROFILE: %v", got)
	}

	// 两家都没有 → 空(调用方据此“不施加”,而不是误判某个目录)
	t.Setenv("USERPROFILE", "")
	if got := UserHomes(); len(got) != 0 {
		t.Fatalf("两家都缺时应为空,得到 %v", got)
	}
	if got := UserHome(); got != "" {
		t.Fatalf("无候选时 UserHome() 应为空,得到 %q", got)
	}
}
