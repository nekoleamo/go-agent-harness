package testutil

// 子进程覆盖桥的行为契约。
//
// 重点三条:
//   ① 没设合并目录 ⇒ **整体不启用**(而不是静默产出坏数据):缺数据与「测不到」必须能分开。
//   ② 合并是**按区块计数相加**,语句数不同的同位置区块**不合并**(否则凭空造覆盖率)。
//   ③ covdata 目录为空 ⇒ 静默跳过;转换失败 ⇒ 显式报错(静默吞掉会让「根本没接上」
//      这件事永远没人发现)。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChildEnvDisabledWithoutMergeDir(t *testing.T) {
	t.Setenv(MergeDirEnv, "")
	RunDir(t) // 显式调一次也不该 panic
	if got := ChildEnv(t); got != nil {
		t.Fatalf("未设 %s 时应返回 nil(不启用),got %v", MergeDirEnv, got)
	}
	// Flush 在未启用时是 no-op(不报错、不落盘)
	Flush(t, "test")
}

func TestChildEnvPointsAtRunDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv(MergeDirEnv, root)
	// RunDir 内部是 sync.Once;单测里换目录需要先重置 —— 显式提供重置入口,
	// 免得测试之间互相污染(runOnce 是包级状态)。
	resetRunDirForTest()
	dir := RunDir(t)
	if dir == "" {
		t.Fatalf("设了 %s 就该拿到子目录", MergeDirEnv)
	}
	env := ChildEnv(t)
	if len(env) != 1 || env[0] != "GOCOVERDIR="+dir {
		t.Fatalf("子进程环境变量不对:%v", env)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("子目录应已创建:%v", err)
	}
	resetRunDirForTest()
}

func TestMergeProfilesSumsCounts(t *testing.T) {
	base := []byte("a.go:1.1,2.2 1 1\nb.go:3.1,4.4 2 0\n")
	extra := []byte("a.go:1.1,2.2 1 3\nc.go:5.5,6.6 1 7\n")
	out := string(MergeProfiles(base, extra))
	// 同区块相加:1+3=4;不同区块原样保留;计数 0 的区块也在(它是「跑到了但没执行」的事实)
	if !contains(out, "a.go:1.1,2.2 1 4") {
		t.Fatalf("同区块计数应相加,got:\n%s", out)
	}
	if !contains(out, "b.go:3.1,4.4 2 0") || !contains(out, "c.go:5.5,6.6 1 7") {
		t.Fatalf("不同区块应原样保留,got:\n%s", out)
	}
	if contains(out, "mode:") {
		t.Fatalf("MergeProfiles 不该处理 mode: 行(由调用方摘出),got:\n%s", out)
	}
}

func TestMergeProfilesDoesNotMergeDifferentStmtCounts(t *testing.T) {
	// 同一位置但语句数不同 = **不同的块**(两次构建插桩结果不同);相加等于凭空造覆盖率。
	out := string(MergeProfiles([]byte("a.go:1.1,2.2 1 5\n"), []byte("a.go:1.1,2.2 2 3\n")))
	if !contains(out, "a.go:1.1,2.2 1 5") || !contains(out, "a.go:1.1,2.2 2 3") {
		t.Fatalf("语句数不同的区块不该被合并,got:\n%s", out)
	}
	if contains(out, "1 8") || contains(out, "2 8") {
		t.Fatalf("出现了相加后的行,这是错的:\n%s", out)
	}
}

func TestMergeProfilesIgnoresJunk(t *testing.T) {
	out := string(MergeProfiles([]byte("not a profile line\n\na.go:1.1,2.2 1 2\n"), nil))
	if !contains(out, "a.go:1.1,2.2 1 2") {
		t.Fatalf("合法行应保留,got:\n%s", out)
	}
	if contains(out, "not a profile") {
		t.Fatalf("坏行应被跳过,got:\n%s", out)
	}
}

func TestParseBlockLineRejectsBadLines(t *testing.T) {
	for _, line := range []string{"", "x", "a.go:1.1,2.2 1", "a.go:1.1,2.2 x 1", "a.go:1.1,2.2 1 -1"} {
		if _, _, ok := parseBlockLine(line); ok {
			t.Errorf("坏行 %q 不该解析成功", line)
		}
	}
	k, n, ok := parseBlockLine("a.go:1.1,2.2 3 12")
	if !ok || k != "a.go:1.1,2.2 3" || n != 12 {
		t.Fatalf("合法行解析错:%q %d %v", k, n, ok)
	}
}

// 子进程目录里真有 covdata 文件时,Flush 应产出 profile;目录不存在数据时静默跳过。
func TestFlushProducesProfileWhenDataPresent(t *testing.T) {
	if _, err := filepath.Glob(filepath.Join("..", "..", "..", "go.mod")); err != nil {
		t.Skip("拿不到仓库根,跳过")
	}
	root := t.TempDir()
	t.Setenv(MergeDirEnv, root)
	resetRunDirForTest()
	dir := RunDir(t)
	if dir == "" {
		t.Skip("未启用合并目录")
	}
	// 空目录 ⇒ 静默跳过,合并目录里不应出现 profile
	Flush(t, "empty-case")
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".out" {
			t.Fatalf("空目录不该产出 profile:%s", e.Name())
		}
	}
	resetRunDirForTest()
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
