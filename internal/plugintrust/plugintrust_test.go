// 插件哈希白名单单测:三条语义(缺省不启用 / 强制时未列入与不符都拒 / 清单坏了不放行)。
package plugintrust

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAbsentMeansNotEnforced 清单不存在 ⇒ 不启用(加载行为与从前完全一致)。
//
// 这条是「机制被架空」的风险点:白名单建立在清单上,清单没了还继续放行等于它不存在。
// 但反过来,清单**默认不存在**(opt-in)—— 由 embed 首次登记时创建。
func TestAbsentMeansNotEnforced(t *testing.T) {
	l, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if l.Enforced() {
		t.Fatal("清单不存在时不该是强制态")
	}
	var sum [32]byte
	if err := l.Verify("tool-basic", sum); err != nil {
		t.Fatalf("未启用时应放行: %v", err)
	}
}

// TestEnforcedRejectsUnknownAndMismatch 启用后:未列入 / 哈希不符,都要拒,且文案带补救办法。
func TestEnforcedRejectsUnknownAndMismatch(t *testing.T) {
	dir := t.TempDir()
	want := [32]byte{1, 2, 3}
	l := &List{dir: dir, enforced: true, sums: map[string][32]byte{"tool-known": want}}

	// 未列入
	err := l.Verify("tool-other", want)
	if err == nil {
		t.Fatal("未列入的插件必须被拒")
	}
	if got := err.Error(); !contains(got, "-trust-plugin tool-other") {
		t.Fatalf("文案应含可直接执行的补救命令: %q", got)
	}

	// 哈希不符
	err = l.Verify("tool-known", [32]byte{9})
	if err == nil {
		t.Fatal("哈希不符必须被拒")
	}
	if got := err.Error(); !contains(got, "已被改动") {
		t.Fatalf("文案应说明是文件被改动: %q", got)
	}

	// 一致 ⇒ 放行
	if err := l.Verify("tool-known", want); err != nil {
		t.Fatalf("一致应放行: %v", err)
	}
}

// TestBrokenManifestFailsClosed 坏行 / 空清单 ⇒ **报错**,不退化成「未启用」。
func TestBrokenManifestFailsClosed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, FileName, "这不是一行两字段\n")
	if _, err := Load(dir); err == nil {
		t.Fatal("坏行应显式报错")
	}

	dir2 := t.TempDir()
	write(t, dir2, FileName, "# 只有注释\n\n")
	if _, err := Load(dir2); err == nil {
		t.Fatal("空清单应报错(否则等于无声的全面放行)")
	}
}

// TestRecordRemoveRoundTrip 登记/移除往返,格式与发行侧那份一致(`<64hex>  <名>`)。
func TestRecordRemoveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	l := &List{dir: dir, sums: map[string][32]byte{}}
	sum := [32]byte{0xab}
	if err := l.Record("tool-x", sum); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(raw), "  tool-x") || !contains(string(raw), "ab") {
		t.Fatalf("清单形态不符(应与发行侧同格式):\n%s", raw)
	}
	// 重新读回来
	l2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := l2.Sum("tool-x"); !ok || got != sum {
		t.Fatalf("往返不一致: %+v ok=%v", got, ok)
	}
	// 移除后不再列出
	if err := l2.Remove("tool-x"); err != nil {
		t.Fatal(err)
	}
	if names := l2.Names(); len(names) != 0 {
		t.Fatalf("移除后应为空: %v", names)
	}
	// 移除不存在的条目:幂等
	if err := l2.Remove("tool-nope"); err != nil {
		t.Fatalf("移除不存在的条目应幂等: %v", err)
	}
}

// TestSetKeepsExistingEntries Set(embed 登记自己)必须保留用户已有的条目。
func TestSetKeepsExistingEntries(t *testing.T) {
	dir := t.TempDir()
	l := &List{dir: dir, sums: map[string][32]byte{}}
	if err := l.Record("tool-user", [32]byte{7}); err != nil {
		t.Fatal(err)
	}
	mine := [32]byte{0x11}
	if err := l.Set([]string{"tool-kit"}, func(n string) ([32]byte, bool) {
		return mine, n == "tool-kit"
	}); err != nil {
		t.Fatal(err)
	}
	l2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := l2.Sum("tool-user"); !ok || got != [32]byte{7} {
		t.Fatal("Set 不该动用户已有条目")
	}
	if got, ok := l2.Sum("tool-kit"); !ok || got != mine {
		t.Fatal("Set 应写入传入的条目")
	}
}

// TestHashFileMatches 哈希算法就是 sha256(与发行侧清单同一种,否则两套对不上)。
func TestHashFileMatches(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// echo -n abc | shasum -a 256 = ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
	if hexOf(got) != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("哈希不符: %s", hexOf(got))
	}
}

func hexOf(s [32]byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 64)
	for _, b := range s {
		out = append(out, digits[b>>4], digits[b&0x0f])
	}
	return string(out)
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
