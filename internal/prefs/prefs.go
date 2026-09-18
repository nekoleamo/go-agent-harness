// Package prefs 宿主共享运行偏好(思考/沙箱/历史注入)持久化:TUI 与 Web 双端
// 读写同一 $GAH_HOME/config/gah-state.json(退出即记,启动恢复)。
// 运行时包(web/、tui/)可用;插件(plugins/)不得 import(仅 import sdk 红线)。
// model 走 providerfile 持久化,不在此文件。
package prefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Prefs 偏好快照。字段零值 = 未设置(加载后保持默认)。
type Prefs struct {
	Thinking string `json:"thinking,omitempty"`
	Sandbox  string `json:"sandbox,omitempty"`
	Approval string `json:"approval,omitempty"`
	History  *int   `json:"history,omitempty"`
	// Statusline TUI 状态栏项集合与顺序(/statusline;空 = 基线默认顺序)。
	Statusline []string `json:"statusline,omitempty"`
}

// Path 偏好文件路径(GAH_HOME 未设 = 空,表示跳过持久化——测试/无 home 场景纯内存)。
func Path() string {
	h := os.Getenv("GAH_HOME")
	if h == "" {
		return ""
	}
	return filepath.Join(h, "config", "gah-state.json")
}

// legacyPath 兼容旧 web-state.json(迁移期:gah-state 不存在时读旧文件)。
func legacyPath() string {
	h := os.Getenv("GAH_HOME")
	if h == "" {
		return ""
	}
	return filepath.Join(h, "config", "web-state.json")
}

// Load 读偏好(缺文件/坏文件 = 零值默认,容忍;迁移期回退旧 web-state.json)。
func Load() Prefs {
	p := Prefs{}
	path := Path()
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &p)
			return p
		}
	}
	if lp := legacyPath(); lp != "" {
		if b, err := os.ReadFile(lp); err == nil {
			_ = json.Unmarshal(b, &p)
			return p
		}
	}
	return p
}

// mu 进程内串行 read-modify-write:偏好是"整对象覆写",两个并发写者(web 每请求一
// goroutine、TUI 与 Web 同进程)各持同一快照会互相抹掉字段(表现为偏好莫名回退)。
var mu sync.Mutex

// Save 覆写偏好(GAH_HOME 未设 = 跳过;目录自动创建;失败静默——非关键路径)。
func Save(p Prefs) {
	mu.Lock()
	defer mu.Unlock()
	saveLocked(p)
}

// Update 读-改-写偏好:进程内互斥 + 落盘前重读文件,只改回调涉及的字段,
// 避免并发写者用共享快照整体覆写抹掉对方刚写入的偏好。
func Update(fn func(*Prefs)) {
	if fn == nil {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	p := Load()
	fn(&p)
	saveLocked(p)
}

// saveLocked Save 的实现体(调用方持有 mu;不做加锁)。
func saveLocked(p Prefs) {
	path := Path()
	if path == "" {
		return
	}
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = writeFileAtomic(path, b, 0o600)
}

// writeFileAtomic 同目录临时文件写入 + rename 原子替换(失败清理临时文件)。
// TUI 与 Web 可能同时写同一文件;原子替换保证读方不会读到半截 JSON 而整体丢偏好。
func writeFileAtomic(path string, raw []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gah-state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// SetThinking / SetSandbox / SetHistory / SetApproval 便捷更新(全走 Update:
// 读-改-写在锁内完成,不与他写者的字段互相覆盖)。
func SetThinking(v string) { Update(func(p *Prefs) { p.Thinking = v }) }
func SetSandbox(v string)  { Update(func(p *Prefs) { p.Sandbox = v }) }
func SetHistory(n int)     { Update(func(p *Prefs) { p.History = &n }) }
func SetApproval(v string) { Update(func(p *Prefs) { p.Approval = v }) }

// SetStatusline 状态栏项集合与顺序(nil/空 = 回基线默认)。
func SetStatusline(items []string) {
	Update(func(p *Prefs) {
		if len(items) == 0 {
			p.Statusline = nil
			return
		}
		p.Statusline = append([]string(nil), items...)
	})
}
