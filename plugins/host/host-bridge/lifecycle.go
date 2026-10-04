// lifecycle.go:外部插件的**启用 / 停用 / 卸载**(批二,2026-10-03)。
//
// 为什么单独一个文件:这三件事共享同一组不变量(按**二进制**而不是按角色停用、
// 停用≠卸载、重启要重验漂移、并发要与 reloadMu 串行),散进 bridge.go 迟早有一处漏掉。
//
// 本批要补的三个洞(逐条对照现状):
//
//	① sdk.ExternalPlugins 此前**只有 Reload** ⇒ 没有任何运行时手段关掉一个行为不良的插件。
//	   在"不验签名、后果自担"的模型下,启停就是用户的止损手段,不是锦上添花。
//
//	② **删二进制不会卸载运行中的进程**:watcher 只监听 Write|Create|Rename,Remove 被
//	   显式排除(注释"删除由 plugin-manager 决定"),而外部插件这条路**没有 plugin-manager
//	   介入** ⇒ 用户 `rm` 掉文件,进程继续跑(持工具注册 + 回调 token)直到 gah 重启。
//	   「我删了它」给人一个**虚假的安全感**。watcher 侧见 core/plugin/watch.go。
//
//	③ pluginPath 多候选歧义叠加其上 ⇒ “清理插件”同时撞三个坑。停用有了明确路径后,
//	   歧义只影响“重载/启停”这一条边(见 pluginPath 的报错文案)。
//
// **本文件刻意不提供「删文件」的入口**:删除的**顺序**是承重的(先停进程再删),而它属于
// internal/install.Uninstall —— 它同时要管白名单与来源账两个条目,拆开就会漏。用户手
// `rm` 的那一条路由 watcher 的 Remove 事件 → onWatchEvent → unloadPath 接住。
// 两处不写第二份删除实现。
package hostbridge

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/install"
	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// disabledNow 这个二进制当前是否被用户停用。
//
// **现读**而不缓存:停用可能来自面板(`/install` 也在跑)或另一个 gah 实例写偏好,
// 缓存过期会变成「我明明点了停用,插件还在」—— 而那正是止损手段最不能失效的时刻。
func (b *Bridge) disabledNow(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if prefs.IsExternalDisabled(name) {
		return true
	}
	// 与 disk 一致地忽略平台扩展名:偏好里存的是基名。
	return prefs.IsExternalDisabled(strings.TrimSuffix(name, ".exe"))
}

// Disable 停用(name = 二进制基名,如 "tool-kit")。实现 sdk.ExternalPlugins。
//
// 顺序:**先撤注册与停进程,再落偏好**。反过来会有一个窗口 —— 进程已经停了但偏好没写,
// 重启 gah 后它又自己回来了,而用户以为自己已经停用它。
//
// 「停用 ≠ 卸载」的后果必须在这条路径上讲清:文件、白名单条目、来源账条目**都留着**
// ⇒ 重新启用**不需要重新 trust**(哈希仍匹配)。
func (b *Bridge) Disable(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("host-bridge: 缺少外部插件名")
	}
	path, err := b.pluginPath(name)
	if err != nil {
		return err
	}
	bin := externalPluginName(path)
	// 幂等:停用一个没在跑的插件不是错误(用户点的是"我不要它跑",不是"它必须在跑")。
	b.unloadPath(path)
	prefs.SetExternalDisabled(bin, true)
	b.logInfo("host-bridge: 外部插件已停用(文件、白名单与来源账条目都留着;重新启用无需重新登记)",
		"name", bin, "path", path)
	return nil
}

