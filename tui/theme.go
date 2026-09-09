// M13 TUI 主题外部化:样式/配色经配置文件与 /theme 命令统一修改(免重编译)。
// 加载链:默认表(DefaultPalette)< data.palette(bundle/patch 装配层样板)<
// $GAH_HOME/config/theme.yaml(用户全局,覆盖前者);/theme <名> 运行期切换
// $GAH_HOME/config/themes/<名>.yaml(ApplyTheme 覆盖 active,即时重绘);
// /theme default 重置回启动活动覆盖链。渲染层零改动(fg()/colorVal 派生不变)。
package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// themeHome 运行时数据根(GAH_HOME,boot 恒设;~/.gah 兜底已弃用 2026-09)。
// 空仅出现在不经 cmd/gah 的嵌入/单测:宁回 TempDir 也不落 cwd/根。
func themeHome() string {
	if home := os.Getenv("GAH_HOME"); home != "" {
		return home
	}
	return os.TempDir()
}

// themeMainPath 用户全局主题文件 $GAH_HOME/config/theme.yaml。
func themeMainPath() string { return filepath.Join(themeHome(), "config", "theme.yaml") }

// themeDirPath 多主题目录 $GAH_HOME/config/themes/(每 *.yaml 文件一个主题)。
func themeDirPath() string { return filepath.Join(themeHome(), "config", "themes") }

// themeNamedPath 指定主题文件路径(防路径穿越:仅接受单段、非点开头的文件名)。
func themeNamedPath(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, "/\\") || name == ".." || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("palette: 非法主题名 %q(仅单段文件名,无扩展名)", name)
	}
	return filepath.Join(themeDirPath(), name+".yaml"), nil
}

// loadThemeFile 读主题文件 → token→色值覆盖。缺文件返回 nil(不报错);
// 空文件 = 空覆盖;坏 yaml 显式报错(不静默)。
func loadThemeFile(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]string{}, nil
	}
	var m map[string]string
	if err := yaml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("palette: 解析主题 %s: %w", path, err)
	}
	return m, nil
}

// loadThemeMain 用户全局主题(config/theme.yaml;缺文件 = 无覆盖)。
func loadThemeMain() (map[string]string, error) { return loadThemeFile(themeMainPath()) }

// loadThemeNamed 指定主题(config/themes/<名>.yaml);缺文件显式报错(用户明确指定,不静默)。
func loadThemeNamed(name string) (map[string]string, error) {
	path, err := themeNamedPath(name)
	if err != nil {
		return nil, err
	}
	over, err := loadThemeFile(path)
	if err != nil {
		return nil, err
	}
	if over == nil {
		return nil, fmt.Errorf("palette: 主题 %q 不存在(%s)", name, path)
	}
	return over, nil
}

// listThemes 可用主题名(themes/ 目录 *.yaml 去扩展名,排序;目录缺失 = 空)。
func listThemes() []string {
	ents, err := os.ReadDir(themeDirPath())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.HasSuffix(n, ".yaml") && !strings.HasPrefix(n, ".") {
			names = append(names, strings.TrimSuffix(n, ".yaml"))
		}
	}
	sort.Strings(names)
	return names
}

// themeResetSentinel /theme 哨兵:选中恢复启动活动覆盖链(默认 + data.palette + theme.yaml)。
const themeResetSentinel = "default"

// themeOptions /theme 一级枚举:可用主题列表 + default 哨兵(themes 目录空时仅哨兵)。
func themeOptions(_ []string) []sdk.Option {
	opts := []sdk.Option{{Value: themeResetSentinel, Desc: "恢复默认配色(启动活动覆盖链)"}}
	for _, n := range listThemes() {
		opts = append(opts, sdk.Option{Value: n, Desc: "主题文件 " + n + ".yaml"})
	}
	return opts
}
