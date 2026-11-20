//go:build !darwin

// bundle:非 macOS 平台没有 .app 包标识(见 bundle_darwin.go 的说明)。
package main

// bundleIdentifier 非 macOS 返回空串。
func bundleIdentifier() string { return "" }