// Enable 启用(name = 二进制基名)。实现 sdk.ExternalPlugins。
//
// 先重验漂移(批二 §2.5):停用半年后回来,作者可能已经把 tag 换了 —— 不重验的话,
// "停用"就成了绕过漂移守卫的后门。
//
// 判定的**方向**要说清:这里比的不是"盘上这份 vs 账上那份"(那份由 verifyTrust 的
// 哈希白名单管),而是"**账上记的那个 ref 现在指向哪里**"。一个几个月前的二进制对着一个
// 已经被 force-push 过的 tag 重新上线,正是要拦的那件事。
//
// 问不到远端(离线/仓库删了)⇒ **放行**并如实说明:判不出漂移 ≠ 有漂移,
// 因为它而拒绝,会让离线用户永远开不了插件。
func (b *Bridge) Enable(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("host-bridge: 缺少外部插件名")
	}
	path, err := b.pluginPath(name)
	if err != nil {
		return err
	}
	bin := externalPluginName(path)
	if note := b.driftOnEnable(bin, path); note != "" {
		return fmt.Errorf("%s", note)
	}
	// 先清偏好再加载:加载路径里 loadEntries/loadOne 都会查"是否停用",
	// 顺序反过来会出现"清偏好 → 加载 → 又被自己判成停用"的自我阻塞。
	prefs.SetExternalDisabled(bin, false)
	if err := b.reload(path, roleOf(name, path)); err != nil {
		// 加载失败:把偏好**放回去**。停在"没启用也没停用"这个中间态会让用户以为
		// 插件启用了却不出工具 —— 而真实原因是启动失败,该由日志与重试来表达。
		prefs.SetExternalDisabled(bin, true)
		if errors.Is(err, errPluginIdle) {
			return nil // 自述空闲不是失败(与 Reload 同款处理)
		}
		return fmt.Errorf("host-bridge: 启用 %s 失败(详见日志): %w", name, err)
	}
	return nil
}

// driftOnEnable 启用前的漂移判定;返回空串 = 可以启用。
//
// 关联来源账的键是**插件 id**,而这里只有二进制名 —— 用落位目录名当 id 找(安装器按
// `<home>/plugins/<id>/` 落位,与二进制的父目录名一致)。找不到账 ⇒ 不判定(不是拒绝):
// 手工放置的插件压根没有来源账这一说。
func (b *Bridge) driftOnEnable(bin, path string) string {
	entry, ok := b.sourceEntryOf(path)
	if !ok {
		return ""
	}
	// 只有 tag 才是承诺;branch/default 移动是正常的,拦它等于逼所有人打 tag。
	if entry.Kind != install.KindTag || entry.Repo == "" || entry.Ref == "" {
		return ""
	}
	remote, err := install.RemoteSHAOf(entry)
	if err != nil || remote == "" {
		b.logInfo("host-bridge: 启用前问不到远端,跳过漂移判定(判不出漂移 ≠ 有漂移)",
			"name", bin, "repo", entry.Repo, "ref", entry.Ref, "err", err)
		return ""
	}
	if remote == entry.Commit {
		return ""
	}
	return fmt.Sprintf("host-bridge: 拒绝启用 %s —— 它当初装的是 %s@%s(%s),而这个 tag 现在指向 %s。"+
		"tag 被 force-push 有两种可能:作者重推了这一版,或仓库/账号易手。"+
		"确认要换成作者现在这一版:重新执行 gah -install %s@%s(并按需加 --accept-drift);"+
		"要留在原版:在 %s 里指定原先那个 commit sha",
		bin, entry.Repo, entry.Ref, install.ShortSHA(entry.Commit), install.ShortSHA(remote),
		entry.Repo, entry.Ref, entry.Repo)
}

// sourceEntryOf 由二进制路径找它的来源账条目(经父目录名 = 安装时的插件 id)。
func (b *Bridge) sourceEntryOf(path string) (install.SourceEntry, bool) {
	home := pluginHome()
	if home == "" {
		return install.SourceEntry{}, false
	}
	id := filepath.Base(filepath.Dir(path))
	ledger, err := install.LoadSources(home)
	if err != nil {
		return install.SourceEntry{}, false // 账坏了:不判定(与列表面的报错口径各自成立)
	}
	return ledger.Find(id)
}

