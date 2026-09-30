package hostcwdsessions

// sessionDir 能力(实现 sdk.SessionDir):按**会话 id** 取独立的会话日志实例。
//
// 与单例的分工:
//   - id 空 = 当前打开的会话 → 直接给 ctx.sessions 单例(**不新建、不计数**),
//     所以未带 id 的调用方行为逐字不变(向后兼容的关键)。
//   - id 非空 → 向 ctx.sessionLogs 注册表要一份独立实例(多会话并行用)。
//
// 一条硬规则:**同一个会话文件不能有两个 Log 实例**。主单例与注册表实例若指向同一文件,
// 两个 Append 会交错写、投影会互相污染。因此:
//   - id 恰是当前打开的会话 → 返回单例(而不是再建一个);
//   - Open/New 切向一个**正被注册表持有**的会话 → 显式拒绝(不静默切换出分叉)。

import (
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// sessionDir 实现 sdk.SessionDir(Service 的适配层:不把 Acquire/Release 混进
// CwdSessions 语义里,两者职责不同 —— 一个按 id 取日志,一个管切换/列举)。
type sessionDir struct {
	svc  *Service
	logs sdk.SessionLogs
	// held 被注册表持有的非当前会话 id 及引用数(与 logs 配对记账,
	// 使 Active 无需从文件名反推 id —— 项目 key 本身含 '-',反推不可靠)。
	held   map[string]int
	heldMu sync.Mutex
}

// Acquire 取该 id 的会话日志:id 空或恰为当前打开的会话 = 主单例。
func (d *sessionDir) Acquire(id string) (sdk.SessionLog, error) {
	s := d.svc
	if id == "" {
		return s.sessions, nil
	}
	if !validSessionID(id) {
		return nil, fmt.Errorf("cwdsessions: 非法会话 id")
	}
	s.mu.RLock()
	key, cur, main := s.key, s.current, s.sessions
	s.mu.RUnlock()
	if main == nil {
		return nil, fmt.Errorf("cwdsessions: 会话日志未装配")
	}
	if id == cur {
		return main, nil
	}
	if d.logs == nil {
		return nil, fmt.Errorf("cwdsessions: 会话实例注册表未装配(缺 ctx.sessionLogs)")
	}
	lg, err := d.logs.Acquire(SessionPath(SessionsRoot(), key, id), id)
	if err != nil {
		return nil, err
	}
	d.heldMu.Lock()
	if d.held == nil {
		d.held = make(map[string]int)
	}
	d.held[id]++
	d.heldMu.Unlock()
	return lg, nil
}

// Release 归还(id 空或当前会话 = no-op;未持有 = no-op,幂等)。
func (d *sessionDir) Release(id string) {
	if id == "" || d.logs == nil {
		return
	}
	s := d.svc
	s.mu.RLock()
	key, cur := s.key, s.current
	s.mu.RUnlock()
	if id == cur {
		return
	}
	d.heldMu.Lock()
	n, ok := d.held[id]
	if ok {
		if n <= 1 {
			delete(d.held, id)
		} else {
			d.held[id] = n - 1
		}
	}
	d.heldMu.Unlock()
	if !ok {
		return
	}
	d.logs.Release(SessionPath(SessionsRoot(), key, id))
}

// Active 当前被占用的非当前会话 id(排序;诊断与展示用)。
func (d *sessionDir) Active() []string {
	d.heldMu.Lock()
	defer d.heldMu.Unlock()
	out := make([]string, 0, len(d.held))
	for id := range d.held {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// heldRefs 该 id 当前被注册表持有的引用数(0 = 无;Service.Open 的冲突闸门用)。
func (d *sessionDir) heldRefs(id string) int {
	d.heldMu.Lock()
	defer d.heldMu.Unlock()
	return d.held[id]
}

// Spawn 新建独立会话文件且**不切换当前会话**(见 sdk.SessionDir.Spawn)。
func (d *sessionDir) Spawn() (string, error) {
	s := d.svc
	s.mu.RLock()
	key := s.key
	s.mu.RUnlock()
	id, err := newSessionID(key)
	if err != nil {
		return "", err
	}
	// 建空文件即可(存在即一个空会话;首次 Append 才真正写入内容)。
	// 不用 Open:那会把当前会话切走(本窗口的历史与输入框当场换掉)。
	if err := os.WriteFile(SessionPath(SessionsRoot(), key, id), nil, 0o600); err != nil {
		return "", fmt.Errorf("cwdsessions: 建会话文件失败: %w", err)
	}
	return id, nil
}

var _ sdk.SessionDir = (*sessionDir)(nil)
