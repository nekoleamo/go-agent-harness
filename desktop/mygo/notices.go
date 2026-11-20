// notices:壳侧提示(便携模式 / 旧数据迁移 / 后台服务失联)—— 由页面自己来取。
//
// 为什么不做「壳往页面里注入 DOM」:旧 Rust 壳用的是 Tauri 的 `w.eval()`,而 Wails v3 的
// `ExecJS` **对从外部 URL 加载的页面永远不生效** —— 它把脚本压进 pendingJS 队列,等
// `runtimeLoaded` 变真才执行,而 runtimeLoaded 只在页面加载 wails 自己的 runtime 时才置位
// (外部页面根本没有 runtime,同「拿不到 bindings」是同一个根因,2026-10-29 实测:注入的
// 横带一次都没出现过)。
//
// 于是改成**拉取式**:壳把提示存在内存里(见 /shell/notices),页面启动后取一次,失联时再取。
// 好处:导航/重载之后提示还在;不需要任何注入 hack;可以单测。
//
// 这三类提示必须被看见,理由是它们都是**数据安全相关**的事实:
//
//	「便携模式:数据在本目录的 gah-data/」/「旧数据已迁到 X」/「后台服务已退出」。
//
// 系统通知可能被用户拒绝授权(实测就会),所以不能只靠通知。
package main

import "sync"

// notices 壳侧提示的存储(并发安全:启动时写、健康探测写、HTTP 读)。
var shellNotices = struct {
	mu    sync.RWMutex
	items []string
}{}

// setShellNotices 整体替换提示列表(空 = 清空)。
func setShellNotices(items []string) {
	shellNotices.mu.Lock()
	if len(items) == 0 {
		shellNotices.items = nil
	} else {
		shellNotices.items = append([]string(nil), items...)
	}
	shellNotices.mu.Unlock()
}

// getShellNotices 取一份快照。
func getShellNotices() []string {
	shellNotices.mu.RLock()
	defer shellNotices.mu.RUnlock()
	return append([]string(nil), shellNotices.items...)
}
