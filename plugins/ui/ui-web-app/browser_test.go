package uiweb

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// 开关解析矩阵:env GAH_WEB_OPEN 优先;否则 data.open_browser;缺省 true。
func TestShouldOpenBrowser(t *testing.T) {
	t.Setenv("GAH_WEB_OPEN", "") // 基线:无 env
	m := func(data map[string]any) *sdk.Manifest {
		return &sdk.Manifest{Data: data}
	}
	cases := []struct {
		name string
		env  string // "" = 未设置
		data map[string]any
		want bool
	}{
		{"缺省(无 env 无 data)", "", nil, true},
		{"data 显式开", "", map[string]any{"open_browser": true}, true},
		{"data 显式关", "", map[string]any{"open_browser": false}, false},
		{"env 0 关(覆盖 data 开)", "0", map[string]any{"open_browser": true}, false},
		{"env false 关", "false", nil, false},
		{"env 1 开", "1", nil, true},
		{"env true 开", "true", map[string]any{"open_browser": false}, true},
		{"env 空串视为未设置", "", map[string]any{"open_browser": true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env == "" {
				if err := os.Unsetenv("GAH_WEB_OPEN"); err != nil {
					t.Fatal(err)
				}
			} else {
				t.Setenv("GAH_WEB_OPEN", tc.env)
			}
			if got := shouldOpenBrowser(m(tc.data)); got != tc.want {
				t.Errorf("shouldOpenBrowser(env=%q data=%v) = %v, want %v", tc.env, tc.data, got, tc.want)
			}
		})
	}
}

// 平台命令构造:仅断言命令形态,不真正启动浏览器。
func TestOpenBrowserCmd(t *testing.T) {
	cmd := openBrowserCmd("http://127.0.0.1:2233")
	if cmd == nil {
		t.Fatal("openBrowserCmd 返回 nil")
	}
	switch runtime.GOOS {
	case "darwin":
		if filepath.Base(cmd.Path) != "open" || len(cmd.Args) != 2 || cmd.Args[1] != "http://127.0.0.1:2233" {
			t.Errorf("darwin 应为 open <url>,got %v", cmd.Args)
		}
	case "linux":
		if filepath.Base(cmd.Path) != "xdg-open" || len(cmd.Args) != 2 || cmd.Args[1] != "http://127.0.0.1:2233" {
			t.Errorf("linux 应为 xdg-open <url>,got %v", cmd.Args)
		}
	case "windows":
		if filepath.Base(cmd.Path) != "rundll32" ||
			cmd.Args[1] != "url.dll,FileProtocolHandler" ||
			cmd.Args[2] != "http://127.0.0.1:2233" {
			t.Errorf("windows 应为 rundll32 url.dll,FileProtocolHandler <url>,got %v", cmd.Args)
		}
	default:
		if filepath.Base(cmd.Path) != "true" {
			t.Errorf("不支持平台应空操作,got %v", cmd.Args)
		}
	}
}
