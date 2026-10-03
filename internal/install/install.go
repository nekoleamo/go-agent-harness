// Package install 提供插件安装/卸载/清单(M6.6,gah -install/-uninstall/-list-plugins)。
// 流程:拉取(桥协议:git clone → 构建 → 落 ~/.gah/plugins/<id>/)→ 登记(enabled
// patch 幂等合并)→ 装配(home profile 引用 patch,装完即启用)。卸载:删目录
// (host-bridge watch 自动撤销工具)。MCP 插件:直接生成 mcp-bridge 配置条目。
// manifest(仓库根 plugin.yaml):{id, protocol: bridge|mcp, binary, build}。
package install

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
)

// PatchFile 安装登记的 patch 文件名(profile 引用)。
const PatchFile = "patch-installed.yaml"

// Manifest 插件仓库声明(plugin.yaml)。
type Manifest struct {
	ID       string `yaml:"id"`
	Protocol string `yaml:"protocol"` // bridge | mcp(仓库侧一般 bridge)
	Binary   string `yaml:"binary"`   // 桥插件产物文件名(须 tool- 前缀)
	Build    string `yaml:"build"`    // 构建命令(默认 go build -o <binary> .)
	// APIVersion 插件协议版本(2026-10-03 加)。**缺省 = 视为 v1**(兼容既有插件,
	// 不能因为加了一个字段就让已发布的插件全被拒);写了但认不出来 ⇒ 显式拒绝。
	//
	// 为什么需要它:插件是**常驻进程**,宿主升级后协议可能变(工具定义字段、回调通道、
	// 沙箱握手)。没有版本闸的表现是「装得上、起得来、跑到一半静默不对」——
	// 比装不上更难查。版本号让不兼容在安装那一刻就说清。
	APIVersion string `yaml:"api_version,omitempty"`
}

// PluginAPIVersion 当前宿主支持的插件协议版本(安装兼容闸的权威)。
const PluginAPIVersion = "v1"

// supportedAPIVersions 认识的版本 → 判否接受。
var supportedAPIVersions = map[string]bool{PluginAPIVersion: true, "1": true}

// checkAPIVersion 兼容闸:缺省放行(v1);认识则放行;不认识 → 显式报错并说清宿主支持什么。
func checkAPIVersion(man Manifest) error {
	v := strings.TrimSpace(man.APIVersion)
	if v == "" {
		return nil
	}
	if supportedAPIVersions[v] {
		return nil
	}
	return fmt.Errorf("install: plugin.yaml 声明 api_version=%s,本版 gah 只支持 %s"+
		"(插件协议变了:请用与本版 gah 匹配的插件版本,或升级 gah)", v, PluginAPIVersion)
}

// Result 安装结果摘要。
type Result struct {
	ID       string
	Protocol string
	Binary   string
	Dir      string
	Patch    string
}

// Install 安装插件:spec 形如 <repo>[@<version>](桥)或 mcp:<id>:<command>(MCP)。
func Install(spec, home string) (*Result, error) {
	if strings.HasPrefix(spec, "mcp:") {
		return installMCP(spec, home)
	}
	return installBridge(spec, home)
}

