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
