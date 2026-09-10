// 会话概述端点(F 组 F3):POST /api/sessions/summary —— 生成/取回概述。
//
// 纪律:本端点会**调用模型**(唯一会触发的路径之一,另一是回合后自动档);
// `GET /api/sessions` 永远只读缓存。未装配 ctx.sessionSummary → 503(前端隐藏入口)。
package web

import (
	"encoding/json"
	"net/http"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// handleSessionSummary POST /api/sessions/summary{id?, force?} → 概述 + 最新会话信息。
func (s *Server) handleSessionSummary(w http.ResponseWriter, r *http.Request) {
	if s.ss == nil {
		http.Error(w, "会话概述未装配(ctx.sessionSummary / host-session-summary)", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		ID    string `json:"id"`    // 空 = 主会话
		Force bool   `json:"force"` // 忽略缓存重新生成
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		http.Error(w, "坏请求体", http.StatusBadRequest)
		return
	}
	sum, err := s.ss.Summary(r.Context(), req.ID, req.Force)
	if err != nil {
		// 生成失败(模型不可用/轮次不足/输出无法解析) → 422 + 结构化文案
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	// 回传最新会话信息(前端据此刷新列表行)
	var info *sdk.SessionInfo
	if s.cs != nil {
		for _, si := range s.cs.Sessions() {
			if si.ID == req.ID {
				cp := si
				info = &cp
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": sum, "session": info, "auto": s.ss.AutoEnabled()})
}
