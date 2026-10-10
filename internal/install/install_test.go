package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/embed"
	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
	"github.com/nekoleamo/go-agent-harness/internal/testutil"
)

// makeFixture 构造本地 git 插件仓库(桥协议 demo 插件);返回仓库路径。
func makeFixture(t *testing.T) string { return makeFixtureVer(t, "") }

// makeFixtureVer 同 makeFixture,额外往 plugin.yaml 写 api_version(空 = 不写该键)。
func makeFixtureVer(t *testing.T, apiVersion string) string {
	t.Helper()
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"), "module demo-tool\n\ngo 1.27\n\nrequire github.com/nekoleamo/go-agent-harness v0.0.0\n\nrequire github.com/nekoleamo/go-agent-harness/sdk v0.0.0\n\nreplace github.com/nekoleamo/go-agent-harness => "+repoRoot(t)+"\n\nreplace github.com/nekoleamo/go-agent-harness/sdk => "+filepath.Join(repoRoot(t), "sdk"))
	manifest := "id: demo\nprotocol: bridge\nbinary: tool-demo\n"
	if apiVersion != "" {
		manifest += "api_version: " + apiVersion + "\n"
	}
	writeFile(t, filepath.Join(repo, "plugin.yaml"), manifest)
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
	// 产物在 home/plugins/demo/tool-demo[.exe](Windows 补 .exe,见 sdk.BinaryName)
	bin := filepath.Join(home, "plugins", "demo", testutil.ExeName("tool-demo"))
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
	if err := Uninstall("demo", home, nil); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(home, "plugins", "demo")) {
		t.Fatal("卸载后目录应删除")
	}
	if err := Uninstall("demo", home, nil); err == nil {
		t.Fatal("重复卸载应报错")
	}
}

// TestInstallMultiTool 多工具插件(P2a):一个二进制承载多工具(桥协议枚举),装后产物即多工具入口。
func TestInstallMultiTool(t *testing.T) {
	repo := t.TempDir()
	writeFile(t, filepath.Join(repo, "go.mod"), "module multi-tool\n\ngo 1.27\n\nrequire github.com/nekoleamo/go-agent-harness v0.0.0\n\nrequire github.com/nekoleamo/go-agent-harness/sdk v0.0.0\n\nreplace github.com/nekoleamo/go-agent-harness => "+repoRoot(t)+"\n\nreplace github.com/nekoleamo/go-agent-harness/sdk => "+filepath.Join(repoRoot(t), "sdk"))
	writeFile(t, filepath.Join(repo, "plugin.yaml"), "id: multi\nprotocol: bridge\nbinary: tool-multi\n")
	writeFile(t, filepath.Join(repo, "main.go"), `package main

import (
	"context"

	bridge "github.com/nekoleamo/go-agent-harness/plugins/host/host-bridge"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

type hiTool struct{}

func (hiTool) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "hi", Description: "多工具测试:打招呼"}
}
func (hiTool) Execute(_ context.Context, args string) (any, error) {
	return map[string]any{"hi": "ok"}, nil
}

func main() {
	bridge.ServeTools(map[string]sdk.Tool{
		"hi":  hiTool{},
		"bye": hiTool{},
	})
}
`)
	runGit(t, repo, "init", "-q")
	runGit(t, repo, "config", "user.email", "t@t")
	runGit(t, repo, "config", "user.name", "t")
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-qm", "init")
	home := makeHome(t)
	res, err := Install(repo, home)
	if err != nil {
		t.Fatalf("多工具插件安装失败: %v", err)
	}
	if res.ID != "multi" {
		t.Fatalf("id 不符: %+v", res)
	}
	// 桥协议枚举多工具的入口产物存在即可(注册面由 host-bridge 枚举测试覆盖)
	bin := filepath.Join(home, "plugins", "multi", testutil.ExeName("tool-multi"))
	if fi, err := os.Stat(bin); err != nil || fi.Size() == 0 {
		t.Fatalf("产物缺失: %v %v", bin, err)
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

// —— 插件协议兼容闸 + 白名单登记(2026-10-03)——

// TestCheckAPIVersion 缺省放行(兼容既有插件);声明与宿主范围有交集则放行;没交集 ⇒ 显式拒绝。
func TestCheckAPIVersion(t *testing.T) {
	if err := checkAPIVersion(Manifest{}); err != nil {
		t.Fatalf("缺省(=v1)应放行,不能因为加了一个字段就让已发布插件全被拒: %v", err)
	}
	if err := checkAPIVersion(Manifest{APIVersion: StringList{"v1"}}); err != nil {
		t.Fatalf("v1 应放行: %v", err)
	}
	// 裸数字与 v 形式等价(旧写法兼容)。
	if err := checkAPIVersion(Manifest{APIVersion: StringList{"1"}}); err != nil {
		t.Fatalf("裸数字 1 应放行: %v", err)
	}
	// 兼容范围:声明 [v1,v2] 与宿主 {v1} 有交集 ⇒ 放行。
	if err := checkAPIVersion(Manifest{APIVersion: StringList{"v1", "v2"}}); err != nil {
		t.Fatalf("声明含 v1 应放行: %v", err)
	}
	err := checkAPIVersion(Manifest{APIVersion: StringList{"v99"}})
	if err == nil {
		t.Fatal("不认识的版本必须显式拒绝")
	}
	if !strings.Contains(err.Error(), strings.Join(PluginAPIVersionSupported, ", ")) {
		t.Fatalf("错误文案应说清本版支持什么: %v", err)
	}
	// 只声明未来版本 ⇒ 拒绝(不能因为"将来会支持"就放行)。
	if err := checkAPIVersion(Manifest{APIVersion: StringList{"v2"}}); err == nil {
		t.Fatal("只声明宿主不认识的版本必须拒绝")
	}
}

// TestManifestAPIVersionShapes `api_version: v1` 与 `api_version: [v1,v2]` 两种写法等价。
//
// 不做这件事的后果:作者写列表而宿主只认单值时,yaml.v3 会把整条声明**静默忽略** ——
// 那等于「作者声明了版本,宿主当没看见」,正是这个字段存在的目的的反面。
func TestManifestAPIVersionShapes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "plugin.yaml"), "id: x\napi_version: v1\n")
	if got := readManifest(dir).APIVersion.Strings(); len(got) != 1 || got[0] != "v1" {
		t.Errorf("单值写法应得 [v1],得 %v", got)
	}
	writeFile(t, filepath.Join(dir, "plugin.yaml"), "id: x\napi_version: [v1, v2]\n")
	if got := readManifest(dir).APIVersion.Strings(); len(got) != 2 {
		t.Errorf("列表写法应得 2 项,得 %v", got)
	}
	writeFile(t, filepath.Join(dir, "plugin.yaml"), "id: x\napi_version: []\n")
	if readManifest(dir).APIVersion.Any() {
		t.Errorf("空列表 = 未声明")
	}
}

