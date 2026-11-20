// debug:开发/自动化用的两个钩子(不参与业务逻辑,发行构建里默认关闭)。
//
// 为什么不用命令行开关:壳由 LaunchServices/托盘启动,命令行参数很难带;
// env 是唯一能同时覆盖「本地调试」与「CI 冒烟」的通道。
//
//	GAH_SHELL_CAPTURE=<path>     首帧显示后把窗口内容截一张 PNG(无屏幕录制权限时的唯一"看一眼"通道)
//	GAH_SHELL_CLOSE_TEST=1       显示后调一次 Close(),验证「关窗收托盘」而非退出
package main

import (
	"os"
	"time"
)

// runDebugHooks 首帧显示后执行调试钩子(全部由 env 控制,默认什么都不做)。
func (s *shell) runDebugHooks() {
	if p := os.Getenv("GAH_SHELL_CAPTURE"); p != "" {
		if png, err := s.window.CapturePage(); err != nil {
			shellLogf("窗内截图失败: %v", err)
		} else if err := os.WriteFile(p, png, 0o644); err != nil {
			shellLogf("写截图失败: %v", err)
		} else {
			shellLogf("窗内截图: %s(%d 字节)", p, len(png))
		}
	}
	if os.Getenv("GAH_SHELL_CLOSE_TEST") != "" {
		s.window.Close()
		time.Sleep(300 * time.Millisecond)
		shellLogf("关窗测试: visible=%v(期望 false,进程仍在)", s.window.IsVisible())
	}
}