// installBridge git 拉取 + 构建 + 落目录 + 登记。
func installBridge(spec, home string) (*Result, error) {
	repo, version := spec, ""
	if i := strings.LastIndex(spec, "@"); i > 0 {
		repo, version = spec[:i], spec[i+1:]
	}
	tmp, err := os.MkdirTemp("", "gah-install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	clone := filepath.Join(tmp, "repo")
	args := []string{"clone", "--depth", "1"}
	if version != "" {
		args = append(args, "--branch", version)
	}
	args = append(args, repo, clone)
	if out, err := run("", "git", args...); err != nil {
		return nil, fmt.Errorf("install: 拉取 %s: %w(%s)", repo, err, out)
	}
	man := readManifest(clone)
	if man.ID == "" {
		return nil, fmt.Errorf("install: 仓库缺少 plugin.yaml(id 必填)")
	}
	// 兼容闸在**拉取之后、构建之前**:不该为一个注定装不上的版本烧一次 go build。
	if err := checkAPIVersion(man); err != nil {
		return nil, err
	}
	if man.Protocol != "" && man.Protocol != "bridge" {
		return nil, fmt.Errorf("install: 仓库声明 protocol=%s,仅支持 bridge", man.Protocol)
	}
	binary := man.Binary
	if binary == "" {
		binary = "tool-" + man.ID
	}
	if !strings.HasPrefix(binary, "tool-") {
		return nil, fmt.Errorf("install: 二进制名须 tool- 前缀(host-bridge 扫描约定): %s", binary)
	}
	buildCmd := man.Build
	if buildCmd == "" {
		// 先 tidy 补齐依赖(第三方 repo 的 go.mod 常不完整),再构建
		buildCmd = "go mod tidy && go build -o " + binary + " ."
	}
	if out, err := run(clone, "sh", "-c", buildCmd); err != nil {
		return nil, fmt.Errorf("install: 构建失败: %w(%s)", err, out)
	}
	src := filepath.Join(clone, binary)
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("install: 构建产物缺失 %s: %v", binary, err)
	}
	// 落 ~/.gah/plugins/<id>/
	dir := filepath.Join(home, "plugins", man.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := copyFile(src, filepath.Join(dir, binary)); err != nil {
		return nil, fmt.Errorf("install: 复制产物: %w", err)
	}
	if mf := filepath.Join(clone, "plugin.yaml"); fileExists(mf) {
		_ = copyFile(mf, filepath.Join(dir, "plugin.yaml"))
	}
	// 登记白名单(2026-10-03):白名单一存在即强制,装完不登记 ⇒ 下次启动被自己拒掉。
	// 登记的是**刚构建出来的这份**的哈希,不是发布方声明的值 —— 我们只能担保自己
	// 装进去的这份,担保不了「作者那一份本来就好」。
	sum, err := plugintrust.HashFile(filepath.Join(dir, binary))
	if err != nil {
		return nil, fmt.Errorf("install: 算产物哈希失败: %w", err)
	}
	list, err := plugintrust.Load(filepath.Join(home, "plugins"))
	if err != nil {
		return nil, err
	}
	if err := list.Record(binary, sum); err != nil {
		return nil, fmt.Errorf("install: 登记 plugins/%s 失败: %w", plugintrust.FileName, err)
	}
	// 登记:host-bridge 指向 home/plugins
	patch := filepath.Join(home, "config", PatchFile)
	if err := EnsurePatch(patch, Entry{
		ID: "host-bridge", Enabled: true,
		Data: map[string]any{"dir": filepath.Join(home, "plugins"), "watch": true},
	}); err != nil {
		return nil, err
	}
	// 装配:profile 引用 patch(幂等,装完即启用)
	if err := EnsureProfilePicks(home); err != nil {
		return nil, err
	}
	return &Result{ID: man.ID, Protocol: "bridge", Binary: binary, Dir: dir, Patch: patch}, nil
}

// installMCP 直接生成 mcp-bridge 配置条目(无需拉取)。
func installMCP(spec, home string) (*Result, error) {
	rest := strings.TrimPrefix(spec, "mcp:")
	i := strings.Index(rest, ":")
	if i <= 0 {
		return nil, fmt.Errorf("install: mcp 格式为 mcp:<id>:<command>,收到 %q", spec)
	}
	id, cmd := rest[:i], rest[i+1:]
	if strings.TrimSpace(cmd) == "" {
		return nil, fmt.Errorf("install: mcp 需要启动命令")
	}
	patch := filepath.Join(home, "config", PatchFile)
	if err := EnsurePatch(patch, Entry{
		ID: "mcp-bridge", Enabled: true,
		Data: map[string]any{"command": cmd},
	}); err != nil {
		return nil, err
	}
	if err := EnsureProfilePicks(home); err != nil {
		return nil, err
	}
	return &Result{ID: id, Protocol: "mcp", Patch: patch}, nil
}

// Uninstall 卸载:删除插件目录(host-bridge watch 自动撤销工具;patch 为共享条目保留)。
func Uninstall(id, home string) error {
	dir := filepath.Join(home, "plugins", id)
	if !fileExists(dir) {
		return fmt.Errorf("uninstall: 插件 %s 未安装(%s)", id, dir)
	}
	return os.RemoveAll(dir)
}

// Item 已安装插件条目。
type Item struct {
	ID       string
	Protocol string
	Binary   string
	Dir      string
	Manifest string
}

// List 列出已安装插件(home/plugins 下一层目录)。
func List(home string) []Item {
	var out []Item
	root := filepath.Join(home, "plugins")
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		item := Item{ID: e.Name(), Dir: dir}
		if mf := filepath.Join(dir, "plugin.yaml"); fileExists(mf) {
			item.Manifest = mf
			b, _ := os.ReadFile(mf)
			var m Manifest
			if yaml.Unmarshal(b, &m) == nil {
				item.Protocol = m.Protocol
				item.Binary = m.Binary
			}
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// readManifest 读仓库 plugin.yaml(缺省返回空结构)。
func readManifest(dir string) Manifest {
	b, err := os.ReadFile(filepath.Join(dir, "plugin.yaml"))
	if err != nil {
		return Manifest{Protocol: "bridge"}
	}
	var m Manifest
	if yaml.Unmarshal(b, &m) != nil {
		return Manifest{Protocol: "bridge"}
	}
	return m
}

// run 执行命令并回传输出。
func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
