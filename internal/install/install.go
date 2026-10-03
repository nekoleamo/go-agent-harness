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
	// Source 实际来源(repo spec 或本地绝对路径),原样回显给用户核对。
	Source string
	// BuildCmd 实际执行的构建命令(plugin.yaml 的 build,空 = 用默认)。
	// 为什么要往外送:确认文案必须含**命令原文**(它可以是任意 shell 命令)——
	// 由内核算好给入口,三个入口就不会拼出三种不一样的说法。
	BuildCmd string
	// Local 是不是从本地目录装的(本地目录的 id 默认是路径,展示上要说清来源)。
	Local bool
	// Audit 这次登记进白名单的审计行(时间/来源/哈希);装完回显给用户看。
	Audit plugintrust.AuditEntry
}

// Facts 把结果整成确认文案所需的事实(入口在**执行前**用 SpecFacts)。
func (r *Result) Facts() ConfirmFacts {
	return ConfirmFacts{Source: r.Source, ID: r.ID, Dir: r.Dir, BuildCmd: r.BuildCmd}
}

// Install 安装插件:spec 形如 <repo>[@<version>](桥)或 mcp:<id>:<command>(MCP)。
func Install(spec, home string) (*Result, error) {
	if strings.HasPrefix(spec, "mcp:") {
		return installMCP(spec, home)
	}
	return installBridge(spec, home)
}

// installBridge 拉取或就地读取 + 构建 + 落目录 + 登记。
//
// 来源两类(判据 isLocalDir,与 install-ui 同款 —— 同一个词在一个项目里必须是同一个意思):
//   - **本地目录**:`/abs/path`、`./rel`、`../rel`、或任何已存在的路径。**自己写的插件
//     不该被迫先 git init + push** —— 那是纯仪式,而且会把源码推到某个远端。
//   - git repo:`<repo>[@<version>]`(http(s)/git@/.git 一律按 repo 处理)。
//
// 本地目录**复制到临时区再构建**:构建产物必须落在我们自己管理的目录里
// (`go build` 会在源目录留 cache/临时文件,而用户的源码目录不该被构建过程弄脏)。
func installBridge(spec, home string) (*Result, error) {
	tmp, err := os.MkdirTemp("", "gah-install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	clone := filepath.Join(tmp, "repo")

	local := isLocalDir(spec)
	source := spec // 回显用:本地目录先按用户给的原样,末尾再换成绝对路径
	if local {
		src, err := filepath.Abs(spec)
		if err != nil {
			return nil, fmt.Errorf("install: 解析本地路径 %s: %w", spec, err)
		}
		if !fileExists(src) {
			return nil, fmt.Errorf("install: 本地路径不存在: %s", src)
		}
		if err := copyDir(src, clone); err != nil {
			return nil, fmt.Errorf("install: 复制本地目录: %w", err)
		}
		source = src // 绝对路径比用户手打的相对路径更可核对
	} else {
		repo, version := spec, ""
		if i := strings.LastIndex(spec, "@"); i > 0 {
			repo, version = spec[:i], spec[i+1:]
		}
		args := []string{"clone", "--depth", "1"}
		if version != "" {
			args = append(args, "--branch", version)
		}
		args = append(args, repo, clone)
		if out, err := run("", "git", args...); err != nil {
			return nil, fmt.Errorf("install: 拉取 %s: %w(%s)", repo, err, out)
		}
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
	built := filepath.Join(clone, binary)
	if _, err := os.Stat(built); err != nil {
		return nil, fmt.Errorf("install: 构建产物缺失 %s: %v", binary, err)
	}
	// 落 ~/.gah/plugins/<id>/
	dir := filepath.Join(home, "plugins", man.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := copyFile(built, filepath.Join(dir, binary)); err != nil {
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
	if err := list.RecordWithAudit(binary, sum, "install:"+spec); err != nil {
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
	audit, _ := list.LastAuditOf(binary)
	return &Result{
		ID: man.ID, Protocol: "bridge", Binary: binary, Dir: dir, Patch: patch,
		Source: source, BuildCmd: buildCmd, Local: local, Audit: audit,
	}, nil
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
	plugins := filepath.Join(home, "plugins")
	dir := filepath.Join(plugins, id)
	if !fileExists(dir) {
		return fmt.Errorf("uninstall: 插件 %s 未安装(%s)", id, dir)
	}
	// 白名单的键是**二进制文件名**(tool-demo / tool-kit.exe),不是插件 id(demo) ——
	// 必须在删目录**之前**把它读出来:删完就无从得知该撤哪一条了。
	// 撤不掉的后果很具体:同名再装、构建产物哈希与上次不同时,会被**自己的旧条目**拒掉,
	// 而错误文案说的是「文件可能已被改动」,与真实原因毫无关系。
	bins := pluginBinNames(dir)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	list, err := plugintrust.Load(plugins)
	if err != nil {
		return err // 清单坏了要报出来,不能悄悄留着幽灵条目
	}
	for _, b := range bins {
		if err := list.Remove(b); err != nil {
			return err
		}
	}
	return nil
}

// BinaryName 插件目录里**实际会加载的那个二进制**的文件名。
//
// 为什么需要它:`Item.Binary` 只来自 plugin.yaml,而**手工放置**的插件(没写 manifest)
// 那一栏是空的 —— 面板与 /install 清单若直接显示空值,用户看到的是"这个插件没有二进制",
// 与"我没 manifest"完全两回事。故回落到扫目录(与 host-bridge 的 isExternalPluginBin
// 同一口径:`tool-` / `cmd-` 前缀),找不到返回 ""。
func BinaryName(dir string) string {
	names := pluginBinNames(dir)
	if len(names) == 0 {
		return ""
	}
	if m := readManifest(dir); m.Binary != "" && fileExists(filepath.Join(dir, m.Binary)) {
		return m.Binary
	}
	return names[0]
}

// pluginBinNames 目录里会被 host-bridge 当成插件的二进制名(manifest 声明优先,
// 否则扫 tool-*/cmd-*;删目录前调用)。
func pluginBinNames(dir string) []string {
	var out []string
	if m := readManifest(dir); m.Binary != "" {
		out = append(out, m.Binary)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "tool-") || strings.HasPrefix(e.Name(), "cmd-") {
			out = append(out, e.Name())
		}
	}
	return out
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
