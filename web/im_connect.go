// IM 连接端点(E 组 E0):/api/im/connect/* —— 统一「扫码 / 表单 / 仅状态」三态连接。
//
// 主路径是 **`im/connect` 事件**(经 SSE/WS 推送相位变化);GET /api/im/connect/state 仅作兜底。
// 旧端点 POST /api/im/login 与 GET /api/im/login/state 保留一个版本周期(内部转调新服务)。
//
// 安全:全部挂 authMiddleware;密钥不回显(spec 只给掩码,status 只给尾号);载荷不含凭证。
package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// imConnService 懒解析连接服务(ctx.imChannels 实现方按需同时实现 IMConnectService)。
// 懒解析避免插件启动顺序依赖(对齐 ctx.doc/ctx.confirm 先例)。
func (s *Server) imConnService() (sdk.IMConnectService, bool) {
	if s.imConn != nil {
		return s.imConn, true
	}
	if s.ctx != nil {
		var ic sdk.IMConnectService
		if err := s.ctx.Inject("ctx.imChannels", &ic); err == nil && ic != nil {
			s.imConn = ic
			return ic, true
		}
	}
	if c, ok := s.imc.(sdk.IMConnectService); ok {
		s.imConn = c
		return c, true
	}
	return nil, false
}

// withQRPNG 给带二维码内容的连接状态补 PNG data URI(服务端渲染,前端直接 img 展示)。
func (s *Server) withQRPNG(st sdk.IMConnectStatus) sdk.IMConnectStatus {
	if st.QRContent == "" || st.QRPNG != "" {
		return st
	}
	if uri, err := qrPNGDataURI(st.QRContent, 6); err == nil {
		st.QRPNG = uri
	}
	return st
}

func (s *Server) imConnUnavailable(w http.ResponseWriter) {
	http.Error(w, "该渠道不支持面板连接配置(未装配 IMConnectService)", http.StatusServiceUnavailable)
}

// handleIMConnectSpec GET /api/im/connect/spec:连接方式声明(渲染二维码卡/表单)。
func (s *Server) handleIMConnectSpec(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.imConnService()
	if !ok {
		s.imConnUnavailable(w)
		return
	}
	spec := svc.ConnectSpec()
	// 白名单外链:仅放行平台侧域(防被改成任意跳转)
	if spec.LoginURL != "" && !imLinkAllowed(spec.LoginURL) {
		spec.LoginURL = ""
	}
	if spec.DocsURL != "" && !imLinkAllowed(spec.DocsURL) {
		spec.DocsURL = ""
	}
	writeJSON(w, http.StatusOK, spec)
}

// handleIMConnectStart POST /api/im/connect/start:发起连接(qr:取码;form:返回当前状态)。
func (s *Server) handleIMConnectStart(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.imConnService()
	if !ok {
		s.imConnUnavailable(w)
		return
	}
	st, err := svc.StartConnect(r.Context())
	if err != nil {
		// 连接进行中/取码失败:仍回结构化状态(前端据 phase/error 渲染),HTTP 409 表达"未开始新的"
		writeJSON(w, http.StatusConflict, s.withQRPNG(st))
		return
	}
	writeJSON(w, http.StatusOK, s.withQRPNG(st))
}

// handleIMConnectSubmit POST /api/im/connect/submit:提交表单(qr 渠道显式 400)。
func (s *Server) handleIMConnectSubmit(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.imConnService()
	if !ok {
		s.imConnUnavailable(w)
		return
	}
	spec := svc.ConnectSpec()
	if spec.Kind != sdk.IMConnectForm {
		http.Error(w, "该渠道为扫码连接,无表单配置项", http.StatusBadRequest)
		return
	}
	var body struct {
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		http.Error(w, "请求体无效", http.StatusBadRequest)
		return
	}
	st, err := svc.SubmitConfig(r.Context(), body.Values)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, st)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleIMConnectState GET /api/im/connect/state:状态兜底轮询(读同一状态机)。
func (s *Server) handleIMConnectState(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.imConnService()
	if !ok {
		s.imConnUnavailable(w)
		return
	}
	writeJSON(w, http.StatusOK, s.withQRPNG(svc.ConnectStatus()))
}

// imLinkAllowed 外链白名单(平台侧域);空串放行(视为未提供)。
func imLinkAllowed(u string) bool {
	if strings.TrimSpace(u) == "" {
		return true
	}
	for _, d := range []string{"q.qq.com", "bot.q.qq.com", "wiki.connect.qq.com", "github.com", "weixin.qq.com"} {
		if strings.Contains(u, "//"+d+"/") || strings.HasSuffix(strings.TrimSuffix(u, "/"), "//"+d) {
			return true
		}
	}
	return false
}

// handleIMConnectEvent 事件→SSE 帧推送由 EventHub 完成(见 events.go 的 Subscribe);
// 本函数仅用于测试/外部触达(直接广播一个状态帧)。
func (s *Server) broadcastIMConnect(ctx context.Context, st sdk.IMConnectStatus) {
	s.hub.Push(Frame{Type: FrameIMConnect, Payload: s.withQRPNG(st)})
	_ = ctx
}
