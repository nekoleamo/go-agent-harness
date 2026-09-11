// Package prefs 宿主共享运行偏好(思考/沙箱/历史注入)持久化:TUI 与 Web 双端
// 读写同一 $GAH_HOME/config/gah-state.json(退出即记,启动恢复)。
// 运行时包(web/、tui/)可用;插件(plugins/)不得 import(仅 import sdk 红线)。
// model 走 providerfile 持久化,不在此文件。
package prefs

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Prefs 偏好快照。字段零值 = 未设置(加载后保持默认)。
type Prefs struct {
	Thinking string `json:"thinking,omitempty"`
	Sandbox  string `json:"sandbox,omitempty"`
	Approval string `json:"approval,omitempty"`
	History  *int   `json:"history,omitempty"`
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

// Save 覆写偏好(GAH_HOME 未设 = 跳过;目录自动创建;失败静默——非关键路径)。
func Save(p Prefs) {
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
	_ = os.WriteFile(path, b, 0o644)
}

// SetThinking / SetSandbox / SetHistory 便捷更新(读-改-写)。
func SetThinking(v string) {
	p := Load()
	p.Thinking = v
	Save(p)
}
func SetSandbox(v string) {
	p := Load()
	p.Sandbox = v
	Save(p)
}
func SetHistory(n int) {
	p := Load()
	p.History = &n
	Save(p)
}
func SetApproval(v string) {
	p := Load()
	p.Approval = v
	Save(p)
}
