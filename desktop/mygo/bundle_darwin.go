//go:build darwin

// bundle:.app 包的标识(who am I)。
//
// 为什么需要它:通知中心在 macOS 上要求应用有**有效的 CFBundleIdentifier**
// —— 一个只有 `.app/Contents/MacOS/` 目录结构但没有 Info.plist 标识的"假包"会让
// UNUserNotificationCenter 直接抛 NSInternalInconsistencyException 把整个进程打死
// (2026-11-20 实测:跑一个手搓的 .app 做升级演练就是这样崩的)。所以"在 .app 里"不等于
// "能注册通知":两者必须分开判断。
package main

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// bundleIdentifier 读当前 .app 的 CFBundleIdentifier(读不到返回空串)。
//
// 用 `defaults read` 而不是自己解析 plist:真实包的 Info.plist 可能是二进制格式,
// 而 defaults 是系统自带的、两种格式都认的唯一事实源。
func bundleIdentifier() string {
	b := bundlePath()
	if b == "" {
		return ""
	}
	out, err := exec.Command("defaults", "read", filepath.Join(b, "Contents", "Info.plist"), "CFBundleIdentifier").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
