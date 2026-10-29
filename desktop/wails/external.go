// external:把「打开外部链接 / 在文件管理器里定位文件」这两件壳专属的事收在一处。
//
// 为什么要壳做(浏览器里 `target=_blank` 就够了):
//   - 桌面窗口是**同一个 webview**,页面里点外链会把这个窗口导航走 —— 用户就再也回不到
//     gah 了(旧 Rust 壳因此有 openURLInBrowser + navigationAllowed 两道);
//   - 导出文件后「在文件管理器里定位」没有任何 Web API 能做。
//
// 安全口径:只放行 http/https 交给系统浏览器;file:// 等本地 scheme 一律拒绝 ——
// 否则页面里的一行链接就能让系统去打开本机任意路径。
package main

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// openExternal 用系统默认浏览器打开链接(只接受 http/https)。
func (s *shell) openExternal(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("链接无法解析: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("只允许打开 http/https 链接(收到 %q)", u.Scheme)
	}
	shellLogf("打开外部链接: %s", redactURL(raw))
	return s.app.Browser.OpenURL(raw)
}

// revealFile 在文件管理器里定位一个已存在的文件(mac 高亮选中,win 打开所在目录并选中)。
func revealFile(path string) error {
	cmd, err := revealCommand(path)
	if err != nil {
		return err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("打开文件位置失败: %v(%s)", err, string(out))
	}
	return nil
}

// revealCommand 按平台给出定位命令(纯函数,便于单测)。
func revealCommand(path string) (*exec.Cmd, error) {
	if path == "" {
		return nil, fmt.Errorf("没有可定位的路径")
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", "-R", path), nil
	case "windows":
		// explorer 的 /select 参数要求路径用反斜杠且不能加引号(引号会被当成路径的一部分)。
		return exec.Command("explorer", "/select,", path), nil
	default:
		return exec.Command("xdg-open", path), nil
	}
}
