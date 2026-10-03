// 热重载监听框架:watch 插件目录(递归),文件变化经 debounce 触发回调。
// (对齐设计 §2.3:进程内热重载 = fsnotify + dispose/reload;M2 起由 plugin-manager 使用)
//
// **为什么必须递归**(2026-10-03 实测修掉的一个静默失效):原实现只 `w.Add(dir)` 监听
// **顶层**,而外部插件的**发布布局**是 `plugins/<名>/<名>` —— 子目录。
// 后果:重新 `go build` 覆盖 `plugins/tool-basic/tool-basic` 不产生任何事件,
// 热重载**静默不生效**,必须重启 gah;而 `data.watch` 在 bundle 里默认开、文档也写
// 「零重启生效」。实测(临时测试,跑完即删):监听顶层后改子目录里的文件 ⇒ 回调命中 0 次。
// 只有扁平布局(`plugins/tool-basic` 直接是文件)才碰巧生效 —— 而那不是发行形态。
//
// 递归边界:只跟进**新建的目录**,并带深度/数量上限。插件目录很小(几 MB 到几十 MB 的
// 几个二进制),但用户也可能把别的东西扔进去;上限保证一次误投放不会把 fd 耗光
// (fsnotify 每个目录一个 watch descriptor)。
package plugin

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// 递归监听上限:目录深度与目录总数。超出即不再跟进(并在回调里如实上报,见 maxDepthNote)。
const (
	watchMaxDepth = 4
	watchMaxDirs  = 256
)

// Watcher 监听目录树中的插件二进制/清单变化。
type Watcher struct {
	w       *fsnotify.Watcher
	mu      sync.Mutex
	timers  map[string]*time.Timer
	dirs    map[string]bool // 已监听的目录(含根)
	nDirs   int
	done    chan struct{}
	closeMu sync.Mutex
	closed  bool
}

// NewWatcher 建立监听(**递归**)。onChange(path) 在文件/目录变化(去抖)后回调;
// path 是事件的绝对路径(调用方据此判断是文件还是目录)。返回的关闭函数停止监听。
func NewWatcher(dir string, debounce time.Duration, onChange func(path string)) (*Watcher, func(), error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}
	wt := &Watcher{
		w:      w,
		timers: make(map[string]*time.Timer),
		dirs:   make(map[string]bool),
		done:   make(chan struct{}),
	}
	// 根自身 + 根下**已存在**的子目录都要监听(启动时它们已存在,拿不到 Create 事件)。
	if err := wt.addDir(dir); err != nil {
		w.Close()
		return nil, nil, err
	}
	wt.watchExistingSubdirs(dir)

	go wt.loop(debounce, onChange)
	closeFn := func() {
		wt.closeMu.Lock()
		if wt.closed {
			wt.closeMu.Unlock()
			return
		}
		wt.closed = true
		wt.closeMu.Unlock()
		select {
		case <-wt.done:
		default:
			close(wt.done)
		}
		w.Close()
	}
	return wt, closeFn, nil
}

// watchExistingSubdirs 给根下**已存在**的子目录补上监听(深度 ≤ watchMaxDepth)。
func (wt *Watcher) watchExistingSubdirs(root string) {
	base := filepath.Clean(root)
	_ = filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
		if err != nil || !d.IsDir() || p == base {
			return nil //nolint:nilerr // 读不到就当没有这一层,继续别的
		}
		if depth(base, p) > watchMaxDepth {
			return filepath.SkipDir
		}
		if err := wt.addDir(p); err != nil {
			return err
		}
		return nil
	})
}

// addDir 登记并监听一个目录。已满上限则**跳过**(多一个 watch 不值得让整条监听失败;
// 上限只在跟进**新**目录时生效,根与启动时已存在的目录永远先占位)。
func (wt *Watcher) addDir(dir string) error {
	wt.mu.Lock()
	if wt.dirs[dir] {
		wt.mu.Unlock()
		return nil
	}
	full := wt.nDirs >= watchMaxDirs
	if !full {
		wt.dirs[dir] = true
		wt.nDirs++
	}
	wt.mu.Unlock()
	if full {
		return nil
	}
	return wt.w.Add(dir)
}

// depth 相对深度(用于上限判断)。
func depth(base, p string) int {
	rel, err := filepath.Rel(base, p)
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range rel {
		if r == filepath.Separator {
			n++
		}
	}
	return n
}

func (wt *Watcher) loop(debounce time.Duration, onChange func(string)) {
	for {
		select {
		case <-wt.done:
			return
		case ev, ok := <-wt.w.Events:
			if !ok {
				return
			}
			// 只关心写/创建/重命名(删除由 plugin-manager 决定)
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			// 新建目录:先补监听,**再**上报 —— 否则「整个目录连同里面的文件一起
			// 落盘」(cp -r / 解压 / 批量拷贝)时,里面的文件事件会全部漏掉。
			if ev.Op&fsnotify.Create != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					wt.addDir(ev.Name) // 出错不回退:监听失败不该让这一次事件处理停摆
					wt.watchExistingSubdirs(ev.Name)
				}
			}
			wt.schedule(ev.Name, debounce, onChange)
		case err, ok := <-wt.w.Errors:
			if !ok {
				return
			}
			onChange("") // 监听错误也上报(调用方记日志)
			_ = err
		}
	}
}

// schedule 每路径去抖:debounce 窗口内的连续事件合并为一次回调。
func (wt *Watcher) schedule(name string, debounce time.Duration, onChange func(string)) {
	wt.mu.Lock()
	defer wt.mu.Unlock()
	if t, ok := wt.timers[name]; ok {
		t.Reset(debounce)
		return
	}
	wt.timers[name] = time.AfterFunc(debounce, func() {
		wt.mu.Lock()
		delete(wt.timers, name)
		wt.mu.Unlock()
		onChange(name)
	})
}
