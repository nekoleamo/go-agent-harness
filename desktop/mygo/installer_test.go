// installer 单测:解包约束、安装器模式识别、换文件(rename 交换 + 回滚)。
package main

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeZip 造一个 zip(键 = 包内路径,值 = 内容)。
func makeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestExtractUpdateSingleTop 正常包:唯一顶层 .app,内层可执行权限位跟着包走。
func TestExtractUpdateSingleTop(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "a.zip")
	makeZip(t, zp, map[string]string{
		"gah.app/Contents/MacOS/gah-desktop": "binary",
		"gah.app/Contents/Info.plist":        "plist",
	})
	top, err := extractUpdate(zp, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(top) != "gah.app" {
		t.Fatalf("顶层条目应是 gah.app,得到 %s", top)
	}
	if _, err := os.Stat(filepath.Join(top, "Contents", "MacOS", "gah-desktop")); err != nil {
		t.Fatalf("内层文件应解出来: %v", err)
	}
}

// TestExtractUpdateRejectsMultipleTops 多顶层条目**必须拒绝**:
// 静默挑一个会把「更新成了别的东西」变成事实。
func TestExtractUpdateRejectsMultipleTops(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "b.zip")
	makeZip(t, zp, map[string]string{"gah.app/x": "1", "README.txt": "2"})
	if _, err := extractUpdate(zp, dir); err == nil {
		t.Fatal("多顶层条目应报错")
	}
}

// TestExtractUpdateRejectsTraversal 带 ../ 的路径必须拒绝(解包不能写出目录)。
func TestExtractUpdateRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "c.zip")
	makeZip(t, zp, map[string]string{"../evil.txt": "x"})
	if _, err := extractUpdate(zp, dir); err == nil {
		t.Fatal("路径穿越应报错")
	}
}

// TestExtractUpdateRejectsNonZip 只接受 zip(与发布链一致)。
func TestExtractUpdateRejectsNonZip(t *testing.T) {
	if _, err := extractUpdate("/tmp/x.tar.gz", t.TempDir()); err == nil {
		t.Fatal("非 zip 应报错")
	}
}

// TestInstallerMode argv 识别(--install-update <plan> 与 = 形式)。
func TestInstallerMode(t *testing.T) {
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = []string{"gah-desktop"}
	if _, ok := installerMode(); ok {
		t.Fatal("默认不是安装器")
	}
	os.Args = []string{"gah-desktop", installFlag, "/tmp/plan.json"}
	if p, ok := installerMode(); !ok || p != "/tmp/plan.json" {
		t.Fatalf("应识别空格形式,得到 %q %v", p, ok)
	}
	os.Args = []string{"gah-desktop", installFlag + "=/tmp/plan2.json"}
	if p, ok := installerMode(); !ok || p != "/tmp/plan2.json" {
		t.Fatalf("应识别等号形式,得到 %q %v", p, ok)
	}
}

// TestInstallerMainSwapsTarget 换文件主流程:旧目标移到 .old-*、新目标就位。
// WaitPID=0 表示"没有要等的进程"(unix 上 processAlive(0) 为假)。
func TestInstallerMainSwapsTarget(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "gah-desktop")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "payload", "gah-desktop")
	if err := os.MkdirAll(filepath.Dir(payload), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payload, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	planDir := t.TempDir()
	planPath := filepath.Join(planDir, "install.json")
	writePlan(t, planPath, installPlan{WaitPID: 0, Target: target, PayloadTop: payload, Relaunch: false})

	if err := installerMain(planPath); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("目标应被换成新内容,得到 %q", got)
	}
	ents, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "gah-desktop.old-") {
			t.Fatalf("旧版本应被清理,残留 %s", e.Name())
		}
	}
}

// TestInstallerMainRollsBack 放新目标失败时必须把旧目标搬回来(用户还得有个能启动的程序)。
func TestInstallerMainRollsBack(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "gah-desktop")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	planDir := t.TempDir()
	planPath := filepath.Join(planDir, "install.json")
	// PayloadTop 指向不存在的路径 ⇒ rename 必失败 ⇒ 触发回滚。
	writePlan(t, planPath, installPlan{WaitPID: 0, Target: target, PayloadTop: filepath.Join(root, "nope")})

	if err := installerMain(planPath); err == nil {
		t.Fatal("放新目标失败应返回错误")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("回滚后旧目标必须还在: %v", err)
	}
	if string(got) != "old" {
		t.Fatalf("回滚后应是旧内容,得到 %q", got)
	}
}

// writePlan 写一份安装计划(测试夹具)。
func writePlan(t *testing.T, path string, plan installPlan) {
	t.Helper()
	b, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSafeArtifactName 清单文件名必须是安全的本地名(不能带路径/怪字符)。
func TestSafeArtifactName(t *testing.T) {
	cases := map[string]string{
		"gah-0.9.2-aarch64-apple-darwin.app.zip": "gah-0.9.2-aarch64-apple-darwin.app.zip",
		"../../etc/passwd":                       "passwd",
		"":                                       "artifact.zip",
		"weird name *?.zip":                      "weirdname.zip",
	}
	for in, want := range cases {
		if got := safeArtifactName(in); got != want {
			t.Fatalf("safeArtifactName(%q)=%q, want %q", in, got, want)
		}
	}
}
