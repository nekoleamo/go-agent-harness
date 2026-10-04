// copydir_test.go:批六 §6.2 —— `copyDir` 的两个缺陷。
//
// 这一条路径是**自己开发插件**的便利路径(本地目录直接当 spec),不是第三方故事,
// 所以两个缺陷的实际影响面比「安全洞」小得多 —— 但其中一个是**真的信息泄漏**。
package install

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCopyDirRejectsSymlinks 目录里含符号链接 ⇒ **拒绝复制**,并说清原因。
//
// 为什么是硬拒绝而不是「跳过」或「只记链接目标」:原实现用 filepath.Walk(基于 Lstat,
// 不跟随)收集文件,却用 os.ReadFile(**跟随**)读 —— 一个指向 `~/.ssh/id_rsa` 的软链
// 会被**读出来写进临时克隆**,而那份克隆随后就是插件仓库:构建脚本完全可见,
// 产物里也可能带出去。指向**目录**的软链还能把整棵树复制进来。
//
// 不做「只记录链接目标」:那仍把链接暴露给构建脚本,还要额外定义"摘要算不算链接本身"
// 的判定 —— 复杂度换不来任何安全性。
func TestCopyDirRejectsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("符号链接在 Windows 上需要特权,且语义不同(该路径由开发者自验)")
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 指向一个"敏感"文件的软链
	secret := filepath.Join(t.TempDir(), "id_rsa")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(src, "innocent.txt")); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "clone")
	err := copyDir(src, dst)
	if err == nil {
		t.Fatal("含符号链接必须拒绝")
	}
	msg := err.Error()
	for _, want := range []string{"符号链接", "innocent.txt"} {
		if !strings.Contains(msg, want) {
			t.Errorf("报错应含 %q: %s", want, msg)
		}
	}
	// 关键:被指向的文件内容**不得**出现在复制产物里。
	if raw, rerr := os.ReadFile(filepath.Join(dst, "innocent.txt")); rerr == nil &&
		strings.Contains(string(raw), "PRIVATE KEY") {
		t.Fatal("被链接指向的内容被复制进来了 —— 那就是信息泄漏")
	}
}

// TestCopyDirPreservesExecBit 复制**保留可执行位**。
//
// 原实现一律写 0o644 ⇒ 本地插件用 `./build.sh` 构建会报 `Permission denied`,
// 而那个报错与真实原因(执行位在复制过程中被吃掉)毫无关系,排查成本极高。
func TestCopyDirPreservesExecBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 没有 POSIX 执行位(该路径由开发者自验)")
	}
	src := t.TempDir()
	script := filepath.Join(src, "build.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "plain.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "clone")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("复制应成功: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dst, "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("执行位丢了: %v —— 用 ./build.sh 构建会报 Permission denied", fi.Mode().Perm())
	}
	// 普通文件不该被无端加上执行位
	fp, err := os.Stat(filepath.Join(dst, "plain.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if fp.Mode().Perm()&0o111 != 0 {
		t.Errorf("普通文件不该有执行位: %v", fp.Mode().Perm())
	}
}

// TestBuildImplicitLabelled 未声明 `build:` ⇒ 装得上,但**三处都要标注**。
//
// 标注的必要性:用户看到一条自己从没写过的命令被执行,却以为是仓库的要求。
// C 口径(拒绝隐式构建)见 build.go 的 implicitBuildAllowed,当前**未启用**。
func TestBuildImplicitLabelled(t *testing.T) {
	repo := makeFixture(t) // 夹具的 plugin.yaml 不写 build:
	home := makeHome(t)
	res, err := Install(repo, home)
	if err != nil {
		t.Fatalf("A 口径下应照常装上: %v", err)
	}
	if !res.BuildImplicit {
		t.Error("Result 要标出 BuildImplicit,否则三个入口无从标注")
	}
	if !strings.Contains(res.BuildCmd, "未声明 build") {
		t.Errorf("回执命令应标注「未声明 build」: %q", res.BuildCmd)
	}
	// 确认文案也要说(Preview → ConfirmPrompt 是面板与 TUI 共用的那份)
	f := Preview(repo, home)
	if !f.BuildImplicit {
		t.Error("Preview 应把 BuildImplicit 填上")
	}
	p := ConfirmPrompt(f)
	if !strings.Contains(p, "没有声明 build") || !strings.Contains(p, "gah 替你选") {
		t.Errorf("确认文案要说明这条命令不是作者写的:\n%s", p)
	}
}

// TestBuildDeclaredNoImplicitWarning 声明了 `build:` ⇒ 不加那条标注(否则是噪音)。
func TestBuildDeclaredNoImplicitWarning(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "plugin.yaml"), "id: demo\nbuild: make -C .\n")
	f := Preview(dir, "")
	if f.BuildImplicit {
		t.Error("作者声明了 build: ⇒ 不该标为隐式")
	}
	if strings.Contains(ConfirmPrompt(f), "没有声明 build") {
		t.Error("显式声明时不该出现隐式构建的提醒")
	}
}

// TestListBinaryNameFallsBack CLI 清单那一栏不能是空的。
//
// Item.Binary 只来自 plugin.yaml,而**官方件与手工放置**的插件根本没写 manifest。
// web 与 `/install list` 早就改用回落(BinaryName 的注释写明了为什么),CLI 若不跟,
// 它就成了唯一一处「空值」无法区分「没有二进制」与「没 manifest」的面 —— 用户据此
// 做安全判断时看到的是同一个空白。
func TestListBinaryNameFallsBack(t *testing.T) {
	home := t.TempDir()
	// ① 有 manifest:用声明的名字
	d1 := filepath.Join(home, "plugins", "declared")
	if err := os.MkdirAll(d1, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d1, "plugin.yaml"), "id: declared\nbinary: tool-declared\n")
	writeFile(t, filepath.Join(d1, "tool-declared"), "x")
	// ② 没 manifest:扫目录回落
	d2 := filepath.Join(home, "plugins", "hand")
	if err := os.MkdirAll(d2, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d2, "tool-hand"), "x")
	// ③ 官方件布局:目录名 == 二进制名,也没有 manifest
	d3 := filepath.Join(home, "plugins", "tool-kit")
	if err := os.MkdirAll(d3, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d3, "tool-kit"), "x")

	got := map[string]string{}
	for _, it := range List(home) {
		got[it.ID] = ListBinaryName(it)
	}
	want := map[string]string{
		"declared": "tool-declared",
		"hand":     "tool-hand",
		"tool-kit": "tool-kit",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("ListBinaryName(%s) = %q, want %q", id, got[id], w)
		}
	}
}
