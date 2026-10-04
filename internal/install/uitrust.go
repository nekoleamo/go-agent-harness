// UI 插件的完整性闸(2026-10-03)。
//
// 为什么要给 UI 插件也加:它是**与宿主同源同权限**的面(产物经动态 import() 进主页面,
// 能调全部 API 含工具执行)。此前它只有一个「展示用」的 sha256 —— 加载与否与它无关。
// 那是本项目最宽的一个口,而同一批里的进程型插件已经有哈希白名单了。
//
// 口径与进程型**同构**:`ui-plugins/SHA256SUMS` 存在即强制,未列入/不符 ⇒ 不下发
// (前端完全看不到它,而不是「看到了但摘要不符」)。缺省不存在 = 现状;`gah -install-ui`
// 会首次创建它 ⇒ 装过 UI 插件之后即强制。
//
// **合格线是 entry scope,不是 full**:full 要求哈希目录里每个文件,而 UI 插件常带
// sourcemap/大 chunk,算全量既费预算又与"实际会被执行的代码"不精确对应。
// entry = manifest 声明会被 import 的那些模块 + manifest 本身 —— 后者必须在内:
// 它定义槽位指向,漏了它就能靠改指向绕过校验。
package install

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
)

// 摘要只需要槽位的 module 字段(复用 uiinstall.go 已有的 UISlot —— 两处不能各定义一份,
// 否则「读的和摘要的」迟早指向不同字段)。

// UIEntryDigest entry scope 的摘要结果。
type UIEntryDigest struct {
	Sum    string // 64 位 hex
	Files  int    // 参与摘要的文件数
	Missed []string
}

// UIDigestEntry 按「manifest.json + 槽位声明的模块」算摘要(顺序稳定:先 manifest 后按序模块)。
//
// 刻意**不**遍历目录:遍历会把 sourcemap/无关 chunk 也算进来,于是「作者重新打了个包、
// 无关文件变了」这种与执行无关的变动也会触发拒绝 —— 那是把闸门做得让人绕过去。
func UIDigestEntry(dir string, slots []UISlot) (UIEntryDigest, error) {
	files := []string{"manifest.json"}
	seen := map[string]bool{"manifest.json": true}
	for _, sl := range slots {
		m := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(sl.Module)), "./")
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		files = append(files, m)
	}
	sort.Strings(files[1:])
	var out UIEntryDigest
	h := sha256.New()
	for _, rel := range files {
		raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			out.Missed = append(out.Missed, rel)
			continue
		}
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(raw))
		h.Write(raw)
		out.Files++
	}
	out.Sum = hex.EncodeToString(h.Sum(nil))
	if len(out.Missed) > 0 {
		return out, fmt.Errorf("install-ui: 摘要失败,缺少声明的模块:%s", strings.Join(out.Missed, ","))
	}
	if out.Files == 0 {
		return out, fmt.Errorf("install-ui: 摘要失败:没有可摘要的文件")
	}
	return out, nil
}

// UITrust 登记一个 UI 插件的摘要(装完调用)。dir = ui-plugins 根。
func UITrust(uiRoot, id string, slots []UISlot) error {
	dir := filepath.Join(uiRoot, id)
	d, err := UIDigestEntry(dir, slots)
	if err != nil {
		return err
	}
	list, err := loadUIList(uiRoot)
	if err != nil {
		return err
	}
	return list.RecordWithAudit(id, mustHex(d.Sum), "ui-install:"+id)
}

// UIUntrust 从 UI 白名单移除。
func UIUntrust(uiRoot, id string) error {
	list, err := loadUIList(uiRoot)
	if err != nil {
		return err
	}
	return list.Remove(strings.TrimSpace(id))
}

// UIList 已登记的 UI 插件 id(排序)。
func UIList(uiRoot string) ([]string, error) {
	list, err := loadUIList(uiRoot)
	if err != nil {
		return nil, err
	}
	return list.Names(), nil
}

// loadUIList 打开 ui-plugins 根下的白名单。
func loadUIList(uiRoot string) (*plugintrust.List, error) {
	return plugintrust.Load(uiRoot)
}

func mustHex(s string) [32]byte {
	var out [32]byte
	b, _ := hex.DecodeString(s)
	copy(out[:], b)
	return out
}

