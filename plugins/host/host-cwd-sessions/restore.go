// restore.go:启动落点决策 —— 「重开后回到上次打开的工作区」。
//
// 问题(2026-10-05 用户反馈:「每次重新打开后,应默认显示的是上次打开的工作区」):
// 重开后总是回到用户 home,而不是上次干活的那个目录。根因是**启动 cwd 由拉起方式决定,
// 不由用户意图决定**:双击图标 / 开机自启 / 托盘重开拿到的都是用户 home(桌面壳的
// defaultWorkspace 正是这么兜底的 —— 它只能看 cwd,而 cwd 在这些路径下就是 home)。
//
// 口径(刻意只覆盖「不像意图」的情形,其余行为一个字不动):
//   - 判定「无意义 cwd」= 用户 home、数据根(gah-data/**)、文件系统根、系统目录;
//   - 只在无意义时恢复:从最近使用列表按时间倒序取第一个**仍可访问**的目录;
//   - 从某个项目目录里手工拉起 → cwd 就是意图,原样保留(哪怕那个目录恰好在 home 下面);
//   - 恢复失败(目录被删/进不去)→ 静默留在原处继续启动,不为一条脏历史拦住开机会。
//
// 为什么落在 Go 侧而不是桌面壳:工作区历史是 gah 自己的数据(workspaces.json),
// 让 Rust 去解析它的格式就多出第二套读法;而「当前进程 cwd 是什么」本来就在本插件的
// 职责边界内 —— 同一处事实源,不做跨语言猜测。
package hostcwdsessions

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// startDirIsMeaningless 判定目录是否「不像用户意图」。
//
// 数据根这一条要连**子目录**一起算:gah-data/sessions 之类是程序自己的数据,
// 从那儿开机会把会话日志当成工作区内容。
func startDirIsMeaningless(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return true
	}
	clean := filepath.Clean(dir)
	// 数据根及其任意子目录
	if home := sdk.Home(); home != "" {
		if h := filepath.Clean(home); clean == h || strings.HasPrefix(clean, h+string(filepath.Separator)) {
			return true
		}
	}
	// 用户 home 本身(注意:**只是判定**,不用它当数据根 —— 便携纪律里数据根唯一是 gah-data)
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		if clean == filepath.Clean(h) {
			return true
		}
	}
	// 文件系统根(Unix 的 "/"、Windows 的 "C:\")
	if filepath.Dir(clean) == clean {
		return true
	}
	// Windows 的 %SystemRoot%(C:\Windows)。桌面壳的 defaultWorkspace 也过滤了它,
	// 但 CLI 场景(计划任务/服务/桌面快捷方式直接跑 gah)不经那条链,这里补齐。
	if sys := os.Getenv("SystemRoot"); sys != "" && clean == filepath.Clean(sys) {
		return true
	}
	// macOS 的 /System(含子目录)与 /usr **本身**。为什么不写成 /usr/** :容器与 CI 里项目常落在
	// /usr/src/app、/usr/local/src/… 那类目录,通配会把用户的真实项目当成「系统目录」劫持走。
	// 精确匹配已经盖住真正该拦的(而 /usr 大多是指向 /System/Library 的软链)。
	if clean == "/usr" || strings.HasPrefix(clean, "/System") {
		return true
	}
	return false
}

// restoreLastWorkspace 无意义 cwd 时恢复到最近一次打开的工作区。
// 返回 (落点目录, 是否发生了恢复) —— 调用方据此决定要不要留痕。
//
// 必须在 recordProject **之前**调用:否则会把 home 记成「最近使用的项目」,
// 列表越用越脏,下一次恢复又会被这条脏记录带偏。
func restoreLastWorkspace(svc *Service, src string) (string, bool) {
	if svc == nil || !startDirIsMeaningless(src) {
		return "", false
	}
	cur := filepath.Clean(src)
	for _, p := range svc.RecentProjects() { // 已按最近使用时间倒序
		dir := strings.TrimSpace(p.Dir)
		if dir == "" {
			continue
		}
		if c := filepath.Clean(dir); c == cur {
			continue // 正是当前这个「无意义」目录本身,跳掉
		}
		if _, err := svc.SwitchDir(dir); err != nil {
			continue // 目录没了 / 进不去:试下一个,不为一条脏记录拦住启动
		}
		return dir, true
	}
	return "", false
}
