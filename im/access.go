// 访问控制(对齐 omp-wechat gate / hermes DM policy / dsh-im 渠道访问策略):
// 三态 disabled(默认,静默丢弃)/ allowlist / pairing(配对码 1h 过期)。
// 命令权限 = 渠道访问策略(不区分 admin/用户,对齐 dsh-im 决策;列表类命令仅授权用户可见)。
package im

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// AccessMode 访问模式。
type AccessMode string

const (
	AccessDisabled  AccessMode = "disabled"  // 默认:静默丢弃全部未授权消息(安全基线)
	AccessAllowlist AccessMode = "allowlist" // 仅 allow 名单中的 SenderKey
	AccessPairing   AccessMode = "pairing"   // 陌生用户发消息 → 发配对码,主机 /im pair 批准
)

// gateResult 入站裁决结果。
type gateResult int

const (
	gateDrop    gateResult = iota // 静默丢弃(不回、不提示,防枚举)
	gateDeliver                   // 放行
	gatePair                      // 需配对(回配对提示)
)

// pendingPairing 一条待批准配对记录。
type pendingPairing struct {
	senderKey string // 谁在等配对
	code      string // 配对码(交给陌生用户,主机批准用)
	expiresAt time.Time
}

// Access 访问控制(并发安全)。
type Access struct {
	mu       sync.Mutex
	mode     AccessMode
	allow    map[string]bool           // senderKey → 授权
	pending  map[string]pendingPairing // code → 记录
	ttl      time.Duration
	onChange func() // 授权集变化回调(插件壳接线做持久化;锁外调用,可 nil)
}

// NewAccess 构造访问控制(opt.Allow 为初始授权;ttl 配对码有效期)。
func NewAccess(mode AccessMode, allow []string, ttl time.Duration) *Access {
	a := &Access{
		mode:    mode,
		allow:   make(map[string]bool),
		pending: make(map[string]pendingPairing),
		ttl:     ttl,
	}
	for _, k := range allow {
		a.allow[k] = true
	}
	return a
}

// SetOnChange 注册授权集变化回调(Allow/Revoke/ApprovePair 后调用;锁外触发,
// 回调内可读 List() 并持久化——如写回凭证 store,重启不丢授权)。
func (a *Access) SetOnChange(fn func()) {
	a.mu.Lock()
	a.onChange = fn
	a.mu.Unlock()
}

// Mode 当前访问模式。
func (a *Access) Mode() AccessMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode
}

// SetMode 运行期切换(disabled 时清空 pending——已发配对码作废)。
func (a *Access) SetMode(m AccessMode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mode = m
	if m == AccessDisabled {
		a.pending = make(map[string]pendingPairing)
	}
}

// Gate 裁决一个发送方(惰性清理过期配对)。
func (a *Access) Gate(senderKey string) (gateResult, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for code, p := range a.pending {
		if p.expiresAt.Before(now) {
			delete(a.pending, code)
		}
	}
	if a.allow[senderKey] {
		return gateDeliver, ""
	}
	switch a.mode {
	case AccessAllowlist, AccessDisabled:
		return gateDrop, ""
	case AccessPairing:
		code := a.newCodeLocked(senderKey, now)
		return gatePair, code
	}
	return gateDrop, ""
}

// Allow 直接授权一个发送方(幂等)。
func (a *Access) Allow(senderKey string) {
	a.mu.Lock()
	a.allow[senderKey] = true
	cb := a.onChange
	a.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// Revoke 撤销授权。
func (a *Access) Revoke(senderKey string) bool {
	a.mu.Lock()
	if !a.allow[senderKey] {
		a.mu.Unlock()
		return false
	}
	delete(a.allow, senderKey)
	cb := a.onChange
	a.mu.Unlock()
	if cb != nil {
		cb()
	}
	return true
}

// Allowed 是否已授权。
func (a *Access) Allowed(senderKey string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.allow[senderKey]
}

// List 授权用户(稳定顺序不可保证;小名单)。
func (a *Access) List() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.allow))
	for k := range a.allow {
		out = append(out, k)
	}
	return out
}

// ApprovePair 用配对码批准其发送方(过期/未知返回 false)。
func (a *Access) ApprovePair(code string) bool {
	a.mu.Lock()
	p, ok := a.pending[code]
	if !ok {
		a.mu.Unlock()
		return false
	}
	delete(a.pending, code)
	if time.Now().After(p.expiresAt) {
		a.mu.Unlock()
		return false
	}
	a.allow[p.senderKey] = true
	cb := a.onChange
	a.mu.Unlock()
	if cb != nil {
		cb()
	}
	return true
}

// newCodeLocked 生成配对码:同发送方复用未过期配对;pending 上限防刷。
func (a *Access) newCodeLocked(senderKey string, now time.Time) string {
	for code, p := range a.pending {
		if p.senderKey == senderKey {
			return code // 复用(配对码 1h 过期,同人重发消息不变码)
		}
	}
	if len(a.pending) >= 32 { // 防刷:同时最多 32 个待批准,新人不发码
		return ""
	}
	code := randCode(3)
	a.pending[code] = pendingPairing{senderKey: senderKey, code: code, expiresAt: now.Add(a.ttl)}
	return code
}

// randCode 生成 n 字节十六进制配对码(不可枚举猜解)。
func randCode(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "pair-unknown"
	}
	return hex.EncodeToString(b)
}
