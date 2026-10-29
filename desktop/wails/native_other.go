//go:build !darwin

// 非 macOS:没有等价的「Dock 弹跳」,通知之外的提示通道交给系统自己决定。
package main

func bounceDock() {}
