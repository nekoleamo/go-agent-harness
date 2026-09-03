// 热重载监听框架:watch 外部插件目录,文件变化经 debounce 触发回调。
// (对齐设计 §2.3:进程内热重载 = fsnotify + dispose/reload;M2 起由 plugin-manager 使用)
package plugin

import (
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Watcher 监听一个目录中的插件二进制/清单变化。
type Watcher struct {
	w      *fsnotify.Watcher
	mu     sync.Mutex
	timers map[string]*time.Timer
	done   chan struct{}
}

// NewWatcher 建立监听。onChange(id) 在文件变化(去抖)后回调;
// id 由调用方从事件文件名解析(如 tool-shell)。返回的关闭函数停止监听。
func NewWatcher(dir string, debounce time.Duration, onChange func(id string)) (*Watcher, func(), error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, nil, err
	}
	if err := w.Add(dir); err != nil {
		w.Close()
		return nil, nil, err
	}
	watcher := &Watcher{w: w, timers: make(map[string]*time.Timer), done: make(chan struct{})}

	go watcher.loop(debounce, onChange)
	closeFn := func() {
		select {
		case <-watcher.done:
		default:
			close(watcher.done)
		}
		w.Close()
	}
	return watcher, closeFn, nil
}

func (wt *Watcher) loop(debounce time.Duration, onChange func(id string)) {
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

// schedule 每文件去抖:debounce 窗口内的连续事件合并为一次回调。
func (wt *Watcher) schedule(name string, debounce time.Duration, onChange func(id string)) {
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
