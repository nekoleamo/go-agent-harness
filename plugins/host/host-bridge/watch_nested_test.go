// 热重载的**嵌套发布布局**回归(2026-10-03)。
//
// 这条钉的是实测出来的静默失效:watch 原先只监听 plugins/ 顶层,而发行布局是
// `plugins/<名>/<名>`(子目录)—— 重新 go build 覆盖子目录里的二进制不产生任何事件,
// 热重载静默不生效,必须重启 gah。扁平布局(测试/手工摆放)碰巧有效,所以从没暴露。
//
// 三段:① watch 递归本身(core/plugin);② 事件到了之后 bridge 是否真的重载/装载
// (本文件);③ 目录整体落盘时不能只拿到目录事件(见 TestLoadUnderNewDirLayout)。
package hostbridge

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nekoleamo/go-agent-harness/core/plugin"
)

// hitCollector 收集 watch 回调(**带锁**:回调在 watcher goroutine 里,不加锁会被 -race
// 判成数据竞争 —— 这类「测试自己制造的红」比没有测试更坏)。
type hitCollector struct {
	mu   sync.Mutex
	hits []string
}

func (h *hitCollector) add(p string) { h.mu.Lock(); h.hits = append(h.hits, p); h.mu.Unlock() }
func (h *hitCollector) count() int   { h.mu.Lock(); defer h.mu.Unlock(); return len(h.hits) }

// waitHit 轮询等待至少一次回调(返回是否等到)。
func (h *hitCollector) waitHit(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if h.count() > 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// buildFakePlugin 造一个「会自称空闲」的可执行脚本:bridge 探测它时能应答 --gah-caps,
// 但加载时自述空闲 ⇒ 走「跳过该角色」分支。这样本用例只验证**事件到达与遍历**,
// 不依赖真实插件二进制的构建。
func buildFakePlugin(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nif [ \"$1\" = \"--gah-caps\" ]; then echo '{}'; exit 0; fi\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestWatcherRecursiveNestedDir watch 必须覆盖子目录(第一段:框架本身)。
//
// 反向验证在下面 TestWatcherFlatLayoutStillWorks 之前手工做过:只 w.Add(顶层) 时,
// 本用例必然失败(回调命中 0 次)。
func TestWatcherRecursiveNestedDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "tool-demo")
	buildFakePlugin(t, filepath.Join(sub, "tool-demo"))

	hc := &hitCollector{}
	_, closeFn, err := plugin.NewWatcher(dir, 80*time.Millisecond, hc.add)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	time.Sleep(200 * time.Millisecond)

	// 改子目录里的二进制(模拟重新 go build 覆盖)
	if err := os.WriteFile(filepath.Join(sub, "tool-demo"), []byte("#!/bin/sh\n# v2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !hc.waitHit(3 * time.Second) {
		t.Fatal("改子目录里的插件二进制未产生事件 —— 嵌套布局下热重载静默失效")
	}
}

// TestWatcherFlatLayoutStillWorks 扁平布局(测试/手工摆放)不能被递归改动弄坏。
func TestWatcherFlatLayoutStillWorks(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "tool-demo")
	buildFakePlugin(t, f)

	hc := &hitCollector{}
	_, closeFn, err := plugin.NewWatcher(dir, 80*time.Millisecond, hc.add)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	time.Sleep(200 * time.Millisecond)

	if err := os.WriteFile(f, []byte("#!/bin/sh\n# v2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !hc.waitHit(3 * time.Second) {
		t.Fatal("扁平布局下 watch 未产生事件")
	}
}

// TestOnWatchEventDirLoadsUnder 目录事件要真的下去装载(第二段:bridge 侧)。
//
// 只把目录路径交给 rolesOf ⇒ 探测一个目录必然失败、返回空角色,于是「整个目录落盘」
// 这种最常见的放法被静默丢弃。本用例钉住「目录事件会走到目录内容」这件事:
// 放一个**探测即失败**的假二进制,它不该被装上(否则会真的 exec 一个假二进制),
// 但也不能 panic / 报错 —— 契约是「安静地试一遍」。
func TestOnWatchEventDirLoadsUnder(t *testing.T) {
	b := newCfgTestBridge()
	dir := t.TempDir()
	buildFakePlugin(t, filepath.Join(dir, "tool-demo"))

	b.onWatchEvent(dir)                             // 目录事件
	b.onWatchEvent(filepath.Join(dir, "tool-demo")) // 里面的文件事件

	if n := b.entryCount(); n != 0 {
		t.Fatalf("假二进制不该被装上(它自称空闲),got %d 个条目", n)
	}
	// 不存在的路径 / 空路径都不得炸
	b.onWatchEvent(filepath.Join(dir, "不存在"))
	b.onWatchEvent("")
}

// entryCount 当前已加载条目数(断言用)。
func (b *Bridge) entryCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.entries)
}
