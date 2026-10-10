// web · 会话作用域(把 ?session=/body.session 落到具体日志与写侧闸门上)。
//
// 为什么读侧与写侧待遇不同(重要,别当成不一致):
//   - **读侧**(首屏历史、事件流、state)现在就能按会话走:每个会话一份独立日志,
//     互不干扰,所以多窗口可以各自看一个会话。
//   - **写侧**(提交输入、命令、确认)仍绑在 agent-loop 单例上 —— 它把事件写进
//     ctx.sessions(当前打开的会话)。在多会话并行回合落地(方案 B-1)之前,
//     若允许「向非当前会话提交」,内容会静默写进**另一个**会话 = 数据错位。
//     故此处在闸门上显式拒绝并说明原因,而不是假装成功。
package web

import (
	"net/http"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// sessionScope 一次请求的会话作用域:ID 为空 = 当前主会话(Log 即 ctx.sessions 单例)。
type sessionScope struct {
	ID  string
	Log sdk.SessionLog
	// release 归还可能持有的独立实例(主会话为 nil = 无需归还)。
	release func()
}

// Release 归还。幂等由**服务端**保证:SessionDir.Release 本身幂等
// (未持有 = no-op),所以这里不能把 release 置 nil —— sessionScope 是值传递,
// 置 nil 只是改副本(staticcheck SA4005 会报「无效赋值」),并不能防重复调用。
func (sc sessionScope) Release() {
	if sc.release != nil {
		sc.release()
	}
}

// sessionKey 会话 id 的**归一化**键:空 / 等于当前打开的会话 ⇒ 空串(= 主会话)。
//
// 三处必须用同一个键,否则会出现「两个回合写同一个文件」(主单例 + 注册表实例):
// 读侧 scopeOf、回合占用 runningFor、agent-loop 的串行锁与取消归属。
func (s *Server) sessionKey(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if s.cs != nil && id == s.cs.CurrentSession() {
		return ""
	}
	return id
}

// runningFor 该会话的回合占用标志(懒建)。
//
// 为什么从一把全局闸改成**每会话一把**:多会话并行后,两个会话各跑各的回合是正常的;
// 用一把全局闸会让「B 窗口在跑」把「A 窗口提交」也顶成 409(明明两边都空)。
// 键用 sessionKey 归一化,与 agent-loop 的串行锁同一把语义。
func (s *Server) runningFor(key string) *atomic.Bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	b, ok := s.runBySession[key]
	if !ok {
		b = &atomic.Bool{}
		s.runBySession[key] = b
	}
	return b
}

// runningOf 只读探测该会话是否在跑(不存在 = false,**不建条目**)。
//
// 为何需要:GET /api/state?session=<任意串> 是 CORS 简单请求(浏览器里任意页面可发),
// 而 sessionKey 直接用查询参数当 map 键 —— 用 runningFor 判会让这张表被任意字符串无限撑大。
// 写侧仍然用 runningFor(懒建 + CAS 是必要的)。
func (s *Server) runningOf(key string) bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	b, ok := s.runBySession[key]
	return ok && b.Load()
}

// runningSessionsOf 当前有回合在跑的**会话 id**(排序;诊断与多窗口 UI 用)。
//
// 为何把归一键换回 id:闸门内部用归一键(空 = 主会话),但对外必须说清「**哪个**会话」。
// 主会话本身也有 id(CwdSessions.CurrentSession()),空键就映射成它 —— 否则前端拿到的
// "" 无法对应到会话列表里的任何一行,「哪个会话在跑」就成了答不出来的问题。
// 拿不到会话服务(cs 未装配)时才保留空串,那时也没有会话列表可对应。
func (s *Server) runningSessionsOf() []string {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	cur := ""
	if s.cs != nil {
		cur = s.cs.CurrentSession()
	}
	out := make([]string, 0, len(s.runBySession))
	for k, b := range s.runBySession {
		if !b.Load() {
			continue
		}
		if k == "" {
			out = append(out, cur)
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// scopeOf 解析会话 id 并取对应日志(读侧用)。
// 缺省 / 等于当前打开的会话 → 主单例(零行为变化)。
func (s *Server) scopeOf(id string) (sessionScope, error) {
	key := s.sessionKey(id)
	if key == "" {
		return sessionScope{Log: s.sessions}, nil
	}
	if s.sdir == nil {
		return sessionScope{}, errSessionScopedUnsupported
	}
	lg, err := s.sdir.Acquire(key)
	if err != nil {
		return sessionScope{}, err
	}
	return sessionScope{ID: key, Log: lg, release: func() { s.sdir.Release(key) }}, nil
}

// sessionScopedError 会话作用域相关的显式失败文案。
type sessionScopedError string

func (e sessionScopedError) Error() string { return string(e) }

var errSessionScopedUnsupported = sessionScopedError(
	"当前构建未装配会话目录(缺 ctx.sessionDir),只能操作当前会话")

// guardWrite 写侧闸门:非当前会话一律 409 并说明原因(不静默写错会话)。
// 返回 true = 允许继续。
func (s *Server) guardWrite(w http.ResponseWriter, id string) bool {
	if s.sessionKey(id) == "" {
		return true
	}
	// 放行的唯一条件:agent-loop 已声明支持按会话执行(方案 B-1 的能力接口)。
	if _, ok := s.loop.(sdk.SessionRunner); ok {
		return true
	}
	http.Error(w, "当前版本尚未支持向指定会话提交(多会话并行回合未落地);请先 /session 切换到该会话再提交", http.StatusConflict)
	return false
}

// sessionPrefsSvc 会话偏好读数(可选窄接口);未装配 ⇒ 所有会话都跟随全局。
func (s *Server) sessionPrefsSvc() sdk.SessionPrefsSource {
	if s.cs == nil {
		return nil
	}
	src, _ := s.cs.(sdk.SessionPrefsSource)
	return src
}

// sessionPrefsOf 读会话显式设置过的偏好(原始值;空 = 没设 = 跟随全局)。
func (s *Server) sessionPrefsOf(sessionID string) sdk.SessionPrefs {
	if src := s.sessionPrefsSvc(); src != nil {
		return src.SessionPrefsOf(sessionID)
	}
	return sdk.SessionPrefs{}
}

// setSessionPref 读-改-写会话级偏好(mutate 决定改哪些项)。
//
// 为什么是 mutate 而不是"传一份要写的值":单项 setter 在两个页面同时改不同项时会丢更新;
// 而且"清除某项"(置空 = 跟随全局)必须能被表达 —— 传值式接口里"空串"和"没传"分不清。
func (s *Server) setSessionPref(sessionID string, mutate func(*sdk.SessionPrefs)) error {
	if s.cs == nil {
		return errSessionScopedUnsupported
	}
	type prefsSetter interface {
		SetSessionPrefs(id string, p sdk.SessionPrefs) error
	}
	st, ok := s.cs.(prefsSetter)
	if !ok {
		return errSessionScopedUnsupported
	}
	p := s.sessionPrefsOf(sessionID)
	mutate(&p)
	return st.SetSessionPrefs(sessionID, p)
}
