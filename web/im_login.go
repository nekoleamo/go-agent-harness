// Web 面板 IM 扫码登录(P3):发起登录 → 返回二维码 PNG(data URI,前端直接 img 展示)
// → 前端轮询进度。渠道经 sdk.IMLoginProvider 提供能力(当前 ui-im-wechat);
// 未实现/未装配 = 端点 503(面板隐藏入口)。
package web

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"

	"rsc.io/qr"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// qrPNGDataURI 把二维码内容编码为 PNG data URI(放大 scale 倍便于扫码;含静默区)。
func qrPNGDataURI(content string, scale int) (string, error) {
	if scale <= 0 {
		scale = 6
	}
	code, err := qr.Encode(content, qr.M)
	if err != nil {
		return "", err
	}
	src := code.Image() // 黑白,自带静默区
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx()*scale, b.Dy()*scale))
	white := color.RGBA{255, 255, 255, 255}
	black := color.RGBA{0, 0, 0, 255}
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := black
			if r, g, bl, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA(); r > 0x8000 && g > 0x8000 && bl > 0x8000 {
				c = white
			}
			for dy := 0; dy < scale; dy++ {
				row := dst.PixOffset(x*scale, y*scale+dy)
				for dx := 0; dx < scale; dx++ {
					dst.Pix[row+dx*4] = c.R
					dst.Pix[row+dx*4+1] = c.G
					dst.Pix[row+dx*4+2] = c.B
					dst.Pix[row+dx*4+3] = c.A
				}
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// handleIMLogin POST /api/im/login:发起扫码登录并返回二维码(含 PNG data URI)。
func (s *Server) handleIMLogin(w http.ResponseWriter, r *http.Request) {
	if s.imLogin == nil {
		http.Error(w, "该渠道不支持面板扫码登录(未装配 IMLoginProvider)", http.StatusServiceUnavailable)
		return
	}
	qr, err := s.imLogin.StartLogin(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	pngURI, perr := qrPNGDataURI(qr.Content, 6)
	resp := map[string]any{
		"channel": qr.Channel, "content": qr.Content, "expires_at": qr.ExpiresAt,
	}
	if perr == nil {
		resp["png"] = pngURI
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleIMLoginState GET /api/im/login/state:登录进度(前端轮询)。
func (s *Server) handleIMLoginState(w http.ResponseWriter, r *http.Request) {
	if s.imLogin == nil {
		http.Error(w, "该渠道不支持面板扫码登录", http.StatusServiceUnavailable)
		return
	}
	st := s.imLogin.LoginState()
	writeJSON(w, http.StatusOK, map[string]any{
		"phase": st.Phase, "detail": st.Detail, "error": st.Error,
	})
}

// IM login 相关类型别名(便于测试断言,避免直接 import sdk 的调用方重复写)。
type imLoginQRResponse struct {
	Channel   string `json:"channel"`
	Content   string `json:"content"`
	ExpiresAt string `json:"expires_at"`
	PNG       string `json:"png,omitempty"`
}

var _ = sdk.IMLoginState{} // 类型引用(避免未使用 import;接口在 sdk)
