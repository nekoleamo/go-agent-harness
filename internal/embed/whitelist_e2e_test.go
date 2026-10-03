package embed

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/plugintrust"
)

// 关键回归:发行形态是**一个 tool-kit 提供四个角色**(2026-10-02 瘦身)⇒ 白名单登记的键
// 必须与 host-bridge 校验时用的键(**文件基名**)一致,否则官方插件会被自己拒掉 ——
// 而那意味着升级后 gah 的工具整组消失,且日志只有一行哈希不符。
// 这条是「登记键」与「校验键」必须同源的唯一护栏。
func TestEnsurePluginsRegistersSelfInWhitelist(t *testing.T) {
	home := t.TempDir()
	if _, err := EnsurePlugins(home); err != nil {
		t.Fatalf("EnsurePlugins: %v", err)
	}
	dir := filepath.Join(home, "plugins")
	list, err := plugintrust.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !list.Enforced() {
		t.Fatal("释放后应当有白名单(即强制态)")
	}
	// 盘上每个插件二进制都必须在清单里且哈希一致
	bad := 0
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if plugintrust.Path(dir) == p {
			return nil
		}
		sum, herr := plugintrust.HashFile(p)
		if herr != nil {
			return nil
		}
		if verr := list.Verify(d.Name(), sum); verr != nil {
			t.Errorf("官方插件被自己拒掉: %s: %v", d.Name(), verr)
			bad++
		}
		return nil
	})
	if bad > 0 {
		t.Fatalf("%d 个官方插件未通过白名单校验", bad)
	}
	t.Logf("白名单登记:%v;盘上条目全部通过", list.Names())
}
