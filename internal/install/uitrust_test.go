// uitrust_test.go:批四 —— UI 侧的**默认闸**与它的放行出口。
//
// 这一批断掉了一条老路:「手工拷一个 UI 插件目录进 ui-plugins/ 就能用」。
// 换来的是:本项目最宽的面(UI 插件与宿主同源同权限)上终于有一道**默认**闸 +
// 一条明确放行命令 + 停用开关。断掉的那一刻必须在日志与面板各留一行说明 ——
// 否则用户会以为**坏了**。
package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
)

// 摆一个手工放置的 UI 插件(不走 InstallUI):目录 + manifest + 一个槽位产物。
func placeUIPlugin(t *testing.T, root, id string) []UISlot {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "manifest.json"),
		`{"id":"`+id+`","version":"1.0.0","slots":[{"name":"v1:extra-panel","priority":10,"module":"./dist/plugin.js"}]}`)
	writeFile(t, filepath.Join(dir, "dist", "plugin.js"), "console.log('hi')\n")
	man, err := readUIManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	return man.Slots
}

// TestEnsureUIListGateExists 闸必须在**没有装过任何 UI 插件**时就存在。
//
// 这正是老路的根因:那份清单**只有 InstallUI 会创建** ⇒ 从没装过的用户手工拷一个
// 目录进来 ⇒ 清单不存在 ⇒ `Load` 返回 Enforced=false ⇒ 放行。
func TestEnsureUIListGateExists(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ui-plugins")
	created, err := EnsureUIList(root)
	if err != nil || !created {
		t.Fatalf("应新建闸: created=%v err=%v", created, err)
	}
	l, err := plugintrust.Load(root)
	if err != nil {
		t.Fatalf("建出来的闸应能加载: %v", err)
	}
	if !l.Enforced() {
		t.Fatal("闸存在就必须是启用的 —— 否则形同虚设")
	}
	// 幂等
	created, err = EnsureUIList(root)
	if err != nil || created {
		t.Fatalf("第二次应幂等: created=%v err=%v", created, err)
	}
}

// TestTrustUIAllowsManualPlugin 手工放置的插件:闸挡住 → trust 后放行。
func TestTrustUIAllowsManualPlugin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ui-plugins")
	if _, err := EnsureUIList(root); err != nil {
		t.Fatal(err)
	}
	slots := placeUIPlugin(t, root, "manual")

	// ① 默认被拒(闸在,但没登记)
	l, err := plugintrust.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := l.Sum("manual"); ok {
		t.Fatal("还没 trust 就不该有登记")
	}
	if err := l.Verify("manual", mustHex("00")); err == nil {
		t.Fatal("未登记的插件应被闸挡住")
	}
	// ② trust 之后放行(登记的是**当前那一份**的 entry-scope 摘要)
	if err := TrustUI(root, "manual", nil); err != nil {
		t.Fatalf("trust 应成功: %v", err)
	}
	d, err := UIDigestEntry(filepath.Join(root, "manual"), slots)
	if err != nil {
		t.Fatal(err)
	}
	l, err = plugintrust.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cur, ok := l.Sum("manual"); !ok || cur != mustHex(d.Sum) {
		t.Errorf("登记的摘要应与验签口径同源: %v", l.Names())
	}
	if err := l.Verify("manual", mustHex(d.Sum)); err != nil {
		t.Errorf("trust 后应放行: %v", err)
	}
	// 审计来源标 trust-ui:manual(与「装的时候登记的」分开:两者信任语义不同)
	if a, ok := l.LastAuditOf("manual"); !ok || a.Source != "trust-ui:manual" {
		t.Errorf("审计来源不对: %+v", a)
	}
}

// TestTrustUIRefusesDifferentDigest 清单里已有**不同**摘要 ⇒ 显式拒绝,不自动洗白。
//
// 自动洗白一个对不上的摘要 = 把「文件被换掉了」这件事抹掉。
func TestTrustUIRefusesDifferentDigest(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ui-plugins")
	if _, err := EnsureUIList(root); err != nil {
		t.Fatal(err)
	}
	placeUIPlugin(t, root, "manual")
	if err := TrustUI(root, "manual", nil); err != nil {
		t.Fatal(err)
	}
	// 产物被改了,再 trust ⇒ 拒绝
	writeFile(t, filepath.Join(root, "manual", "dist", "plugin.js"), "console.log('swapped')\n")
	err := TrustUI(root, "manual", nil)
	if err == nil {
		t.Fatal("已有不同摘要时必须拒绝(不自动洗白)")
	}
	for _, want := range []string{"不同", "untrust-ui-plugin"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("拒绝文案应含 %q: %v", want, err)
		}
	}
}