// TestInstallRecordsWhitelist 装完必须登记白名单。
//
// 为何这条重要:白名单一存在即强制,而清单默认不存在(opt-in)。用户装了第一个插件之后
// 清单就出现了 —— 里面若没有刚装的这件,下次启动它会被**自己**拒掉,症状是「装完能用,
// 重启就没了」。
func TestInstallRecordsWhitelist(t *testing.T) {
	home := t.TempDir()
	res, err := Install(makeFixture(t), home)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	names, err := TrustedList(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range names {
		if n == res.Binary {
			found = true
		}
	}
	if !found {
		t.Fatalf("装完应登记 %s,白名单=%v", res.Binary, names)
	}
	// 登记的哈希必须与盘上那份一致(否则第一次加载就会被自己拒)
	sum, err := plugintrust.HashFile(filepath.Join(res.Dir, res.Binary))
	if err != nil {
		t.Fatal(err)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := list.Sum(res.Binary)
	if !ok || got != sum {
		t.Fatal("登记的哈希与盘上产物不一致")
	}
}

// TestInstallRejectsIncompatibleAPIVersion 兼容闸在**构建之前**生效。
func TestInstallRejectsIncompatibleAPIVersion(t *testing.T) {
	home := t.TempDir()
	_, err := Install(makeFixtureVer(t, "v99"), home)
	if err == nil {
		t.Fatal("不兼容的 api_version 应被拒")
	}
	if !strings.Contains(err.Error(), "api_version") {
		t.Fatalf("错误应点名 api_version: %v", err)
	}
	// 闸在构建前 ⇒ 不该留下任何产物
	if entries, _ := os.ReadDir(filepath.Join(home, "plugins")); len(entries) != 0 {
		t.Fatalf("被拒的安装不该留下产物: %v", entries)
	}
}

// TestInstallFromLocalDir 本地目录安装(2026-10-03)。
//
// 为什么这条路径必须独立于 git:自己写的插件**不该被迫先 git init + push** ——
// 那是纯仪式,而且会把源码推到某个远端去。
func TestInstallFromLocalDir(t *testing.T) {
	repo := makeFixture(t)         // 本地 git 仓库(造料用;安装本身不走 git)
	home := t.TempDir()            // 不带 seed:install 只需要 home/plugins
	dir := strings.TrimSpace(repo) // 直接把本地目录当来源

	res, err := Install(dir, home)
	if err != nil {
		t.Fatalf("从本地目录安装失败: %v", err)
	}
	if !res.Local {
		t.Fatal("Local 标志应为 true(面板与 /install 回显要用)")
	}
	if res.ID != "demo" || res.Binary != testutil.ExeName("tool-demo") {
		t.Fatalf("结果不符: %+v", res)
	}
	// 判据用 filepath.IsAbs 而不是 HasPrefix("/"):Windows 的绝对路径带盘符
	// (`C:\Users\...`),按 "/" 断言会在 Windows CI 上稳定红。
	if res.Source == "" || !filepath.IsAbs(res.Source) {
		t.Fatalf("本地来源应回显绝对路径: %q", res.Source)
	}
	if !fileExists(filepath.Join(res.Dir, res.Binary)) {
		t.Fatal("产物未落位")
	}
	// 白名单登记 + 审计来源标成 install:<本地路径>
	names, err := TrustedList(home)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range names {
		if n == res.Binary {
			found = true
		}
	}
	if !found {
		t.Fatalf("装完应登记白名单:%v", names)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	a, ok := list.LastAuditOf(res.Binary)
	if !ok || !strings.HasPrefix(a.Source, "install:") {
		t.Fatalf("审计来源应标出 install:<spec>,got %+v", a)
	}
}

// TestUninstallRemovesWhitelistEntry 卸载必须撤白名单条目。
//
// 留着幽灵条目的后果很具体:同名再装、构建产物哈希与上次不同时,会被**自己的旧条目**拒掉,
// 而错误文案说的是「文件可能已被改动」—— 与真实原因毫无关系。
func TestUninstallRemovesWhitelistEntry(t *testing.T) {
	home := t.TempDir()
	res, err := Install(makeFixture(t), home)
	if err != nil {
		t.Fatal(err)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := list.Sum(res.Binary); !ok {
		t.Fatal("装完应在白名单里")
	}
	if err := Uninstall(res.ID, home, nil); err != nil {
		t.Fatalf("卸载失败: %v", err)
	}
	list2, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := list2.Sum(res.Binary); ok {
		t.Fatal("卸载后白名单条目应一并撤销(否则同名再装会被自己的旧条目拒掉)")
	}
}
