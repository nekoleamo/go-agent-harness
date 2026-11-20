package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadWindowState 读上次几何:正常读回;缺失/损坏/尺寸过小一律当"没有"
// (宁可用默认尺寸,也不能开出一个针眼窗口或直接启动失败)。
func TestLoadWindowState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	// localDataDir 在 mac 用 ~/Library/Application Support,在 win 用 LOCALAPPDATA;
	// 这里直接往它算出的路径写,避免依赖平台细节。
	path := windowStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	// 缺失 → false
	if _, ok := loadWindowState(); ok {
		t.Fatal("文件不存在时应返回 false")
	}

	// 正常 → 读回
	b, _ := json.Marshal(winState{X: 120, Y: 80, W: 1440, H: 900})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, ok := loadWindowState()
	if !ok || st.W != 1440 || st.H != 900 || st.X != 120 || st.Y != 80 {
		t.Fatalf("读回不对: %+v ok=%v", st, ok)
	}

	// 尺寸过小(损坏)→ false
	b2, _ := json.Marshal(winState{X: 0, Y: 0, W: 10, H: 10})
	if err := os.WriteFile(path, b2, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadWindowState(); ok {
		t.Fatal("尺寸过小应视为损坏")
	}

	// 坏 JSON → false(不 panic)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadWindowState(); ok {
		t.Fatal("坏 JSON 应返回 false")
	}
}

// TestWindowStateRoundTrip 写盘后能被读回(原子写路径 .new 不应残留)。
func TestWindowStateRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LOCALAPPDATA", home)
	path := windowStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	want := winState{X: -1400, Y: 40, W: 1600, H: 1000}
	b, _ := json.Marshal(want)
	if err := os.WriteFile(path+".new", b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	got, ok := loadWindowState()
	if !ok || got != want {
		t.Fatalf("读回 %+v ok=%v, want %+v", got, ok, want)
	}
	if _, err := os.Stat(path + ".new"); !isErrNotExist(err) {
		t.Fatal("临时文件应在 rename 后消失")
	}
}
