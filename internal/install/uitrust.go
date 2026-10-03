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