// TestTrustUIRejectsIDMismatch 目录名与 manifest 的 id 不一致 ⇒ 拒绝。
//
// 登记哪一个都会让事后说不清是哪一个 —— 而这份清单正是事后唯一的依据。
func TestTrustUIRejectsIDMismatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ui-plugins")
	if _, err := EnsureUIList(root); err != nil {
		t.Fatal(err)
	}
	placeUIPlugin(t, root, "dir-name")
	writeFile(t, filepath.Join(root, "dir-name", "manifest.json"), `{"id":"other-id","slots":[]}`)
	err := TrustUI(root, "dir-name", nil)
	if err == nil {
		t.Fatal("id 不一致必须拒绝")
	}
	if !strings.Contains(err.Error(), "other-id") {
		t.Errorf("文案要说清两个 id: %v", err)
	}
}

// TestTrustUIIncludesSlotModules 合格线含**槽位声明的模块**,不只是 manifest.json。
//
// 只摘要 manifest 的话,「改 manifest 的槽位指向」就能换一个完全不同的模块进来 ——
// 那等于把闸做成形同虚设。测试把模块换掉、摘要必须跟着变。
func TestTrustUIIncludesSlotModules(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ui-plugins")
	if _, err := EnsureUIList(root); err != nil {
		t.Fatal(err)
	}
	slots := placeUIPlugin(t, root, "manual")
	d1, err := UIDigestEntry(filepath.Join(root, "manual"), slots)
	if err != nil {
		t.Fatal(err)
	}
	// 只改槽位产物(manifest 不动)⇒ 摘要必须变
	writeFile(t, filepath.Join(root, "manual", "dist", "plugin.js"), "console.log('v2')\n")
	d2, err := UIDigestEntry(filepath.Join(root, "manual"), slots)
	if err != nil {
		t.Fatal(err)
	}
	if d1.Sum == d2.Sum {
		t.Fatal("槽位产物变了摘要却没变 —— 那等于没纳入校验范围")
	}
}

// TestTrustUIEmptyID 缺 id 显式报错。
func TestTrustUIEmptyID(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ui-plugins")
	if err := TrustUI(root, "  ", nil); err == nil {
		t.Fatal("缺 id 应报错")
	}
}

// TestMarkOfficial 官方件标 official;**不抢**同名已装插件的条目。
func TestMarkOfficial(t *testing.T) {
	home := t.TempDir()
	var ledger SourceLedger
	// 用户自己装了一个同名插件
	ledger.Record(SourceEntry{PluginID: "tool-kit", Repo: "github.com/a/b", Kind: KindTag, Origin: OriginUser})
	if err := WriteSources(home, &ledger); err != nil {
		t.Fatal(err)
	}
	if err := MarkOfficial(home, []string{"tool-kit", "tool-basic"}); err != nil {
		t.Fatal(err)
	}
	back, err := LoadSources(home)
	if err != nil {
		t.Fatal(err)
	}
	kit, _ := back.Find("tool-kit")
	if kit.Origin != OriginUser || kit.Repo != "github.com/a/b" {
		t.Errorf("不该抢用户自己装的同名插件: %+v", kit)
	}
	basic, ok := back.Find("tool-basic")
	if !ok || basic.Origin != OriginOfficial {
		t.Errorf("官方件应标 official: %+v", basic)
	}
	// 幂等
	if err := MarkOfficial(home, []string{"tool-basic"}); err != nil {
		t.Fatal(err)
	}
	back2, _ := LoadSources(home)
	if n := len(back2.All()); n != 2 {
		t.Errorf("重复标注不应新增条目: %d", n)
	}
}

// TestUIUntrustLastEntryKeepsGate 撤销最后一个登记后,闸**必须仍然关着**。
//
// 这条是给一个真漏洞立的(2026-10-03 验证阶段查出):旧的写入侧在条目清空时**删文件**,
// 而 Load 对不存在的文件返回「未启用」⇒ **撤销最后一条登记 = 把闸静默关掉**,
// 手工放置的 UI 插件立刻又能加载,直到下次 boot 重建闸。窗口静默、且取决于何时重启。
func TestUIUntrustLastEntryKeepsGate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "ui-plugins")
	if _, err := EnsureUIList(root); err != nil {
		t.Fatal(err)
	}
	placeUIPlugin(t, root, "demo")
	if err := TrustUI(root, "demo", nil); err != nil {
		t.Fatal(err)
	}
	if err := UIUntrust(root, "demo"); err != nil {
		t.Fatal(err)
	}
	l, err := plugintrust.Load(root)
	if err != nil {
		t.Fatalf("撤销后清单读不出来: %v", err)
	}
	if !l.Enforced() {
		t.Fatal("撤销唯一一条登记后闸被静默关掉了 —— 手工放置的 UI 插件又能直接加载")
	}
	if err := l.Verify("demo", [32]byte{0}); err == nil {
		t.Fatal("闸关着时,已撤销登记的插件必须仍被拒")
	}
}
