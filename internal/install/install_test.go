package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/embed"
)

// makeFixture 构造本地 git 插件仓库(桥协议 demo 插件);返回仓库路径。
func makeFixture(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"), "module demo-tool\n\ngo 1.27\n\nrequire github.com/nekoleamo/go-agent-harness v0.0.0\n\nreplace github.com/nekoleamo/go-agent-harness => "+repoRoot(t))
	writeFile(t, filepath.Join(repo, "plugin.yaml"), "id: demo\nprotocol: bridge\nbinary: tool-demo\n")
	// 桥协议实现:直接复用仓库示例插件源码(握手 + echo 工具)
	example, err := os.ReadFile(filepath.Join(repoRoot(t), "extplugins", "tool-echo", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repo, "main.go"), string(example))
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "t@t")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "init")
	return repo
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v(%s)", args, err, out)
	}
}

// makeHome 构造带 seed 样板的临时 home。
func makeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if _, err := embed.EnsureSeed(home); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestInstallBridge 桥插件全流程:拉取→构建→落目录→登记→profile 装配→卸载。
func TestInstallBridge(t *testing.T) {
	repo := makeFixture(t)
	home := makeHome(t)

	res, err := Install(repo, home)
	if err != nil {
		t.Fatalf("Install 失败: %v", err)
	}
	if res.ID != "demo" || res.Protocol != "bridge" {
		t.Fatalf("结果不符: %+v", res)
	}
	// 产物在 home/plugins/demo/tool-demo
	bin := filepath.Join(home, "plugins", "demo", "tool-demo")
	if fi, err := os.Stat(bin); err != nil || fi.Size() == 0 {
		t.Fatalf("产物缺失: %v %v", bin, err)
	}
	// manifest 副本
	if !fileExists(filepath.Join(home, "plugins", "demo", "plugin.yaml")) {
		t.Fatal("manifest 副本缺失")
	}
	// 登记 patch:host-bridge enabled + dir 指向 home/plugins
	patch := filepath.Join(home, "config", PatchFile)
	b, err := os.ReadFile(patch)
	if err != nil {
		t.Fatalf("patch 未生成: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "host-bridge") || !strings.Contains(s, "plugins") {
		t.Fatalf("patch 应登记 host-bridge: %s", s)
	}
	// profile 装配(幂等)
	for _, pf := range []string{"profile-tui.yaml", "profile-headless.yaml"} {
		pb, err := os.ReadFile(filepath.Join(home, "config", pf))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(pb), PatchFile) {
			t.Fatalf("%s 应引用安装 patch: %s", pf, pb)
		}
	}
	if err := EnsureProfilePicks(home); err != nil {
		t.Fatal(err)
	}
	pb, _ := os.ReadFile(filepath.Join(home, "config", "profile-tui.yaml"))
	if strings.Count(string(pb), PatchFile) != 1 {
		t.Fatalf("profile 引用应幂等(仅一次): %s", pb)
	}
	// List 可见
	items := List(home)
	if len(items) != 1 || items[0].ID != "demo" {
		t.Fatalf("List 不符: %+v", items)
	}
	// 卸载:目录删除
	if err := Uninstall("demo", home); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(home, "plugins", "demo")) {
		t.Fatal("卸载后目录应删除")
	}
	if err := Uninstall("demo", home); err == nil {
		t.Fatal("重复卸载应报错")
	}
}

// TestInstallMCP mcp 插件:不拉取,直接登记 mcp-bridge 配置。
func TestInstallMCP(t *testing.T) {
	home := makeHome(t)
	res, err := Install("mcp:weather:npx -y @example/weather", home)
	if err != nil {
		t.Fatal(err)
	}
	if res.Protocol != "mcp" {
		t.Fatalf("协议应为 mcp: %+v", res)
	}
	b, err := os.ReadFile(filepath.Join(home, "config", PatchFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "mcp-bridge") || !strings.Contains(string(b), "npx -y @example/weather") {
		t.Fatalf("patch 应登记 mcp-bridge 命令: %s", b)
	}
	// 非法格式报错
	if _, err := Install("mcp:bad-no-command", home); err == nil {
		t.Fatal("非法 mcp 格式应报错")
	}
}

// TestInstallMissingManifest 仓库无 plugin.yaml → 报错。
func TestInstallMissingManifest(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"), "module no-manifest\n\ngo 1.27\n")
	writeFile(t, filepath.Join(repo, "x.go"), "package no-manifest\n")
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "t@t")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "init")
	if _, err := Install(repo, makeHome(t)); err == nil {
		t.Fatal("缺 plugin.yaml 应报错")
	}
}

// TestRuntimePatchPersist TUI 持久开关原语:写入/读取/清除/幂等。
func TestRuntimePatchPersist(t *testing.T) {
	home := makeHome(t)
	patch := RuntimePatch(home)

	// 写入两个条目(幂等合并)
	for i := 0; i < 2; i++ {
		if err := EnsurePatch(patch, Entry{ID: "host-jobs", Enabled: false}); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnsurePatch(patch, Entry{ID: "host-bridge", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	got := ReadEnablements(patch)
	if got["host-jobs"] != false || got["host-bridge"] != true {
		t.Fatalf("ReadEnablements 不符: %+v", got)
	}
	// profile 引用(幂等)
	if err := EnsureProfileRef(home, "patch-runtime.yaml"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureProfileRef(home, "patch-runtime.yaml"); err != nil {
		t.Fatal(err)
	}
	pb, _ := os.ReadFile(filepath.Join(home, "config", "profile-tui.yaml"))
	if strings.Count(string(pb), "patch-runtime.yaml") != 1 {
		t.Fatalf("profile 引用应幂等(仅一次): %s", pb)
	}
	// 清除一条(幂等)
	if err := RemoveEntry(patch, "host-jobs"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveEntry(patch, "host-jobs"); err != nil {
		t.Fatal(err)
	}
	got = ReadEnablements(patch)
	if _, ok := got["host-jobs"]; ok {
		t.Fatalf("清除后不应存在: %+v", got)
	}
	if got["host-bridge"] != true {
		t.Fatalf("其它条目应保留: %+v", got)
	}
	// 文件缺失:读空表、清除不报错
	if len(ReadEnablements(RuntimePatch(t.TempDir()))) != 0 {
		t.Fatal("缺失文件应返回空表")
	}
	if err := RemoveEntry(filepath.Join(t.TempDir(), "x.yaml"), "id"); err != nil {
		t.Fatal(err)
	}
}
