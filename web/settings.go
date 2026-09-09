// web 设置面板端点:会话压缩(compact)与历史注入(history)的可视化配置承载。
// 其余设置(模型/思考/沙箱/provider/插件/指令热更)复用既有 REST(/api/control、
// /api/providers、/api/plugins、/api/reload),此处仅补 web 原先无的纯宿主能力。
package web

import (
	"encoding/json"
	"net/http"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// —— 运行偏好持久化(思考/沙箱/历史;退出即记,重启恢复)——
// 宿主共享 $GAH_HOME/config/gah-state.json(经 internal/prefs,兼容旧 web-state.json;
// GAH_HOME 未设 = 跳过持久化,纯内存行为)。model 经 providerfile 持久化,不在此文件。

// loadPrefs / savePrefs 委托宿主共享 prefs 包(TUI 与 Web 同一偏好)。
func loadPrefs() prefs.Prefs  { return prefs.Load() }
func savePrefs(p prefs.Prefs) { prefs.Save(p) }

// ApplyPrefs 启动恢复:按上次退出偏好设置思考/沙箱/历史注入(Inject 后调用)。
// 单条非法值跳过(不阻塞 boot);model 由 providerfile 链自行恢复。
func (s *Server) ApplyPrefs() {
	p := prefs.Load()
	if p.Thinking != "" {
		lvl := sdk.ParseThinking(p.Thinking)
		if lvl.String() == p.Thinking && s.llm != nil {
			s.llm.SetThinking(lvl)
		}
	}
	if p.Sandbox != "" && s.sb != nil {
		s.sb.SetMode(sdk.SandboxMode(p.Sandbox))
	}
	if p.History != nil && s.sessions != nil {
		s.sessions.SetHistory(*p.History)
	}
}

// handleCompact 手动滚动压缩(POST /api/compact {prompt?}):
// ctx.sessions 断言 CompactService(未实现 = 501);成功返回摘要与折叠事件数。
// 返回后前端需自行刷新会话流(压缩改变投影,历史经 session/event 广播已实时反映)。
func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		http.Error(w, "会话日志未装配(ctx.sessions)", http.StatusServiceUnavailable)
		return
	}
	cs, ok := s.sessions.(sdk.CompactService)
	if !ok {
		http.Error(w, "会话服务不支持滚动压缩(CompactService)", http.StatusNotImplemented)
		return
	}
	var req struct {
		Prompt string `json:"prompt"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req) // 可选载荷,坏 body 用默认
	if req.Prompt == "" {
		req.Prompt = "手动压缩"
	}
	summary, folded, err := cs.Compact(req.Prompt)
	if err != nil {
		http.Error(w, "压缩失败: "+err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "summary": summary, "folded": folded})
}

// handleSettingsHistory 设置历史注入条数(POST /api/settings/history {n}):
// 语义沿用 SessionLog.SetHistory:n=-1 禁止注入;0 = 全部(unlimited);N>0 = 最近 N 条。
func (s *Server) handleSettingsHistory(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		http.Error(w, "会话日志未装配(ctx.sessions)", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		N int `json:"n"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	if req.N < -1 {
		http.Error(w, "n 取值: -1 禁止 | 0 全部 | N>0 最近 N 条", http.StatusBadRequest)
		return
	}
	s.sessions.SetHistory(req.N)
	// 持久化历史注入偏好(退出即记,重启恢复)
	p := loadPrefs()
	p.History = &req.N
	savePrefs(p)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "history": req.N})
}
