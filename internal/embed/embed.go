// Package embed 提供配置样板的内嵌资源与首启释放(对齐设计 §7.2/7.3:单二进制自包含)。
// seed/ 与仓库 config/ 保持一致(guard 测试保证);发布形态下无本地 config 也能启动。
package embed

import (
	"embed"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

//go:embed seed
var Seed embed.FS

// FileNames 返回 seed 中的样板文件名(首启释放清单)。
func FileNames() ([]string, error) {
	return listNames("seed")
}

func listNames(dir string) ([]string, error) {
	entries, err := fs.ReadDir(Seed, dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// EnsureSeed 把 seed 样板释放到 home 的 config/ 目录(缺失才写;用户编辑不被覆盖)。
// 返回释放的文件名列表。
func EnsureSeed(home string) ([]string, error) {
	cfgDir := filepath.Join(home, "config")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return nil, err
	}
	names, err := FileNames()
	if err != nil {
		return nil, err
	}
	var written []string
	for _, n := range names {
		dst := filepath.Join(cfgDir, n)
		if _, err := os.Stat(dst); err == nil {
			continue // 已存在:不覆盖(可编辑层)
		}
		raw, err := Seed.ReadFile("seed/" + n)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return nil, err
		}
		written = append(written, dst)
	}
	return written, nil
}

var _ = sdk.SDKVersion // 保持 sdk 感知(seed 与 SDK 同版本发布语义)
