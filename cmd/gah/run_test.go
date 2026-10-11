// M0(为嵌入做准备的入口重构)的行为护栏:options 快照、退出码映射、数据根注入口。
// 拆分本身不改变 CLI 语义 —— 进程级用例见 cli_test.go;这里钉住的是**新引入的接缝**。
package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveHomePortableWhenNoOverride CLI 路径:opts.Home 为空 → 便携根(唯一根),true。
func TestResolveHomePortableWhenNoOverride(t *testing.T) {
	home, portable := resolveHome(&options{})
	want := homeDir()
	if home != want {
		t.Fatalf("未注入 Home 时应走便携根 %q,得 %q", want, home)
	}
	if !portable {
		t.Fatal("CLI 路径 portable 应为 true(调用方据此提示忽略 GAH_HOME env)")
	}
}

// TestResolveHomeEmbeddedOverride 嵌入形态(mobile/libgah):容器路径由宿主注入,
// **不受便携根约束**——.so 里 os.Executable 指向宿主 app_process,便携根既不可写也不该写。
func TestResolveHomeEmbeddedOverride(t *testing.T) {
	in := filepath.Join(t.TempDir(), "gah-data")
	home, portable := resolveHome(&options{Home: in})
	if home != in {
		t.Fatalf("注入的数据根应被采用 %q,得 %q", in, home)
	}
	if portable {
		t.Fatal("注入路径不是便携根(不该触发忽略 GAH_HOME env 的提示)")
	}
	// 纯空白 = 未注入(不能让出「注入」语义退回便携根的中间态)
	if h, p := resolveHome(&options{Home: "   "}); h != homeDir() || !p {
		t.Fatalf("空白 Home 应等同未注入: h=%q portable=%v", h, p)
	}
}

// TestExitErrCarriesCode 已自行输出的失败:错误里带退出码,调用方(errors.As)不得重复打印。
func TestExitErrCarriesCode(t *testing.T) {
	err := error(exit(1))
	var ee *exitErr
	if !errors.As(err, &ee) {
		t.Fatal("exit() 的错误应可 errors.As 取到 *exitErr")
	}
	if ee.code != 1 {
		t.Fatalf("退出码 = %d, want 1", ee.code)
	}
	// 普通错误不带退出码:main 走默认 1 分支
	var none *exitErr
	if errors.As(errors.New("x"), &none) {
		t.Fatal("普通错误不应被当成 exitErr")
	}
}

// TestRunVersionSucceedsWithoutProfile run 的纯查询分支:不经数据根、不装配插件。
func TestRunVersionSucceedsWithoutProfile(t *testing.T) {
	if err := run(&options{ShowVersion: true}); err != nil {
		t.Fatalf("-version 应返回 nil,得 %v", err)
	}
}

// TestManagementOnly 判据表:纯查询/管理动作(不需要终端)与需要 TUI 的动作要分得清。
// 这条判据决定 TUI 护栏跳不跳 —— 判错的方向是"把运维命令拦死"(用户最常见的用法)。
func TestManagementOnly(t *testing.T) {
	mgmt := []*options{
		{DumpConfig: true}, {ListPlugins: true}, {CheckUpdates: true}, {ListTrusted: true},
		{ListUIPlugins: true}, {Install: "repo"}, {Artifact: "http://x"}, {Uninstall: "id"},
		{InstallUI: "repo"}, {UninstallUI: "id"}, {TrustPlugin: "n"}, {UntrustPlugin: "n"},
		{TrustUIPlugin: "n"}, {UntrustUIPlugin: "n"},
	}
	for _, o := range mgmt {
		if !managementOnly(o) {
			t.Fatalf("纯查询/管理动作应被判为不需终端:%+v", o)
		}
	}
	// 这些仍然需要终端(要跑 UI):空 options、只有 profile/input/ephemeral
	for _, o := range []*options{{}, {Profile: "tui"}, {Ephemeral: true}, {Input: "hi"}, {ShowVersion: true}} {
		if managementOnly(o) {
			t.Fatalf("这些不该被判为纯查询(会绕过 TUI 护栏):%+v", o)
		}
	}
}

// TestCLIOptionsSnapshotsFlags cliOptions 读全局 flag 变量;未注册的指针也要能安全取零值
// (单测里可能直接置 nil),避免入口糖之后拿到垃圾值。
func TestCLIOptionsSnapshotsFlags(t *testing.T) {
	o := cliOptions()
	if o.Profile == "" || o.Input != "" || o.ShowVersion {
		t.Fatalf("默认快照应为空 profile + 无其它项: %+v", o)
	}
	// nil 指针安全:临时替换两个变量再还原
	savedProfile := profileFlag
	profileFlag = nil
	defer func() { profileFlag = savedProfile }()
	if got := cliOptions().Profile; got != "" {
		t.Fatalf("nil flag 指针应取空串,得 %q", got)
	}
}

// TestEphemeralDoesNotCreatePortableRoot -ephemeral 不该在二进制旁边留下 gah-data/。
//
// 这条是 review 逮到的:上一版先 `homeDir()`(它的语义就是"不存在则新建")再判 ephemeral,
// 于是 `gah -ephemeral -dump-config` 这种纯读调用会**白建一个空数据根** ——
// 对只读安装目录是直接失败,对可写目录是留下垃圾。
func TestEphemeralDoesNotCreatePortableRoot(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	expected := filepath.Join(filepath.Dir(filepath.Clean(execRealPath(exe))), "gah-data")
	// 用例前提:测试二进制旁**当前没有** gah-data(有就是别的用例留下的,先清掉)
	if _, err := os.Stat(expected); err == nil {
		if err := os.RemoveAll(expected); err != nil {
			t.Skipf("无法清理已存在的 %s(并发用例?):%v", expected, err)
		}
	}
	o := cliOptions()
	o.Ephemeral = true
	o.DumpConfig = true
	o.Profile = "web"
	if err := run(o); err != nil {
		t.Fatalf("ephemeral dump-config 应成功:%v", err)
	}
	if _, err := os.Stat(expected); err == nil {
		_ = os.RemoveAll(expected)
		t.Fatalf("ephemeral 运行不该创建便携数据根 %s", expected)
	}
}
