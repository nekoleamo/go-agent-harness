// 群维度授权端点(G-E5-2):/api/im/groups —— Web 面板「群授权」区段。
//
// 语义:GET 列举「已授权 ∪ 最近活动」群(含 last_seen / stale 标记);
// POST {chat_id, allow} 授权或撤销(撤销未知群 422,不静默)。
//
// 安全:挂 authMiddleware(与其他 /api/* 一致);载荷只有裸群 openid,不含凭证;
// 未装配 IMGroupAccessService → 503(面板隐藏该区,不静默假装支持)。
package web

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// imGroupService 懒解析群授权能力(对齐 imConnService 的时序纪律)。
func (s *Server) imGroupService() (sdk.IMGroupAccessService, bool) {
	if s.imGroups != nil {
		return s.imGroups, true
	}
	if s.ctx != nil {
		var gs sdk.IMGroupAccessService
		if err := s.ctx.Inject("ctx.imChannels", &gs); err == nil && gs != nil {
			s.imGroups = gs
			return gs, true
		}
	}
	if g, ok := s.imc.(sdk.IMGroupAccessService); ok {
		s.imGroups = g
		return g, true
	}
	return nil, false
}

// groupsResp 群列表响应(前端零转换)。
type groupsResp struct {
	Groups []sdk.IMGroupEntry `json:"groups"`
}

func (s *Server) imGroupsUnavailable(w http.ResponseWriter) {
	http.Error(w, "该渠道不支持群授权管理(未装配 IMGroupAccessService)", http.StatusServiceUnavailable)
}

// handleIMGroups GET /api/im/groups:列举群维度授权与最近活动群。
func (s *Server) handleIMGroups(w http.ResponseWriter, _ *http.Request) {
	svc, ok := s.imGroupService()
	if !ok {
		s.imGroupsUnavailable(w)
		return
	}
	writeJSON(w, http.StatusOK, groupsResp{Groups: svc.Groups()})
}

// imGroupReq 授权/撤销请求体。
type imGroupReq struct {
	ChatID string `json:"chat_id"`
	Allow  bool   `json:"allow"`
}

// handleIMGroupSet POST /api/im/groups:授权或撤销一个群(回最新列表)。
func (s *Server) handleIMGroupSet(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.imGroupService()
	if !ok {
		s.imGroupsUnavailable(w)
		return
	}
	var req imGroupReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	if err := dec.Decode(&req); err != nil {
		http.Error(w, "请求体非法: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ChatID) == "" {
		http.Error(w, "缺少 chat_id", http.StatusBadRequest)
		return
	}
	if err := svc.SetGroupAccess(req.ChatID, req.Allow); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusOK, groupsResp{Groups: svc.Groups()})
}
