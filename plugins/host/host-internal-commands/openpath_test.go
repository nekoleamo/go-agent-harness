package hostintcmd

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// TestShouldOpenExportBrowser 开关优先级:env > manifest data > 缺省 true。
// 缺省开是有意的:导出一份 HTML 的人下一步必然是打开它(与 `gah web` 的 data.open_browser 同取向)。
func TestShouldOpenExportBrowser(t *testing.T) {
	on := map[string]any{"export_open_browser": true}
	off := map[string]any{"export_open_browser": false}
	cases := []struct {
		name string
		env  string
		data map[string]any
		want bool
	}{
		{"缺省开(无 env 无 data)", "", nil, true},
		{"data 关", "", off, false},
		{"data 开", "", on, true},
		{"data 非布尔按缺省(不静默当 false)", "", map[string]any{"export_open_browser": "no"}, true},
		{"env 0 压过 data 开", "0", on, false},
		{"env false 关", "false", nil, false},
		{"env 1 压过 data 关", "1", off, true},
		{"env 其它值视为开", "yes", off, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(exportOpenEnv, c.env)
			var m *sdk.Manifest
			if c.data != nil {
				m = &sdk.Manifest{Data: c.data}
			}
			if got := shouldOpenExportBrowser(m); got != c.want {
				t.Fatalf("shouldOpenExportBrowser = %v,期望 %v", got, c.want)
			}
		})
	}
}

// TestOpenPathReportsCommandFailure open 类命令非 0 退出时,错误必须带上它的 stderr ——
// 否则"没反应"没法归因(默认应用缺失、无图形环境、被策略拦都长一个样)。
func TestOpenPathReportsCommandFailure(t *testing.T) {
	orig := openPathCmd
	defer func() { openPathCmd = orig }()
	openPathCmd = func(string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo 无图形环境 >&2; exit 3")
	}
	err := openPath("/tmp/x.html")
	if err == nil || !strings.Contains(err.Error(), "无图形环境") {
		t.Fatalf("失败应带 stderr: %v", err)
	}
	// 成功路径不返回错误
	openPathCmd = func(string) *exec.Cmd { return exec.Command("true") }
	if err := openPath("/tmp/x.html"); err != nil {
		t.Fatalf("成功不应报错: %v", err)
	}
}
