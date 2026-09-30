// host-session-log · 会话日志实例注册表(实现 sdk.SessionLogs)。
//
// 为什么需要它:ctx.sessions 是**单例**,SetPath/Load 是「切换」语义 —— 两个会话同时
// 跑回合时会争同一份内存事件与同一个文件句柄(追加与投影互相交错,工具结果错位)。
// 这里按落盘路径给每个会话一份**独立** Log:各自的事件序列、seq、压缩水位、文件句柄,
// 广播也走各自的事件名(见 sdk.SessionEventName),订阅方可精确只订自己那个会话。
package sessionlog

import (
	"fmt"
	"sort"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// logEntry 一个已建实例 + 引用计数。
type logEntry struct {
	log  *Log
	refs int
}

// Logs 实现 sdk.SessionLogs。
type Logs struct {
	mu     sync.Mutex
	byPath map[string]*logEntry
	ctx    sdk.Ctx
}

// NewLogs 建注册表(需 ctx:新实例要靠它广播会话事件)。
func NewLogs(c sdk.Ctx) *Logs {
	return &Logs{byPath: make(map[string]*logEntry), ctx: c}
}

// Acquire 取/建该路径的独立实例;同路径重复取 = 同一实例(引用 +1)。
func (r *Logs) Acquire(path, id string) (sdk.SessionLog, error) {
	if path == "" {
		return nil, fmt.Errorf("sessionlog: Acquire 需要落盘路径")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.byPath[path]; ok {
		e.refs++
		return e.log, nil
	}
	// 先 Load 再入表:文件不存在 = 空会话(正常);真失败(权限/EIO)返回错误不入表,
	// 免得留下「已登记但内容不完整」的实例。
	lg := newLog("")
	lg.ctx = r.ctx
	lg.id = id
	if err := lg.Load(path); err != nil {
		return nil, err
	}
	r.byPath[path] = &logEntry{log: lg, refs: 1}
	return lg, nil
}

// Release 归还一次引用;归零时落盘并移出表(空闲会话不留内存副本)。未持有 = no-op。
func (r *Logs) Release(path string) {
	if path == "" {
		return
	}
	r.mu.Lock()
	e, ok := r.byPath[path]
	if !ok {
		r.mu.Unlock()
		return
	}
	e.refs--
	if e.refs > 0 {
		r.mu.Unlock()
		return
	}
	delete(r.byPath, path)
	r.mu.Unlock()
	// Close 有自身的锁与落盘;必须在注册表锁外调用(避免与 Append 的锁序交叉)。
	e.log.Close()
}

// CloseAll 落盘并关闭注册表里的**所有**实例(插件卸载时调)。
// 必需而非可选:空闲实例的 *os.File 句柄不关,在 Windows 上会让临时目录清理失败
// (RemoveAll 报「文件被另一进程占用」),在 POSIX 上只是句柄泄漏到进程结束。
func (r *Logs) CloseAll() {
	r.mu.Lock()
	entries := make([]*Log, 0, len(r.byPath))
	for p, e := range r.byPath {
		entries = append(entries, e.log)
		delete(r.byPath, p)
	}
	r.mu.Unlock()
	for _, lg := range entries {
		lg.Close()
	}
}

// Active 当前持有实例的路径(排序)。
func (r *Logs) Active() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.byPath))
	for p := range r.byPath {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
