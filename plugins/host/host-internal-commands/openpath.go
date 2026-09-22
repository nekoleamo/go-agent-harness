package hostintcmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// exportOpenEnv /export 出 HTML 后自动打开浏览器的开关(0/false 关;其余开)。
const exportOpenEnv = "GAH_EXPORT_OPEN"

// shouldOpenExportBrowser 决定导出 HTML 后是否自动打开浏览器:
// env GAH_EXPORT_OPEN 优先(0/false 关,其余开);否则 manifest data.export_open_browser;缺省 true。
// 缺省开:导出一份 HTML 的人下一步必然是打开它 —— 与 `gah web` 的 data.open_browser 同一取向。
// 打不开(无桌面/SSH/容器)不是错误:文件已经写好了,导出本身是成功的(调用方只提示不报错)。
func shouldOpenExportBrowser(m *sdk.Manifest) bool {
	if v := os.Getenv(exportOpenEnv); v != "" {
		return !(v == "0" || strings.EqualFold(v, "false"))
	}
	if m != nil && m.Data != nil {
		if v, ok := m.Data["export_open_browser"].(bool); ok {
			return v
		}
	}
	return true
}

// openPath 用系统默认程序打开本地文件。
// **同步**(等命令自身返回,不等应用退出:macOS `open` / `xdg-open` / `rundll32` 都是立即返回)
// 并带上 stderr —— 否则“打开失败”只能是静默失败,用户与日志都查不出原因。
func openPath(path string) error {
	out, err := openPathCmd(path).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

// openPathCmd 平台命令构造(包级变量:测试替换它,避免真的拉起浏览器)。
// darwin 用**绝对路径** /usr/bin/open:PATH 被外部壳(桌面端 sidecar / 服务管理器)精简时
// 仍能可靠打开,xdg-open 与 rundll32 所在目录不在 POSIX 保证之列,保留查 PATH。
var openPathCmd = func(path string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("/usr/bin/open", path)
	case "linux":
		return exec.Command("xdg-open", path)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		return exec.Command("true") // 不支持平台:空操作(调用方按成功处理)
	}
}
