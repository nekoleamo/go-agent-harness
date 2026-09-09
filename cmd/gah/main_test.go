// P5.7 便携数据根自动创建测试:同级 gah-data 不存在 → 新建;已存在 → 复用;
// 二进制目录只读(创建失败)→ 回落空(调用方报错;数据根唯一 = 便携 gah-data)。
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPortableRootCreatesWhenMissing(t *testing.T) {
	base := t.TempDir()
	exe := filepath.Join(base, "bin", "gah")
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	// 同级 gah-data 不存在 → 自动新建并返回
	got := portableRoot(exe)
	want := filepath.Join(base, "bin", "gah-data")
	if got != want {
		t.Fatalf("portableRoot = %q,want %q", got, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Fatalf("gah-data 应已自动创建: %v", err)
	}
}

func TestPortableRootReusesExisting(t *testing.T) {
	base := t.TempDir()
	exe := filepath.Join(base, "gah")
	want := filepath.Join(base, "gah-data")
	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := portableRoot(exe); got != want {
		t.Fatalf("已存在应直接复用: %q", got)
	}
}

// TestHomeDirIgnoresEnv 2026-09-16 收紧:GAH_HOME env 不再作为输入源,
// homeDir 只返回二进制同级便携根(不存在则自动新建),绝不返回 env 指定路径。
func TestHomeDirIgnoresEnv(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe = execRealPath(exe) // 归一(mac /var→/private/var)后作为期望数据根父目录
	expected := filepath.Join(filepath.Dir(filepath.Clean(exe)), "gah-data")
	t.Setenv("GAH_HOME", t.TempDir()) // 任意非便携路径
	got := homeDir()
	if got != expected {
		t.Fatalf("homeDir 应忽略 GAH_HOME env 并返回便携根 %q,得 %q", expected, got)
	}
	defer os.RemoveAll(expected) // 清理测试二进制旁自动创建的 gah-data
}

func TestPortableRootReadonlyDir(t *testing.T) {
	base := t.TempDir()
	binDir := filepath.Join(base, "ro-bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "gah")
	if err := os.Chmod(binDir, 0o500); err != nil { // 只读:无法在其下建 gah-data
		t.Fatal(err)
	}
	defer os.Chmod(binDir, 0o700) // 还原供 TempDir 清理
	if got := portableRoot(exe); got != "" {
		t.Fatalf("只读目录应回落空(不可便携): %q", got)
	}
}

// TestExecRealPath 符号链接启动时 os.Executable 返回入口链接路径;execRealPath
// 应归一到真实二进制(数据根随真实目录,pi 式 symlink 全局安装的正确性基础)。
func TestExecRealPath(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real-gah")
	if err := os.WriteFile(target, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link-gah")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink 不可用: %v", err)
	}
	realTarget, _ := filepath.EvalSymlinks(target) // mac 归一到 /private/var 前缀
	if got := execRealPath(link); got != realTarget {
		t.Fatalf("execRealPath(%q) = %q,want 真实 %q", link, got, realTarget)
	}
	// 非链接路径:与 EvalSymlinks 自身结果一致(原样或前缀归一)
	if got := execRealPath(target); got != realTarget {
		t.Fatalf("execRealPath 非链接路径应仅前缀归一: %q", got)
	}
}
