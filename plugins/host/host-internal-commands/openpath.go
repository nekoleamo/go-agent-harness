package hostintcmd

import (
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

// openPath 用系统默认程序打开本地文件(异步:只 Start 不 Wait,浏览器的生命周期与 gah 无关)。
func openPath(path string) error { return openPathCmd(path).Start() }

// openPathCmd 平台命令构造(包级变量:测试替换它,避免真的拉起浏览器)。
var openPathCmd = func(path string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path)
	case "linux":
		return exec.Command("xdg-open", path)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		return exec.Command("true") // 不支持平台:空操作(调用方按成功处理)
	}
}
