// uninstall_test.go:批二 §2.7 —— **卸载的顺序是承重的**。
//
// 顺序错了的后果不是「多留一个空目录」,而是:用户删掉插件文件,运行中的进程却继续
// 持着工具注册与回调 token 直到 gah 重启 —— 而用户已经认为它没了。
// **「我删了它」给的是虚假的安全感。**
package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/testutil"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// fakeCtl 记录调用顺序的控制面替身。
type fakeCtl struct {
	calls []string
	paths map[string]string // 名字 → 路径
	fail  string            // 这个名字的 Disable 要报错
	// onDisable 在每次 Disable 前触发(用来断言「停用时文件还在」)。
	onDisable func(name string, binPath string)
}

func (f *fakeCtl) Reload(string) error { f.calls = append(f.calls, "reload"); return nil }
func (f *fakeCtl) List() []sdk.ExternalPluginInfo {
	out := make([]sdk.ExternalPluginInfo, 0, len(f.paths))
	for n, p := range f.paths {
		out = append(out, sdk.ExternalPluginInfo{Name: n, Path: p, Loaded: true})
	}
	return out
}
func (f *fakeCtl) Enable(string) error { f.calls = append(f.calls, "enable"); return nil }
func (f *fakeCtl) Disable(name string) error {
	if f.onDisable != nil {
		f.onDisable(name, f.paths[name])
	}
	f.calls = append(f.calls, "disable:"+name)
	if f.fail == name {
		return errors.New("进程停不下来")
	}
	return nil
}

// TestUninstallStopsProcessBeforeDeleting 先停进程,再删文件。
func TestUninstallStopsProcessBeforeDeleting(t *testing.T) {
	repo := makeFixture(t)
	home := makeHome(t)
	if _, err := Install(repo, home); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "plugins", "demo")
	bin := filepath.Join(dir, testutil.ExeName("tool-demo"))

	// 在「文件还在的时候」调 Disable —— 停用必须发生在文件被删之前。
	ctl := &fakeCtl{paths: map[string]string{"tool-demo": bin}}
	ctl.onDisable = func(_ string, binPath string) {
		if _, err := os.Stat(binPath); err != nil {
			t.Errorf("停用时文件已被删(顺序反了): %v", err)
		}
	}
	if err := Uninstall("demo", home, ctl); err != nil {
		t.Fatalf("卸载应成功: %v", err)
	}
	if len(ctl.calls) != 1 || ctl.calls[0] != "disable:tool-demo" {
		t.Fatalf("调用的应是 Disable(tool-demo),得 %v", ctl.calls)
	}
	if fileExists(dir) {
		t.Error("卸载后目录应删除")
	}
}

// TestUninstallNilControlPlane ctl 可为 nil(CLI 场景:gah 没在跑,没有活进程可停)。
func TestUninstallNilControlPlane(t *testing.T) {
	repo := makeFixture(t)
	home := makeHome(t)
	if _, err := Install(repo, home); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall("demo", home, nil); err != nil {
		t.Fatalf("ctl 为 nil 应正常卸载: %v", err)
	}
	if fileExists(filepath.Join(home, "plugins", "demo")) {
		t.Error("目录应删除")
	}
}

// TestUninstallAbortsWhenCannotStop 停不掉 ⇒ **中止,不删文件**。
//
// 报错让人重试,好过删完再告诉用户停不掉:那时文件已经没了,用户没有可重试的对象。
func TestUninstallAbortsWhenCannotStop(t *testing.T) {
	repo := makeFixture(t)
	home := makeHome(t)
	if _, err := Install(repo, home); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "plugins", "demo")
	bin := filepath.Join(dir, "tool-demo")

	ctl := &fakeCtl{paths: map[string]string{"tool-demo": bin}, fail: "tool-demo"}
	err := Uninstall("demo", home, ctl)
	if err == nil {
		t.Fatal("停不掉时卸载应报错")
	}
	if !strings.Contains(err.Error(), "中止卸载") || !strings.Contains(err.Error(), "文件未删") {
		t.Errorf("文案要说清「已中止」与「文件没删」: %v", err)
	}
	if !fileExists(dir) {
		t.Error("中止时不该删文件(用户没有可重试的对象了)")
	}
}

// TestUninstallSkipsOtherPlugins 只停**这个插件目录下面**的二进制。
//
// 不判包含关系的后果很具体:停掉别的插件(用户会莫名其妙少几个工具),
// 或者反过来一个都没停到(虚假的安全感又回来了)。
func TestUninstallSkipsOtherPlugins(t *testing.T) {
	repo := makeFixture(t)
	home := makeHome(t)
	if _, err := Install(repo, home); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "plugins", "demo")
	other := filepath.Join(home, "plugins", "other", "tool-other")

	ctl := &fakeCtl{paths: map[string]string{
		"tool-demo":  filepath.Join(dir, "tool-demo"),
		"tool-other": other,
	}}
	if err := Uninstall("demo", home, ctl); err != nil {
		t.Fatal(err)
	}
	if len(ctl.calls) != 1 || ctl.calls[0] != "disable:tool-demo" {
		t.Fatalf("只应停本插件的进程,得 %v", ctl.calls)
	}
}

// TestUnderDir 包含关系判定(含边界:前缀相同但不同目录)。
func TestUnderDir(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{"/a/plugins/demo/tool-demo", "/a/plugins/demo", true},
		{"/a/plugins/demo", "/a/plugins/demo", true},
		{"/a/plugins/other/tool-x", "/a/plugins/demo", false},
		{"/a/plugins/demo2/tool-x", "/a/plugins/demo", false}, // 前缀相同但不是子目录
		{"relative/tool-x", "relative", true},
	}
	for _, c := range cases {
		if got := underDir(c.path, c.dir); got != c.want {
			t.Errorf("underDir(%q,%q) = %v, want %v", c.path, c.dir, got, c.want)
		}
	}
}
