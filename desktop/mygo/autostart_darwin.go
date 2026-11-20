//go:build darwin

// autostart:开机自启(macOS)。
//
// 为什么自己实现:mygo 0.10.0 **没有导出开机自启 API**(只有 internal/platform 里的
// SetOpenAtLogin,Wails v3 有 app.Autostart)。§13.2 曾把这条记成"等价",实测是能力缺口,
// 所以这里用 LaunchAgent 落一份 plist 自己管。
//
// 口径:
//   - 只对打包运行(.app)开放:裸二进制没有 bundle id,登记成登录项在系统里显示不出来;
//   - 用 `/usr/bin/open -a <bundle>` 启动,而不是直接 exec 内层二进制 —— 后者不会作为
//     正规应用启动(Dock/菜单栏/聚焦行为都会怪);
//   - 判定 = plist 存在且内容对得上当前 bundle 路径(换目录/升级后路径变了要算"未启用")。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// autostartPlistName LaunchAgent 文件名(与 appID 同源,便于用户/我们定位)。
const autostartPlistName = appID + ".plist"

// plistPath LaunchAgent plist 落点。
func plistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents", autostartPlistName)
}

// plistContent 生成 LaunchAgent 内容(纯文本,一眼能看完)。
//
// bundle 路径要转义:它来自用户自己的安装位置(`~/Applications/Dev & Tools/gah.app` 这种
// 名字里带 `&` 的目录完全合法),直接拼进 XML 会让 plist 解析失败 —— 表现是"勾了开机自启
// 但重启后不生效",而日志里什么都没有。
func plistContent(bundle string) string {
	bundle = xmlEscape(bundle)
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>` + appID + `</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/open</string>
    <string>-a</string>
    <string>` + bundle + `</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>ProcessType</key><string>Interactive</string>
</dict>
</plist>
`
}

// xmlEscape 转义 XML 文本节点的四个特殊字符。
func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return r.Replace(s)
}

// autostartEnabled 读真实自启状态。
func (s *shell) autostartEnabled() bool {
	bundle := bundlePath()
	if bundle == "" {
		return false
	}
	p := plistPath()
	if p == "" {
		return false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "<string>"+xmlEscape(bundle)+"</string>")
}

// setAutostart 开/关开机自启。
func (s *shell) setAutostart(on bool) error {
	p := plistPath()
	if p == "" {
		return fmt.Errorf("取不到用户目录,无法设置开机自启")
	}
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除登录项失败: %w", err)
		}
		// bootout 失败不算错:plist 已经不在,系统下次登录就不会拉起它。
		_ = exec.Command("launchctl", "bootout", "gui/"+fmt.Sprint(os.Getuid()), p).Run()
		return nil
	}
	bundle := bundlePath()
	if bundle == "" {
		return fmt.Errorf("当前不是从 .app 包运行(开发态),不能登记开机自启;请用打包版再设置")
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("建 LaunchAgents 目录失败: %w", err)
	}
	if err := os.WriteFile(p, []byte(plistContent(bundle)), 0o644); err != nil {
		return fmt.Errorf("写登录项失败: %w", err)
	}
	// bootstrap 失败也接受:RunAtLoad 的 plist 在下次登录时照样生效;
	// 这一步只是让它"现在也立刻注册",失败只在日志里说一声。
	if out, err := exec.Command("launchctl", "bootstrap", "gui/"+fmt.Sprint(os.Getuid()), p).CombinedOutput(); err != nil {
		shellLogf("开机自启: launchctl bootstrap 失败(下次登录仍会生效): %v %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
