// assets:壳要用的图标(全部由 `go run ./desktop/wails/cmd/genicons` 从唯一图标源生成)。
//
// 为什么要自己生成:Wails 自带的 `pkg/icons` 是**框架的默认图标**(托盘、窗口、Dock 全用它),
// 真机上看到的就是 Wails 的红龙 —— 这是"壳看起来是别人的应用"最直接的来源。
// 三个用途需要三种形态:
//   - appicon.png      窗口/任务栏/Dock(macOS 打包时另用 icon.icns,这里是裸二进制跑时的兜底)
//   - tray.png         Windows/Linux 托盘(彩色)
//   - tray-template.png macOS 托盘模板图(纯黑+alpha,系统按明暗主题反色)
package main

import _ "embed"

//go:embed assets/appicon.png
var appIconPNG []byte

//go:embed assets/tray.png
var trayColorPNG []byte

//go:embed assets/tray-template.png
var trayTemplatePNG []byte
