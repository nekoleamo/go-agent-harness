// Package prefs 宿主共享运行偏好(思考/沙箱/历史注入)持久化:TUI 与 Web 双端
// 读写同一 $GAH_HOME/config/gah-state.json(退出即记,启动恢复)。
// 写入方:运行时包(web/、tui/)与**宿主内置插件**(plugins/host/、plugins/policy/:随单二进制
// 编译、非插件包依赖,不构成 import 环,如 host-internal-commands 的命令回写)。外部插件
// (extplugins/,独立 module 的外部二进制)不在此列 —— 它们只 import sdk。
// model 走 providerfile 持久化,不在此文件。
package prefs

import (
	"log"
	"time"

	"encoding/json"
	"github.com/nekoleamo/go-agent-harness/internal/xlock"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Prefs 偏好快照。字段零值 = 未设置(加载后保持默认)。
type Prefs struct {
	Thinking string `json:"thinking,omitempty"`
	Sandbox  string `json:"sandbox,omitempty"`
	Approval string `json:"approval,omitempty"`
	History  *int   `json:"history,omitempty"`
	// SandboxSync 档位联动开关(审批档 → 沙箱有效档)的用户选择:R10 ②-2。
	// nil = 未设置(用插件 config 的 data.sync 默认值);非 nil = 用户显式选择,覆盖 config。
	SandboxSync *bool `json:"sandbox_sync,omitempty"`
	// Statusline TUI 状态栏项集合与顺序(/statusline;空 = 基线默认顺序)。
	Statusline []string `json:"statusline,omitempty"`
	// WebAllowHosts web_fetch 的 TOFU 域名白名单(A7 出口审批):用户批准过的**精确 host**,
	// 小写。与 GAH_WEB_ALLOW_HOSTS(静态名单,不落盘)互补:静态名单用于无人值守事前放行。
	WebAllowHosts []string `json:"web_allow_hosts,omitempty"`
	// Role 当前角色 ID(空 = 未启用角色,即基线行为;角色定义见 internal/roles)。
	// 为什么放偏好而不是另开文件:切换要**立即**对下一次组装生效,而它本来就是一个
	// 跨端共享的小状态(TUI/Web 读写同一份),与 thinking/sandbox 同类。
	Role string `json:"role,omitempty"`
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

// AddWebAllowHost 把一个 host 追加进 TOFU 白名单(小写、幂等;GAH_HOME 未设 = 纯内存无效写)。
//
// 为何不用 Save:并发写者(web 每请求一 goroutine)各持快照会互抹字段 —— Update 会落盘前重读。
func AddWebAllowHost(host string) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return
	}
	Update(func(p *Prefs) {
		for _, h := range p.WebAllowHosts {
			if strings.EqualFold(strings.TrimSpace(h), host) {
				return
			}
		}
		p.WebAllowHosts = append(p.WebAllowHosts, host)
	})
}

// mu 进程内串行 read-modify-write:偏好是"整对象覆写",两个并发写者(web 每请求一
// goroutine、TUI 与 Web 同进程)各持同一快照会互相抹掉字段(表现为偏好莫名回退)。
var mu sync.Mutex

// Save 覆写偏好(GAH_HOME 未设 = 跳过;目录自动创建;失败静默——非关键路径)。
func Save(p Prefs) {
	path := Path()
	if path == "" {
		mu.Lock()
		defer mu.Unlock()
		saveLocked(p)
		return
	}
	// 跨进程:整对象覆写同样要串行,否则一个实例的整份快照会盖掉另一个实例刚写的字段。
	mu.Lock()
	defer mu.Unlock()
	ran, err := xlock.TryRun(lockPath(path), func() error { saveTo(path, p); return nil })
	if err != nil {
		logPrefs("Save 失败", "err", err)
		return
	}
	if !ran {
		logPrefs("Save 放弃:另一个实例正占用偏好锁(本次未落盘)", "path", path)
	}
}

