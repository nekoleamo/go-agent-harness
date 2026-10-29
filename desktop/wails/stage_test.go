package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFile 测试辅助:写一个文件(建目录)。
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestStageSidecarReuseAndPrune 钉住两条真机换来的人不变量:
//  1. 同一份源**第二次不再复制**(staged=false)—— 省一次 30-45MB 复制;
//  2. 历史副本被清掉 —— 按内容寻址每次升级一个新文件,不清会一直堆。
//
// 反向验证:把「同名同大小 ⇒ 复用」改回「总是复制」,第 3 条断言必红;
// 去掉 pruneStale 调用,第 5 条断言必红。
func TestStageSidecarReuseAndPrune(t *testing.T) {
	home := t.TempDir()
	src := filepath.Join(home, "src", "gah")
	writeFile(t, src, "sidecar-binary-v1")

	p1, err := stageSidecar(src, home, "0.7.4")
	if err != nil {
		t.Fatalf("首次外置失败: %v", err)
	}
	if !p1.staged || !p1.external {
		t.Fatalf("首次应 staged=true external=true,得到 %+v", p1)
	}
	if want := dataRoot(home); p1.dataRoot != want {
		t.Fatalf("数据根 = %q, want %q", p1.dataRoot, want)
	}
	if got, _ := os.ReadFile(p1.bin); string(got) != "sidecar-binary-v1" {
		t.Fatalf("副本内容不对: %q", got)
	}

	p2, err := stageSidecar(src, home, "0.7.4")
	if err != nil {
		t.Fatal(err)
	}
	if p2.staged {
		t.Fatal("同一份源应复用,不该再复制一次")
	}
	if p2.bin != p1.bin {
		t.Fatalf("复用应指向同一个副本: %q vs %q", p2.bin, p1.bin)
	}

	// 塞一个旧版本副本 + 一个 .new 残留,下一次外置应把它们清掉。
	stale := filepath.Join(home, "bin", "gah-0.7.3-deadbeef")
	writeFile(t, stale, "old")
	writeFile(t, p1.bin+".new", "half")

	// 源变了(内容 + mtime 都可能变)⇒ 新名字 + staged=true。
	writeFile(t, src, "sidecar-binary-v2-longer")
	p3, err := stageSidecar(src, home, "0.7.4")
	if err != nil {
		t.Fatal(err)
	}
	if !p3.staged || p3.bin == p1.bin {
		t.Fatalf("内容变了应换名字并重新复制,得到 %+v", p3)
	}
	for _, gone := range []string{stale, p1.bin, p1.bin + ".new"} {
		if _, err := os.Stat(gone); !isErrNotExist(err) {
			t.Fatalf("历史文件未被清理: %s(err=%v)", gone, err)
		}
	}
}

// TestMigrateFromCopiesOnce 旧位置(应用目录内)的数据一次性搬到外置根:
// 只复制不删除;新位置已有数据时绝不合并(覆盖等于丢数据)。
func TestMigrateFromCopiesOnce(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(t.TempDir(), "gah-data")
	writeFile(t, filepath.Join(legacy, "sessions", "a.jsonl"), "log")
	writeFile(t, filepath.Join(legacy, "config", "web.yaml"), "addr: x")

	moved, err := migrateFrom(legacy, home)
	if err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if moved != dataRoot(home) {
		t.Fatalf("迁移落地 = %q, want %q", moved, dataRoot(home))
	}
	if got, _ := os.ReadFile(filepath.Join(dataRoot(home), "sessions", "a.jsonl")); string(got) != "log" {
		t.Fatalf("迁移后内容不对: %q", got)
	}
	if _, err := os.Stat(filepath.Join(legacy, "sessions", "a.jsonl")); err != nil {
		t.Fatal("旧数据不该被删除")
	}

	// 第二次:新位置已有数据 ⇒ 什么都不做(不返回落地路径)。
	moved2, err := migrateFrom(legacy, home)
	if err != nil || moved2 != "" {
		t.Fatalf("已有数据时不该再迁移: moved=%q err=%v", moved2, err)
	}

	// 旧位置根本不存在 ⇒ 不是错误。
	moved3, err := migrateFrom(filepath.Join(t.TempDir(), "nope"), home)
	if err != nil || moved3 != "" {
		t.Fatalf("无旧数据应是静默成功: moved=%q err=%v", moved3, err)
	}
}

// TestIsPortableAt 便携判定只看同目录的 portable.marker(空文件即可)。
func TestIsPortableAt(t *testing.T) {
	dir := t.TempDir()
	if isPortableAt(dir) {
		t.Fatal("没有 marker 时不该判为便携")
	}
	writeFile(t, filepath.Join(dir, portableMarker), "")
	if !isPortableAt(dir) {
		t.Fatal("有 marker 时应该是便携")
	}
	// 名字相近但不能误判。
	other := t.TempDir()
	writeFile(t, filepath.Join(other, portableMarker+".txt"), "")
	if isPortableAt(other) {
		t.Fatal("相近名字不该误判为便携")
	}
}

// TestStagedNameStable 副本名只由「版本 + 源指纹」决定:同一指纹稳定,不同指纹必不同。
// 反向验证:把指纹从名字里去掉,第二条断言必红(那会让所有版本共用一个文件名)。
func TestStagedNameStable(t *testing.T) {
	a := stagedName("0.7.4", "0.7.4\n100\n1700000000\n")
	b := stagedName("0.7.4", "0.7.4\n100\n1700000000\n")
	c := stagedName("0.7.4", "0.7.4\n101\n1700000000\n")
	if a != b {
		t.Fatalf("同一指纹应同名: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("指纹不同必须换名: %q", a)
	}
	if !isValidStagedName(a) {
		t.Fatalf("名字形状不对: %q", a)
	}
}

// isValidStagedName 形如 gah-<版本>-<8位十六进制>[.exe]。
func isValidStagedName(name string) bool {
	base := name
	if filepath.Ext(base) == ".exe" {
		base = base[:len(base)-4]
	}
	return len(base) > len("gah-")+9 && base[:4] == "gah-" && base[len(base)-9] == '-'
}

// TestTripleArchNaming 钉住产物名里的架构拼法:Rust target triple 用 aarch64,
// Go 的 GOARCH 是 arm64。这条曾经让壳在 arm mac 上直接「找不到 sidecar」。
func TestTripleArchNaming(t *testing.T) {
	got := triple()
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" && !strings.HasPrefix(got, "aarch64-") {
		t.Fatalf("darwin/arm64 的 triple 应以 aarch64- 开头,得到 %q", got)
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "amd64" && !strings.HasPrefix(got, "x86_64-") {
		t.Fatalf("darwin/amd64 的 triple 应以 x86_64- 开头,得到 %q", got)
	}
	if strings.Contains(got, "arm64") {
		t.Fatalf("triple 里不该出现 Go 的 arm64 拼法: %q", got)
	}
}
