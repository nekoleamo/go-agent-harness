// UI 插件安装/卸载/清单(M7.2,gah -install-ui/-uninstall-ui/-list-ui-plugins)。
// 复用 install 包流程模式(拉取→构建→落 home/ui-plugins/<id>/),产物为前端静态资源
// (vite build),装配期由 web server 聚合并被前端加载器动态导入覆盖槽位。
// 安全契约:`v-html` 源码静态扫描拒装(渲染层转义禁用的第三方模板注入)。
package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// UIManifest UI 插件声明(仓库根 manifest.json)。
type UIManifest struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	// Out 构建输出目录(相对仓库根;默认 dist;产物为 vite build 结果)。
	Out string `json:"out"`
	// Slots 槽位覆盖声明(module 相对插件目录,指向 Out 内产物入口)。
	Slots []UISlot `json:"slots"`
}

// UISlot 单个槽位覆盖。
type UISlot struct {
	Name     string `json:"name"` // stream | input | statusbar | confirm
	Priority int    `json:"priority"`
	Module   string `json:"module"` // 如 ./dist/plugin.js
}

// UIResult 安装结果摘要。
type UIResult struct {
	ID      string
	Version string
	Dir     string
	Slots   int
}

// InstallUI 安装 UI 插件:spec = <repo>[@<version>] 或本地目录路径。
// 流程:拉取/拷贝源码 → v-html 静态扫描(命中拒装)→ npm 构建 → 产物落 home/ui-plugins/<id>/。
func InstallUI(spec, home string) (*UIResult, error) {
	tmp, err := os.MkdirTemp("", "gah-ui-install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	src := filepath.Join(tmp, "repo")
	if isLocalDir(spec) {
		if !fileExists(spec) {
			return nil, fmt.Errorf("install-ui: 本地目录不存在 %s", spec)
		}
		if err := copyDir(spec, src); err != nil {
			return nil, err
		}
	} else {
		repo, version := spec, ""
		if i := strings.LastIndex(spec, "@"); i > 0 {
			repo, version = spec[:i], spec[i+1:]
		}
		args := []string{"clone", "--depth", "1"}
		if version != "" {
			args = append(args, "--branch", version)
		}
		args = append(args, repo, src)
		if out, err := run("", "git", args...); err != nil {
			return nil, fmt.Errorf("install-ui: 拉取 %s: %w(%s)", repo, err, out)
		}
	}

	// 契约校验:v-html 静态扫描(源码 .vue/.html/.js/.ts 指令形态命中 → 拒装;注释提及不误伤)
	if file, ok := scanVHTML(src); ok {
		return nil, fmt.Errorf("install-ui: 拒绝安装——源码含 v-html 指令(%s)(渲染层转义契约,防模板注入)", file)
	}

	// manifest 校验
	man, err := readUIManifest(src)
	if err != nil {
		return nil, fmt.Errorf("install-ui: 读 manifest.json: %w", err)
	}
	if man.ID == "" {
		return nil, fmt.Errorf("install-ui: 仓库缺少 manifest.json(id 必填)")
	}
	if strings.Contains(man.ID, "/") || strings.Contains(man.ID, "..") {
		return nil, fmt.Errorf("install-ui: manifest id 含非法路径字符 %q", man.ID)
	}
	for _, s := range man.Slots {
		switch s.Name {
		case "stream", "input", "statusbar", "confirm", "settings-section", "sidebar-action", "extra-panel":
		default:
			return nil, fmt.Errorf("install-ui: 未知槽位 %q(契约 v1:stream/input/statusbar/confirm;v2 扩展:settings-section/sidebar-action/extra-panel)", s.Name)
		}
		if s.Module == "" {
			return nil, fmt.Errorf("install-ui: 槽位 %s 缺 module(产物入口,如 ./dist/plugin.js)", s.Name)
		}
	}

	// 构建(vite;需 npm;产物默认 dist)
	if out, err := run(src, "sh", "-c", "npm install --no-audit --no-fund && npm run build"); err != nil {
		return nil, fmt.Errorf("install-ui: 构建失败(需 node/npm): %w(%s)", err, out)
	}
	outDir := man.Out
	if outDir == "" {
		outDir = "dist"
	}
	// module 入口与产物一致性校验(module 相对插件根,通常含 out 前缀,如 ./dist/plugin.js)
	for _, s := range man.Slots {
		mod := filepath.Join(src, strings.TrimPrefix(s.Module, "./"))
		if !fileExists(mod) {
			rel := strings.TrimPrefix(s.Module, "./")
			return nil, fmt.Errorf("install-ui: 槽位 %s 的 module 产物缺失 %s(构建输出目录=%s)", s.Name, rel, outDir)
		}
	}

	// 落 home/ui-plugins/<id>/(manifest 同步;装配即时生效,前端重载页面即换)
	dir := filepath.Join(home, "ui-plugins", man.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// 保留 out 子目录层级(产物与 manifest.module 相对插件根一致,如 ./dist/plugin.js)
	outDist := dir
	if outDir != "" {
		outDist = filepath.Join(dir, outDir)
	}
	if err := copyDir(filepath.Join(src, outDir), outDist); err != nil {
		return nil, fmt.Errorf("install-ui: 复制产物: %w", err)
	}
	raw, _ := json.MarshalIndent(man, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		return nil, err
	}
	return &UIResult{ID: man.ID, Version: man.Version, Dir: dir, Slots: len(man.Slots)}, nil
}

// UninstallUI 卸载:删目录(装配即时失效;前端重载回默认实现)。
func UninstallUI(id, home string) error {
	dir := filepath.Join(home, "ui-plugins", id)
	if !fileExists(dir) {
		return fmt.Errorf("uninstall-ui: 插件 %s 未安装(%s)", id, dir)
	}
	return os.RemoveAll(dir)
}

// UIItem 已安装 UI 插件条目。
type UIItem struct {
	ID      string
	Version string
	Dir     string
	Slots   []UISlot
}

// ListUI 列出已安装 UI 插件(home/ui-plugins 下一层含 manifest.json 的目录)。
func ListUI(home string) []UIItem {
	var out []UIItem
	root := filepath.Join(home, "ui-plugins")
	entries, err := os.ReadDir(root)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf := filepath.Join(root, e.Name(), "manifest.json")
		b, err := os.ReadFile(mf)
		if err != nil {
			continue
		}
		var m UIManifest
		if json.Unmarshal(b, &m) != nil || m.ID == "" {
			continue
		}
		out = append(out, UIItem{ID: m.ID, Version: m.Version, Dir: filepath.Join(root, e.Name()), Slots: m.Slots})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// scanVHTML 静态扫描源码中的 v-html 指令形态(模板指令必带绑定 `v-html=`,大小写不敏感;
// 注释/文档里的"v-html"字样不误伤)。返回命中文件路径(ok=true);跳过 node_modules。
func scanVHTML(dir string) (string, bool) {
	found := ""
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found != "" {
			return nil
		}
		if strings.Contains(p, "node_modules") {
			return nil
		}
		lower := strings.ToLower(p)
		if !strings.HasSuffix(lower, ".vue") && !strings.HasSuffix(lower, ".html") &&
			!strings.HasSuffix(lower, ".js") && !strings.HasSuffix(lower, ".ts") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if strings.Contains(strings.ToLower(string(b)), "v-html=") {
			found = p
		}
		return nil
	})
	return found, found != ""
}

// isLocalDir spec 是否为本地路径(相对/绝对的已存在路径;仓库 URL 以 .git 或协议前缀)。
func isLocalDir(spec string) bool {
	if strings.HasPrefix(spec, "http://") || strings.HasPrefix(spec, "https://") ||
		strings.HasPrefix(spec, "git@") || strings.HasSuffix(spec, ".git") {
		return false
	}
	return strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") ||
		fileExists(spec)
}

// readUIManifest 读仓库根 manifest.json(缺省返回空结构)。
func readUIManifest(dir string) (UIManifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return UIManifest{}, err
	}
	var m UIManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return UIManifest{}, err
	}
	return m, nil
}

// copyDir 递归拷贝目录(node_modules 排除——构建期重装,不落盘)。
// 注意:Walk 会清理相对路径的 "./" 前缀,TrimPrefix 失配——src 先绝对化,路径稳定。
func copyDir(src, dst string) error {
	abs, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	return filepath.Walk(abs, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "node_modules" || strings.HasPrefix(info.Name(), ".") {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, strings.TrimPrefix(p, abs)), 0o755)
		}
		rel := strings.TrimPrefix(p, abs)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
}