// EnsureUIList **无条件**建 UI 侧的完整性闸(批四 §A.2)。幂等;已存在则什么都不做。
//
// 为什么 UI 侧必须这样,而进程型侧不需要:进程型有 embed 每次启动登记的**官方四件**,
// 所以 `plugins/SHA256SUMS` 必然存在 ⇒ 手工放进去的进程型插件必被白名单拒。
// 而 **UI 侧根本没有官方插件**,「像进程型那样每次 boot 登记官方件」这条路不存在;
// 原先那份清单**只有 InstallUI 会创建** ⇒ 从没装过 UI 插件的用户手工拷一个目录进去,
// 清单不存在 ⇒ `Load` 返回 Enforced=false ⇒ **放行**。
//
// 而 UI 插件是**与宿主同源同权限**的最宽面(产物经动态 import() 进主页面,能调全部 API
// 含工具执行)。"本项目最宽的面上一道默认闸"这件事,只能靠无条件建闸来实现。
func EnsureUIList(uiRoot string) (created bool, err error) {
	return plugintrust.EnsureEmpty(uiRoot)
}

// TrustUI 给**已经放在 ui-plugins/ 里**的插件补登记(放行出口,批四 §A.3)。
//
// 与 `-trust-plugin` 同款形状,一条都不能少:
//   - **显式执行**,绝不自动;
//   - 只登记**当前那一份**的 entry-scope 摘要(用 UIDigestEntry,与验签口径同源 ——
//     「同一句话、同一口径」是作用域口径原则的第一次真实应用);
//   - 清单里已有**不同**摘要 ⇒ 显式拒绝,不自动洗白(文件可能已被改动)。
//
// 「登记成功」即等价于「你看过这一份」—— 除此以外不做任何推断(没装过这个 id 就登记 id,
// 不问它从哪来、是谁写的)。
func TrustUI(uiRoot, id string, slots []UISlot) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("trust-ui-plugin: 缺少 UI 插件 id")
	}
	dir := filepath.Join(uiRoot, id)
	man, err := readUIManifest(dir)
	if err != nil {
		return fmt.Errorf("trust-ui-plugin: %s 下没有可读的 manifest.json(%s)", uiRoot, id)
	}
	if man.ID != "" && man.ID != id {
		return fmt.Errorf("trust-ui-plugin: 目录名是 %s,但 manifest 里的 id 是 %s。"+
			"两边不一致时登记哪一个都会让事后说不清是哪一个 —— 先把目录名改成 manifest 的 id", id, man.ID)
	}
	slots = mergeDeclaredSlots(man, slots)
	d, err := UIDigestEntry(dir, slots)
	if err != nil {
		return err
	}
	list, err := plugintrust.Load(uiRoot)
	if err != nil {
		return err
	}
	if cur, ok := list.Sum(id); ok && cur != mustHex(d.Sum) {
		return fmt.Errorf("trust-ui-plugin: %s 在白名单里已有**不同**的摘要(文件可能已被改动)。"+
			"确认这就是你要的版本后,先 gah -untrust-ui-plugin %s 再执行本命令", id, id)
	}
	// 审计来源标 trust-ui:manual:与「装的时候登记的」分开 —— 两者信任语义不同
	// (后者是你看着一份现成的前端代码点的头)。
	return list.RecordWithAudit(id, mustHex(d.Sum), "trust-ui:manual")
}

// mergeDeclaredSlots 用 manifest 自己声明的槽位补全(显式传入的优先)。
//
// 为什么要补:trust 的合格线是「manifest + 槽位声明的模块」。若调用方没读 manifest
// 就来登记,只能登记 manifest 一个文件 —— 那等于**给一个改槽位指向就能绕过校验的插件放行**。
// 与 uidigest 把 manifest.json 算进去是同一个道理(它定义槽位指向)。
func mergeDeclaredSlots(man UIManifest, slots []UISlot) []UISlot {
	seen := map[string]bool{}
	for _, s := range slots {
		seen[s.Module] = true
	}
	out := append([]UISlot(nil), slots...)
	for _, sl := range man.Slots {
		if !seen[sl.Module] {
			out = append(out, sl)
		}
	}
	return out
}

// UIPluginSlots 读一个已放置的 UI 插件的槽位声明(供 trust 命令用)。
//
// 为什么要显式读:合格线是「manifest + 槽位声明的模块」。少读槽位 = 给一个
// 「改 manifest 的槽位指向就能绕过校验」的插件放行 —— 那等于把闸做成形同虚设。
func UIPluginSlots(uiRoot, id string) ([]UISlot, error) {
	man, err := readUIManifest(filepath.Join(uiRoot, strings.TrimSpace(id)))
	if err != nil {
		return nil, fmt.Errorf("trust-ui-plugin: %s 下没有可读的 manifest.json: %w", id, err)
	}
	return man.Slots, nil
}
