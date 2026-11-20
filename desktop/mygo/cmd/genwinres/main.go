// genwinres:给 Windows 壳产物写 PE 资源(图标 + 版本信息 + manifest)。
//
// 为什么需要它:Windows 的**桌面/任务栏/标题栏图标**与"属性 → 详细信息"都来自 exe 自己的
// PE 资源(.rsrc 段),不是运行期能改的(托盘图标是另一条路:运行期塞 PNG,那条现在是对的)。
// 裸 `go build` 产出的 exe 没有资源段 ⇒ 用户看到的就是 Go 的默认图标 —— 2026-11-20 真机反馈
// 「桌面图标、任务栏和状态栏都是默认图标,托盘正常」正是这一条。
//
// 产物:`rsrc_windows_<arch>.syso`,Go 按文件名里的平台后缀自动链接(不需要 build tag),
// 所以放一次就永远生效;但它含**版本号**,发版时必须按当前 tag 重新生成(见 gen/publish 脚本)。
//
// 用 winres(纯 Go)而不是 windres:目标是 CGO=0、跨平台构建,CI 的 runner 不保证有 MinGW。
//
// 用法:
//
//	go run ./desktop/mygo/cmd/genwinres -version 0.9.2   # 产物落 desktop/mygo/
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/tc-hib/winres"
	"github.com/tc-hib/winres/version"
)

// iconSizes Windows 实际会用到的图标尺寸(16 = 标题栏/托盘小图,256 = 大图标视图/桌面)。
var iconSizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

func main() {
	versionStr := flag.String("version", "0.0.0", "写进 VERSIONINFO 的版本号(取 git describe)")
	outDir := flag.String("out", ".", "syso 输出目录(必须是壳模块根,Go 才链接到)")
	iconPath := flag.String("icon", "../../desktop/icons/gah-icon-1024.png", "图标源(1024×1024 PNG;相对壳模块目录)")
	flag.Parse()

	raw, err := os.ReadFile(*iconPath)
	if err != nil {
		fatal("读图标源失败: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		fatal("解码图标源失败: %v", err)
	}
	icon, err := winres.NewIconFromResizedImage(img, iconSizes)
	if err != nil {
		fatal("生成 ICO 失败: %v", err)
	}

	for _, arch := range []struct {
		name string
		a    winres.Arch
	}{
		{"amd64", winres.ArchAMD64},
		{"arm64", winres.ArchARM64},
	} {
		rs := &winres.ResourceSet{}
		if err := rs.SetIcon(winres.ID(1), icon); err != nil {
			fatal("写图标资源失败: %v", err)
		}

		vi := &version.Info{}
		vi.SetFileVersion(numericVersion(*versionStr))
		vi.SetProductVersion(numericVersion(*versionStr))
		_ = vi.Set(version.LangDefault, version.ProductName, "gah")
		_ = vi.Set(version.LangDefault, version.FileDescription, "gah 桌面版")
		_ = vi.Set(version.LangDefault, version.CompanyName, "nekoleamo")
		_ = vi.Set(version.LangDefault, version.OriginalFilename, "gah-desktop.exe")
		_ = vi.Set(version.LangDefault, version.ProductVersion, *versionStr)
		_ = vi.Set(version.LangDefault, version.FileVersion, *versionStr)
		rs.SetVersionInfo(*vi)

		// manifest:Per-Monitor V2 DPI(否则高分屏上整个界面被系统位图拉伸,糊成一片)、
		// 长路径感知、常见控件 V6。执行级别保持 asInvoker(不要求管理员)。
		rs.SetManifest(winres.AppManifest{
			Description:         "gah 桌面版",
			ExecutionLevel:      winres.AsInvoker,
			DPIAwareness:        winres.DPIPerMonitorV2,
			LongPathAware:       true,
			UseCommonControlsV6: true,
		})

		out := filepath.Join(*outDir, "rsrc_windows_"+arch.name+".syso")
		f, err := os.Create(out)
		if err != nil {
			fatal("建 %s 失败: %v", out, err)
		}
		if err := rs.WriteObject(f, arch.a); err != nil {
			_ = f.Close()
			fatal("写 %s 失败: %v", out, err)
		}
		if err := f.Close(); err != nil {
			fatal("关闭 %s 失败: %v", out, err)
		}
		st, _ := os.Stat(out)
		fmt.Printf("genwinres: %s(%d 字节, version=%s)\n", out, st.Size(), *versionStr)
	}
}

// numericVersion 把 "0.9.2-preview" / "v0.9.2" 收敛成 Windows 需要的 4 段数字。
// 预发布后缀一律丢弃:VERSIONINFO 只收数字段,后缀留给界面显示。
func numericVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	out := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		if i < len(parts) {
			out = append(out, parts[i])
			continue
		}
		out = append(out, "0")
	}
	return strings.Join(out, ".")
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "genwinres: "+format+"\n", args...)
	os.Exit(1)
}
