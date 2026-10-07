// pruneObsolete 的三条边界(2026-10-07 用户真机带出来的缺陷)。
//
// 起因:升级只加不减时,**旧版本官方插件永远留在数据根里**,与新版重复提供同名工具,
// 而先注册的旧插件先上场。用户症状是「v0.5.6 已支持 anysearch,面板却报未知 provider
// "anysearch"(可选: exa)」—— 版本、哈希、自检全绿,没有任何一处能自证「提供 web_search
// 的是升级前的旧插件」。
package embed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 建一个装好的插件目录:写入可执行文件 + SHA256SUMS(含 audit 行)。
func mkPlugin(t *testing.T, home, name, source, hash string) string {
	t.Helper()
	dir := filepath.Join(home, "plugins", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("binary-"+name+hash), 0o755); err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(home, "plugins", "SHA256SUMS")
	line := hash + "  " + name + "\n# audit: 2026-10-01T00:00:00Z " + source + " " + name + " " + hash + "\n"
	f, err := os.OpenFile(sums, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
	return dir
}

// 旧官方插件(不再随包)被移走;仍随包的不动;用户自装的不动。
func TestPruneObsoleteOnlyTouchesEmbed(t *testing.T) {
	home := t.TempDir()
	oldOfficial := mkPlugin(t, home, "tool-basic", "embed", strings.Repeat("a", 64))
	kept := mkPlugin(t, home, "tool-kit", "embed", strings.Repeat("b", 64))
	userInstalled := mkPlugin(t, home, "my-own", "trust:manual", strings.Repeat("c", 64))

	// 当前版本仍随包:tool-kit;不再随包:tool-basic
	want := map[string][32]byte{"tool-kit": {}}

	got := pruneObsolete(home, want)
	if len(got) != 1 || !strings.Contains(got[0], "tool-basic") {
		t.Fatalf("应只移走不再随包的 tool-basic,实得 %v", got)
	}
	if _, err := os.Stat(oldOfficial); !os.IsNotExist(err) {
		t.Fatalf("旧插件目录应被移走: %v", err)
	}
	// 移走而不是删除:落在 .obsolete-<hash8> 旁边,用户能自己看/放回
	entries, _ := os.ReadDir(filepath.Join(home, "plugins"))
	var moved bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tool-basic.obsolete-") {
			moved = true
		}
	}
	if !moved {
		t.Fatalf("应留一份 .obsolete-* 供回滚,实际目录:%v", entries)
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("仍随包的插件不该动: %v", err)
	}
	if _, err := os.Stat(userInstalled); err != nil {
		t.Fatalf("用户自己装的插件**永远**不该动: %v", err)
	}
}

// **读不到清单**时什么都不做 —— 清单是唯一的「这东西当初从哪来」依据,没有它就
// 区分不出官方随包与用户自装,此时动任何插件都是凭猜测。
//
// 注意与「清单可读但 want 为空」区分:后者是**明确**的「当前版本不带它们」,该移走。
func TestPruneObsoleteUnreadableListIsNoop(t *testing.T) {
	home := t.TempDir()
	// 只有插件目录、没有 SHA256SUMS(清单损坏/被手删都长这样)
	dir := filepath.Join(home, "plugins", "tool-basic")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tool-basic"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := pruneObsolete(home, map[string][32]byte{}); len(got) != 0 {
		t.Fatalf("清单读不到时必须不动任何东西,实得 %v", got)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("插件目录不该被动:%v", err)
	}
}

// 清单可读且当前版本确实不带它们 ⇒ 移走(这是 pruneObsolete 的正向行为)。
func TestPruneObsoleteEmptyWantMovesAllEmbed(t *testing.T) {
	home := t.TempDir()
	mkPlugin(t, home, "tool-basic", "embed", strings.Repeat("a", 64))

	got := pruneObsolete(home, map[string][32]byte{})
	if len(got) != 1 {
		t.Fatalf("want 为空(当前版本不带任何官方插件)应移走已登记的 embed 插件,实得 %v", got)
	}
}
