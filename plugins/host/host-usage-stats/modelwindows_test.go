package hostusagestats

import "testing"

// TestDeepSeekV4Window1M V4 系列是 1M 上下文(官方 2026-09 发布);V4.1 Flash 的模型名
// `deepseek-flash` 与别名 `DeepSeek-V4.1-Flash` 都必须落进 1M,不能掉进 128K 兜底。
// 反向:老模型(deepseek-chat / moonshot)仍按 128K —— 表是**逐档**的,不是整体抬。
func TestDeepSeekV4Window1M(t *testing.T) {
	for _, m := range []string{
		"DeepSeek-V4.1-Flash", "deepseek-v4.1-flash", "deepseek-v4-flash",
		"deepseek-v4-pro", "deepseek-flash", "deepseek-v4",
	} {
		if got := matchWindow(m, modelWindows); got != 1024*1024 {
			t.Fatalf("%s 窗口 = %d,期望 %d(1M)", m, got, 1024*1024)
		}
	}
	for _, m := range []string{"deepseek-chat", "deepseek-reasoner", "deepseek-coder"} {
		if got := matchWindow(m, modelWindows); got != 128*1024 {
			t.Fatalf("%s 窗口 = %d,期望 %d(128K)", m, got, 128*1024)
		}
	}
}
