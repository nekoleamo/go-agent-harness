// exename.go:平台可执行文件名的单一事实源。
//
// 为什么需要:Windows 的 os/exec 对**绝对路径**不做扩展名补全 —— 不带 .exe 的 PE 一律
// ErrNotFound(Go 1.27 os/exec/lp_windows.go:hasExt 为假时只逐个试 PATHEXT 里的扩展名)。
// 三处都在产出可执行文件名(随包插件释放 internal/embed、发布产物 scripts/gen-extplugins.sh、
// 插件安装 internal/install),口径必须一致,否则症状是「装上了却起不来」。
package sdk

import (
	"runtime"
	"strings"
)

// ExeSuffix 当前平台可执行文件的扩展名(Windows 为 ".exe",其余为空串)。
func ExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// BinaryName 给可执行文件基名补上平台扩展名(已带 .exe 则不重复补)。
//
// 只补不删:非 Windows 上传入的 "tool-x.exe" 原样保留(作者显式写的名字不该被悄悄改掉)。
func BinaryName(name string) string {
	if ExeSuffix() == "" || strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name
	}
	return name + ".exe"
}
