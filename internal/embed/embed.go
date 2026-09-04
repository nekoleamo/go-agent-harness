// Package embed 提供配置样板的内嵌资源与首启释放(对齐设计 §7.2/7.3:单二进制自包含)。
// seed/ 与仓库 config/ 保持一致(guard 测试保证);发布形态下无本地 config 也能启动。
package embed

import (
	"compress/gzip"
	"embed"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

//go:embed seed
var Seed embed.FS

// FileNames 返回 seed 中的样板文件名(首启释放清单)。
func FileNames() ([]string, error) {
	return listNames(Seed, "seed")
}

func listNames(fsys fs.FS, dir string) ([]string, error) {
	entries, err := fs.ReadDir(fsys, dir)
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
// EnsurePlugins(P1 方案 B):随包插件二进制释放到 home/plugins/<name>/(已存在跳过,用户自装不覆盖)。
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

// EnsurePlugins 释放随包外部插件二进制到 home/plugins/<name>/<name>(方案 B 首启释放)。
// P0 体积门(M7):embed 存 gzip(.gz,压缩率约 50%),释放时解压落盘;
// 已存在的同名文件跳过(用户经 gah -install/-uninstall 维护的版本优先)。
// OpenExtPlugin 打开本平台外部插件 gzip 产物(只读;调用方负责 Close)。
// P4 平台匹配:build-tag 保证只取当前构建平台的产物(黑盒测试/工具链读取用)。
func OpenExtPlugin(bin string) (io.ReadCloser, error) {
	return extPlugins.Open(extPluginDir + "/" + bin + ".gz")
}

// 外部插件 embed 声明按平台拆在 extplugins_<os>_<arch>.go(build-tag 限定,
// 每平台文件定义同名 extPlugins/extPluginDir;主包每目标只嵌本平台产物,
// 体积门不变,发行产物平台匹配——P4 交叉编译矩阵回归)。
func EnsurePlugins(home string) ([]string, error) {
	names, err := listNames(extPlugins, extPluginDir)
	if err != nil {
		return nil, err
	}
	var written []string
	for _, n := range names {
		if !strings.HasSuffix(n, ".gz") {
			continue // 只处理 gzip 打包的外部插件
		}
		bin := strings.TrimSuffix(n, ".gz")
		dst := filepath.Join(home, "plugins", bin, bin)
		if _, err := os.Stat(dst); err == nil {
			continue // 已存在(用户自装/旧版本):不覆盖
		}
		fgz, err := extPlugins.Open(extPluginDir + "/" + n)
		if err != nil {
			return nil, err
		}
		gzr, err := gzip.NewReader(fgz)
		if err != nil {
			fgz.Close()
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			gzr.Close()
			fgz.Close()
			return nil, err
		}
		raw, err := io.ReadAll(gzr)
		gzr.Close()
		fgz.Close()
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, raw, 0o755); err != nil {
			return nil, err
		}
		written = append(written, dst)
	}
	return written, nil
}

var _ = sdk.SDKVersion // 保持 sdk 感知(seed 与 SDK 同版本发布语义)
