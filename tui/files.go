// P4-2 @ 引用补全的文件索引:递归收集当前工作区相对路径为候选(Option.Value=rel 路径)。
// 排除常见生成/依赖目录与隐藏目录;上限防超大仓库(截断按 WalkDir 字典序靠前保留)。
package tui

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// mentionMaxFiles @ 候选全量上限(超大仓库截断,防止选择器/过滤开销失控)。
const mentionMaxFiles = 4000

// mentionSkipDirs WalkDir 跳过的目录(依赖/生成/版本控制等)。
var mentionSkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, ".venv": true, "venv": true, "__pycache__": true,
	".build": true, "dist": true, "build": true, ".next": true, ".nuxt": true,
	"coverage": true, ".vscode": true, ".idea": true, ".gah": true,
}

// projectFiles @ 引用候选(App 注入;cwd 缓存——同一工作区只索引一次,workspace 切换失效)。
func (a *App) projectFiles() []sdk.Option {
	wd, err := os.Getwd()
	if err != nil {
		return nil
	}
	if a.mFiles != nil && a.mFilesDir == wd {
		return a.mFiles // 缓存命中(同一 cwd)
	}
	a.mFilesDir = wd
	a.mFiles = indexProjectFiles(wd)
	return a.mFiles
}

// indexProjectFiles 递归索引目录内文件(相对路径,跳过 mentionSkipDirs 与隐藏目录,上限截断)。
// 纯函数(输入绝对目录,输出候选),便于单测;结果按相对路径字典序稳定。
func indexProjectFiles(dir string) []sdk.Option {
	var files []string
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 跳过不可访问条目(权限/断链),不中断索引
		}
		if d.IsDir() {
			name := d.Name()
			if path != dir && (mentionSkipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir // 跳过依赖/生成/隐藏目录
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, path)
			if len(files) >= mentionMaxFiles {
				return fs.SkipAll // 达上限停止(字典序靠前保留)
			}
		}
		return nil
	})
	opts := make([]sdk.Option, 0, len(files))
	for _, f := range files {
		rel, err := filepath.Rel(dir, f)
		if err != nil {
			continue
		}
		opts = append(opts, sdk.Option{Value: filepath.ToSlash(rel), Desc: fileSizeLabel(f)})
	}
	sort.SliceStable(opts, func(i, j int) bool { return opts[i].Value < opts[j].Value })
	return opts
}

// fileSizeLabel 候选说明列:文件体积(粗筛用;路径层级本身已表达目录)。
func fileSizeLabel(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	n := fi.Size()
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return ""
}
