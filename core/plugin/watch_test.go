package plugin

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcherDebounce(t *testing.T) {
	dir := t.TempDir()
	var got []string
	var mu sync.Mutex

	_, closeFn, err := NewWatcher(dir, 50*time.Millisecond, func(id string) {
		mu.Lock()
		got = append(got, id)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()

	// 连续两次写同一文件:debounce 窗口内应合并为一次回调
	f := filepath.Join(dir, "tool-shell")
	os.WriteFile(f, []byte("v1"), 0o644)
	os.WriteFile(f, []byte("v2"), 0o644)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("watcher 未收到回调")
	}
	if len(got) > 2 {
		t.Fatalf("debounce 未生效,回调次数 %d", len(got))
	}
	if got[0] != f {
		t.Fatalf("回调应携带文件路径,got %s", got[0])
	}
}

// TestWatcherReportsRemove 删除要被上报(批二 §2.6)。
//
// 此前 Remove 被**显式排除**(注释「删除由 plugin-manager 决定」),而外部插件这条路
// **没有 plugin-manager 介入** ⇒ 用户 rm 掉插件文件,运行中的进程继续持着工具注册与
// 回调 token 直到 gah 重启。「我删了它」给的是**虚假的安全感**。
func TestWatcherReportsRemove(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "tool-demo")
	if err := os.WriteFile(f, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	_, closeFn, err := NewWatcher(dir, 50*time.Millisecond, func(p string) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	// 等监听建立后再删(先删再 Add 会漏事件)。
	time.Sleep(100 * time.Millisecond)
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) == 0 {
		t.Fatal("删除文件未被上报 —— 调用方永远不会知道该卸载它")
	}
	// 去抖 + 存在性判定:回调时文件确实已不在
	for _, p := range got {
		if p == f {
			if _, err := os.Stat(f); err == nil {
				t.Fatal("回调时文件仍在,说明是中间态被当成了删除")
			}
		}
	}
}

// TestWatcherRemoveDirReported 整个目录被删也要上报(发布布局是 plugins/<名>/<名>)。
func TestWatcherRemoveDirReported(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "tool-demo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "tool-demo"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	_, closeFn, err := NewWatcher(dir, 50*time.Millisecond, func(p string) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	time.Sleep(100 * time.Millisecond)
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got == nil {
		t.Fatal("目录被删未被上报 —— 删掉整个插件目录时进程会一直跑到 gah 重启")
	}
}

// TestWatcherRecreateAfterRemove 先删后重建要重新被监听。
//
// 摘掉登记表不是洁癖:不摘的话「同一路径重建」时 addDir 会因「已登记」而不再真正 add,
// 那一次新建目录里的插件就永远不会被加载。
func TestWatcherRecreateAfterRemove(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "tool-demo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "tool-demo"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	_, closeFn, err := NewWatcher(dir, 50*time.Millisecond, func(p string) {
		mu.Lock()
		seen[p]++
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	time.Sleep(100 * time.Millisecond)
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	// 重建:再写一次,应当**再**收到事件(说明监听没被摘死)
	inner := filepath.Join(sub, "tool-demo")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inner, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := seen[inner]
		mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("目录删除后重建,新文件没有事件 —— addDir 因「已登记」跳过了真正的 add")
}
