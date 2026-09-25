// A-5#125 数据根可写性探测单测:
// 可写目录 → true;只读目录 → false;未注入 GAH_HOME → nil(省略字段,不谎报);
// 路径不存在 → false(不 panic)。
package web

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProbeWritable(t *testing.T) {
	if got := probeWritable(""); got != nil {
		t.Fatalf("未注入 GAH_HOME 应无法判定(nil),得到 %v", *got)
	}

	dir := t.TempDir()
	if got := probeWritable(dir); got == nil || !*got {
		t.Fatalf("可写目录应判定为可写,得到 %v", got)
	}
	// 探针文件不许残留(只读根上尤其不能有半截产物)。
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("探测后目录应干净,实际残留 %d 项", len(ents))
	}

	if got := probeWritable(filepath.Join(dir, "no-such-dir")); got == nil || *got {
		t.Fatalf("不存在的路径应判定为不可写,得到 %v", got)
	}
}

func TestProbeWritableReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 无视权限位,只读断言无意义")
	}
	if runtime.GOOS == "windows" {
		// Windows 上 chmod 不影响 ACL 可写性(TempDir 仍然可写),构造不出只读目录 ⇒ 断言无对象。
		t.Skip("Windows 用 ACL 而非权限位,chmod 0555 不产生只读语义")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) }) // 让 TempDir 能清理
	if got := probeWritable(ro); got == nil || *got {
		t.Fatalf("只读目录应判定为不可写,得到 %v", got)
	}
}
