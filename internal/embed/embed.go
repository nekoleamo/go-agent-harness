// Package embed 提供配置样板的内嵌资源与首启释放(对齐设计 §7.2/7.3:单二进制自包含)。
// seed/ 与仓库 config/ 保持一致(guard 测试保证);发布形态下无本地 config 也能启动。
package embed

import (
	"compress/gzip"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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

// EnsureSeed 把 seed 样板释放到 home 的 config/ 目录。
// 版本语义:缺失写;已存在且版本一致 → 跳过(用户编辑不被覆盖);
// **seed 版本更高(bundle-*.yaml 头部 seed-version)→ 备份后覆盖**——新增基础能力条目
// (host-* 等)老用户自动升级,无需人工删样板(配置树语义:用户自定义应走 patch 层)。
// 返回释放/升级的文件名列表。
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
		raw, err := Seed.ReadFile("seed/" + n)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			if err := os.WriteFile(dst, raw, 0o644); err != nil {
				return nil, err
			}
			written = append(written, dst)
			continue
		}
		if !strings.HasPrefix(n, "bundle-") {
			continue // 非 bundle 样板(profile/patch):用户配置偏好,已有不覆盖
		}
		if seedVersion(raw) <= diskVersion(dst) {
			continue // 版本一致或更高:不覆盖(用户编辑保留)
		}
		// 版本升级:备份旧内容后覆盖(新增能力条目对老用户生效)
		old, err := os.ReadFile(dst)
		if err != nil {
			return nil, fmt.Errorf("seed 升级读取旧样板失败 %s: %w", dst, err)
		}
		bak := dst + ".bak-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(bak, old, 0o644); err != nil {
			return nil, fmt.Errorf("seed 升级备份失败 %s: %w", dst, err)
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return nil, err
		}
		written = append(written, dst)
	}
	return written, nil
}

// seedVersion 解析样板首部 seed-version 注释(bundle 系列;无标记 = 0)。
func seedVersion(raw []byte) int {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# seed-version:") {
			// 取首个 token(容忍行内说明:# seed-version: 1 # 备注…)
			f := strings.Fields(strings.TrimPrefix(line, "# seed-version:"))
			if len(f) > 0 {
				if v, err := strconv.Atoi(f[0]); err == nil {
					return v
				}
			}
		}
	}
	return 0
}

// diskVersion 读取落盘样板版本(无版本/不可读 = 0,视为旧版触发升级)。
func diskVersion(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return seedVersion(raw)
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
