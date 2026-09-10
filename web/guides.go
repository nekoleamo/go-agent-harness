// 首启引导端点(G-E4-R):/api/guides —— 桌面壳首启「连接 IM 远程控制」提示的关闭记录。
//
// 语义:GET 返回已关闭的引导 id 列表;POST {id} 记下「不再提示」(幂等)。
// 偏好落在宿主共享 $GAH_HOME/config/gah-state.json(internal/prefs;GAH_HOME 未设 = 纯内存)。
//
// 安全:id 只用于本地偏好键,做白名单式校验(短、字符集受限)防脏写;不涉凭证。
package web

import (
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
)

// guideIDRe 引导 id 允许的字符集(小写字母/数字/短横/下划线,1..64)。
var guideIDRe = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// guidesResp 引导关闭状态响应。
type guidesResp struct {
	Dismissed []string `json:"dismissed"`
}

// handleGuidesList GET /api/guides:已关闭的首启引导 id。
func (s *Server) handleGuidesList(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, guidesResp{Dismissed: prefs.Load().DismissedGuides})
}

// handleGuideDismiss POST /api/guides {id}:记录「不再提示」(幂等)。
func (s *Server) handleGuideDismiss(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil {
		http.Error(w, "请求体非法: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !guideIDRe.MatchString(req.ID) {
		http.Error(w, "非法引导 id(允许 [a-z0-9_-]{1,64})", http.StatusBadRequest)
		return
	}
	prefs.SetGuideDismissed(req.ID)
	writeJSON(w, http.StatusOK, guidesResp{Dismissed: prefs.Load().DismissedGuides})
}
