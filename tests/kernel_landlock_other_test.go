//go:build !linux

// 非 Linux 平台的占位:内核沙箱可用性由 platform 分支决定(darwin 另判 sandbox-exec)。
package tests

func landlockABIVersion() int { return -1 }