// Update 读-改-写偏好:进程内互斥 + 落盘前重读文件,只改回调涉及的字段,
// 避免并发写者用共享快照整体覆写抹掉对方刚写入的偏好。
//
// **跨进程**:进程内的 mu 挡不住"桌面壳起了两个实例各改各的"。因此这里在读-改-写
// 全程持一把**文件锁**;拿不到锁(另一个实例正在写)时按"稍后重试"处理 ——
// 锁的持有时间只有一次读+一次写,重试几乎必然成功;连续两次失败就**放弃本次修改**
// 并记日志(绝不静默成功:调用方会以为偏好已改,重启后又不是那一套)。
func Update(fn func(*Prefs)) {
	if fn == nil {
		return
	}
	path := Path()
	if path == "" {
		// 无数据根(纯内存/嵌入场景):退回进程内互斥的老口径。
		mu.Lock()
		defer mu.Unlock()
		p := Load()
		fn(&p)
		saveLocked(p)
		return
	}
	// 进程内也要串行:文件锁在同进程的不同 fd 之间**同样**互斥,若不在进程内先排队,
	// 同一进程里的几十个 goroutine(=web 每请求一个)会自己抢自己,重试再多次也会丢操作。
	mu.Lock()
	defer mu.Unlock()
	// 跨进程:带退避地重试。锁的持有时间只有「一次读 + 一次写」,正常竞争都是毫秒级;
	// 上限 1.5s 是为了覆盖"另一个实例正在做一次慢写"的情况。
	delay := 5 * time.Millisecond
	deadline := time.Now().Add(1500 * time.Millisecond)
	for {
		ran, err := xlock.TryRun(lockPath(path), func() error {
			p := loadFrom(path) // 锁内重读:拿到的必是另一个写者刚落盘的状态
			fn(&p)
			saveTo(path, p)
			return nil
		})
		if err != nil {
			logPrefs("偏好更新失败", "err", err)
			return
		}
		if ran {
			return
		}
		// busy:有别的进程(或另一把锁的持有者)正在写。等它一下再试。
		if time.Now().After(deadline) {
			logPrefs("偏好更新放弃:偏好锁持续被占用(本次修改未落盘)", "path", path)
			return
		}
		time.Sleep(delay)
		if delay < 100*time.Millisecond {
			delay *= 2
		}
	}
}

// lockPath 偏好文件的锁路径(与状态文件同目录,便于整目录一起迁移)。
func lockPath(statePath string) string {
	return statePath + ".lock"
}

// saveLocked Save 的实现体(调用方持有 mu;不做加锁)。
func saveLocked(p Prefs) {
	path := Path()
	if path == "" {
		return
	}
	saveTo(path, p)
}

// loadFrom 读指定状态文件(不回落到 legacy:调用方明确知道要读哪个文件)。
// 读不出来/坏 JSON = 空偏好(与 Load 同口径:坏文件不阻断启动)。
func loadFrom(path string) Prefs {
	p := Prefs{}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

// saveTo 写指定状态文件(原子替换;失败静默 —— 偏好是非关键路径)。
func saveTo(path string, p Prefs) {
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = writeFileAtomic(path, b, 0o600)
}

// logPrefs 偏好层的失败留痕。用标准 log 而非 slog:internal/prefs 是不依赖 ctx 的
// 底层包,而「两个实例抢锁」这类问题恰恰是**用户报不出 stack、只有日志能查**的那类。
func logPrefs(msg string, kv ...any) {
	if len(kv) == 0 {
		log.Printf("prefs: %s", msg)
		return
	}
	log.Printf("prefs: "+msg, kv...)
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

// SetThinking / SetSandbox / SetHistory / SetApproval / SetSandboxSync 便捷更新(全走 Update:
// 读-改-写在锁内完成,不与他写者的字段互相覆盖)。
func SetThinking(v string) { Update(func(p *Prefs) { p.Thinking = v }) }
func SetSandbox(v string)  { Update(func(p *Prefs) { p.Sandbox = v }) }
func SetHistory(n int)     { Update(func(p *Prefs) { p.History = &n }) }
func SetApproval(v string) { Update(func(p *Prefs) { p.Approval = v }) }

// SetSandboxSync 档位联动开关(/sandbox sync on|off 与 Web 设置面板同一偏好)。
func SetSandboxSync(v bool) { Update(func(p *Prefs) { p.SandboxSync = &v }) }

// SetRole 当前角色 ID(空 = 停用角色;internal/roles 与 host-roles 共用)。
func SetRole(v string) { Update(func(p *Prefs) { p.Role = v }) }

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