// List 外部插件清单(名字/路径/角色/是否停用/来源)。实现 sdk.ExternalPlugins。
//
// 列出的是**宿主认得的**外部插件(已加载的 + 盘上在的),而不是"目录里有什么文件"——
// 用户要的是"我装的东西现在什么状态",不是一份 ls。
func (b *Bridge) List() []sdk.ExternalPluginInfo {
	type agg struct {
		info sdk.ExternalPluginInfo
		seen map[string]bool
	}
	byName := map[string]*agg{}

	add := func(path, role string, loaded bool) {
		bin := externalPluginName(path)
		a, ok := byName[bin]
		if !ok {
			a = &agg{info: sdk.ExternalPluginInfo{
				Name:     bin,
				Path:     path,
				Disabled: b.disabledNow(bin),
				Source:   b.sourceSummary(path),
			}, seen: map[string]bool{}}
			byName[bin] = a
		}
		if role != "" && !a.seen[role] {
			a.seen[role] = true
			a.info.Roles = append(a.info.Roles, role)
		}
		a.info.Loaded = a.info.Loaded || loaded
	}

	b.mu.RLock()
	for _, e := range b.entries {
		if e == nil {
			continue
		}
		add(e.path, e.role, true)
	}
	b.mu.RUnlock()

	// 盘上在但没加载的(停用的 / 上次加载失败的)也要列出来 —— 「我停用的那个」
	// 在清单里消失,等于用户以为自己停错了。
	for _, p := range b.scanCandidates() {
		add(p, "", false)
	}
	// 被拒的补上原因(否则「没在跑」有两种读法:停用 vs 被拦)。
	b.mu.RLock()
	for _, r := range b.rejects {
		if a, ok := byName[strings.TrimSuffix(r.Name, ".exe")]; ok {
			a.info.Reject = r.Reason
		}
	}
	b.mu.RUnlock()

	out := make([]sdk.ExternalPluginInfo, 0, len(byName))
	for _, a := range byName {
		sort.Strings(a.info.Roles)
		out = append(out, a.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sourceSummary 来源一格(面板/CLI 显示用):`github.com/a/b@v1.2.0(tag · 9f2c1ab3e5f7)`。
func (b *Bridge) sourceSummary(path string) string {
	e, ok := b.sourceEntryOf(path)
	if !ok {
		return ""
	}
	cell := e.Repo
	if e.Ref != "" {
		cell += "@" + e.Ref
	}
	cell += "(" + e.Kind
	if e.Commit != "" {
		cell += " · " + install.ShortSHA(e.Commit)
	}
	if e.Drifted {
		cell += " · 会移动"
	}
	return cell + ")"
}

// unloadPath 停掉某个二进制下的**全部**条目(逐角色 unreg + kill + 删条目)。
//
// 为什么按路径而不是按角色:停用是「我不要这一件插件跑」,而一个二进制可能同时
// 持着工具注册与回调 token —— 只停一个角色会剩下一个握着工具的进程,用户完全看不出来。
func (b *Bridge) unloadPath(path string) int {
	b.reloadMu.Lock()
	defer b.reloadMu.Unlock()
	b.mu.Lock()
	var victims []*extEntry
	for key, e := range b.entries {
		if e == nil {
			continue
		}
		if e.path != path && keyPathOf(key, e) != path {
			continue
		}
		victims = append(victims, e)
		delete(b.entries, key)
	}
	b.mu.Unlock()
	for _, e := range victims {
		e.unreg()
		e.kill()
	}
	return len(victims)
}

// scanCandidates 盘上所有像插件的二进制(用于 List 的"没加载的也要列")。
func (b *Bridge) scanCandidates() []string {
	var out []string
	_ = filepath.WalkDir(b.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isExternalPluginBin(d.Name()) {
			return nil //nolint:nilerr // 读不到这一项就跳过,不影响其余
		}
		out = append(out, path)
		return nil
	})
	sort.Strings(out)
	return out
}
