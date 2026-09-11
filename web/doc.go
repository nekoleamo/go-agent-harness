// 文档预览端点(D1):/api/doc/*——预览(块模型 JSON)、原生字节(Range/下载)、
// 内嵌资产(白名单 MIME)、文件树、HTML 沙箱、markdown 文本渲染。
//
// 全部挂 authMiddleware;全部经 host-docview 的统一 resolver(沙箱 + 逃逸校验 + deny-list,
// strict 模式:根集合 = workspace ∪ $GAH_HOME/attachments)。未装配 ctx.doc 时**不注册路由**。
package web

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// docMaxRenderBytes /api/doc/render 请求体上限(会话流单条文本)。
const docMaxRenderBytes = 256 << 10

// docErrStatus 把 sdk 哨兵错误映射为 HTTP 状态码。
func docErrStatus(err error) (int, string) {
	switch {
	case errors.Is(err, sdk.ErrDocDenied):
		return http.StatusForbidden, "路径不被允许"
	case errors.Is(err, sdk.ErrDocNotFound):
		return http.StatusNotFound, "文件不存在"
	case errors.Is(err, sdk.ErrDocTooLarge):
		return http.StatusRequestEntityTooLarge, "超出字节预算"
	case errors.Is(err, sdk.ErrDocUnsupported):
		return http.StatusUnsupportedMediaType, "不支持的格式"
	case errors.Is(err, sdk.ErrDocParse):
		return http.StatusUnprocessableEntity, "解析失败"
	default:
		return http.StatusBadRequest, "请求无效"
	}
}

// docRequest 从查询参数构造请求(strict 恒真:Web 端不允许任意绝对路径)。
func docRequest(r *http.Request) sdk.DocRequest {
	q := r.URL.Query()
	req := sdk.DocRequest{Path: q.Get("path"), Strict: true}
	if v := q.Get("page"); v != "" {
		req.Page, _ = strconv.Atoi(v)
	}
	if v := q.Get("sheet"); v != "" {
		req.Sheet, _ = strconv.Atoi(v)
	}
	if v := q.Get("max"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			req.MaxBytes = n
		}
	}
	if v := q.Get("lines"); v != "" {
		req.Limit, _ = strconv.Atoi(v)
	}
	if v := q.Get("offset"); v != "" {
		req.Offset, _ = strconv.Atoi(v)
	}
	return req
}

// docSvc 懒解析文档服务:每次请求现取并缓存(避免插件启动顺序依赖)。
// 并发首请求需互斥:多个请求同时写 s.doc 会构成数据竞争(-race 可捕获)。
func (s *Server) docSvc(w http.ResponseWriter) (sdk.DocService, bool) {
	s.docMu.Lock()
	cached := s.doc
	s.docMu.Unlock()
	if cached != nil {
		return cached, true
	}
	if s.ctx != nil {
		var d sdk.DocService
		if err := s.ctx.Inject("ctx.doc", &d); err == nil && d != nil {
			s.docMu.Lock()
			s.doc = d
			s.docMu.Unlock()
			return d, true
		}
	}
	http.Error(w, "文档预览未装配(缺 ctx.doc / host-docview)", http.StatusServiceUnavailable)
	return nil, false
}

// handleDocPreview GET /api/doc/preview?path=&page=&sheet=&max= → DocView JSON。
func (s *Server) handleDocPreview(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.docSvc(w)
	if !ok {
		return
	}
	req := docRequest(r)
	if strings.TrimSpace(req.Path) == "" {
		http.Error(w, "缺少 path 参数", http.StatusBadRequest)
		return
	}
	v, err := svc.Preview(r.Context(), req)
	if err != nil {
		code, msg := docErrStatus(err)
		http.Error(w, msg, code)
		return
	}
	// RawURL 供前端下载/原生查看器使用(自身路径,无宿主绝对路径)。
	// 浅拷贝后改:svc.Preview 可能返回缓存内的共享 *DocView,原地改写会与其他
	// 并发请求竞争(同路径两个请求命中缓存时)。
	out := *v
	out.RawURL = "/api/doc/raw?path=" + url.QueryEscape(req.Path)
	writeJSON(w, http.StatusOK, &out)
}

// handleDocRaw GET /api/doc/raw?path=&dl=1:原生字节,支持 Range(浏览器 PDF 查看器必需)。
func (s *Server) handleDocRaw(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.docSvc(w)
	if !ok {
		return
	}
	req := docRequest(r)
	if strings.TrimSpace(req.Path) == "" {
		http.Error(w, "缺少 path 参数", http.StatusBadRequest)
		return
	}
	rc, mimeType, err := svc.Raw(r.Context(), req)
	if err != nil {
		code, msg := docErrStatus(err)
		http.Error(w, msg, code)
		return
	}
	defer rc.Close()

	name := filepath.Base(req.Path)
	disposition := "inline"
	if r.URL.Query().Get("dl") != "" || isDangerousInline(mimeType) {
		// text/html 与未知类型**永不 inline**(防同源 XSS);下载名经 FormatMediaType 转义
		disposition = "attachment"
	}
	disp := mime.FormatMediaType(disposition, map[string]string{"filename": name})
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", disp)
	w.Header().Set("Content-Type", mimeType)
	http.ServeContent(w, r, name, time.Time{}, rc)
}

// isDangerousInline 不允许内联呈现的类型(HTML/SVG 等可执行文档内容)。
func isDangerousInline(mimeType string) bool {
	m := strings.ToLower(mimeType)
	if strings.HasPrefix(m, "image/") && !strings.Contains(m, "svg") {
		return false
	}
	switch {
	case strings.Contains(m, "html"), strings.Contains(m, "svg"), strings.Contains(m, "xml"),
		strings.Contains(m, "javascript"), strings.Contains(m, "octet-stream"):
		return true
	}
	return false
}

// docAssetInlineOK 资产端点可内联呈现的 MIME 白名单:图片(排除 svg)+ PDF。
// PDF 与 /api/doc/raw 的内联查看同风险等级(浏览器原生查看器,无脚本执行面);
// D6-2 外部转换器产物经此路径呈现(高保真预览档)。
func docAssetInlineOK(mimeType string) bool {
	m := strings.ToLower(mimeType)
	if strings.Contains(m, "svg") || strings.Contains(m, "html") || strings.Contains(m, "javascript") {
		return false
	}
	return strings.HasPrefix(m, "image/") || strings.HasPrefix(m, "application/pdf")
}

// handleDocAsset GET /api/doc/asset?path=&id=:docx/pptx 内嵌图 / markdown 同目录图 / 转换 PDF。
func (s *Server) handleDocAsset(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.docSvc(w)
	if !ok {
		return
	}
	req := docRequest(r)
	id := r.URL.Query().Get("id")
	if strings.TrimSpace(req.Path) == "" || id == "" {
		http.Error(w, "缺少 path/id 参数", http.StatusBadRequest)
		return
	}
	rc, mimeType, err := svc.Asset(r.Context(), req, id)
	if err != nil {
		code, msg := docErrStatus(err)
		http.Error(w, msg, code)
		return
	}
	defer rc.Close()
	// 资产端点 MIME 白名单:图片(排除 svg)+ PDF
	if !docAssetInlineOK(mimeType) {
		http.Error(w, "资产类型不允许: "+mimeType, http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = io.Copy(w, io.LimitReader(rc, 20<<20))
}

// handleDocRaster GET /api/doc/raster?path=&page=1&dpi=96:PDF 页光栅化(RST-1/D6-1a)。
// 服务端经外部 pdftoppm 渲染(需 host-docview data.external_raster);返回 PNG 字节。
func (s *Server) handleDocRaster(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.docSvc(w)
	if !ok {
		return
	}
	rs, ok := svc.(sdk.DocRasterService)
	if !ok {
		http.Error(w, "光栅预览未启用(需 host-docview data.external_raster 与本机 poppler)",
			http.StatusServiceUnavailable)
		return
	}
	req := docRequest(r)
	if strings.TrimSpace(req.Path) == "" {
		http.Error(w, "缺少 path 参数", http.StatusBadRequest)
		return
	}
	page := atoiDefault(r.URL.Query().Get("page"), 1)
	dpi := atoiDefault(r.URL.Query().Get("dpi"), 0)
	out, err := rs.Raster(r.Context(), req, page, dpi)
	if err != nil {
		code, msg := docErrStatus(err)
		http.Error(w, msg, code)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = w.Write(out.Data)
}

// atoiDefault 宽松整数解析(空/非法 → def)。
func atoiDefault(s string, def int) int {
	if strings.TrimSpace(s) == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}

// handleDocTree GET /api/doc/tree?path=&depth=2:工作台文件树。
func (s *Server) handleDocTree(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.docSvc(w)
	if !ok {
		return
	}
	req := docRequest(r)
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	tree, err := svc.List(r.Context(), req, depth)
	if err != nil {
		code, msg := docErrStatus(err)
		http.Error(w, msg, code)
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

// handleDocHTML GET /api/doc/html?path=:HTML 沙箱预览(独立内容路由,CSP 禁一切外联与脚本)。
// 前端 iframe 另加 sandbox(无 allow-scripts)再兜一层。
func (s *Server) handleDocHTML(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.docSvc(w)
	if !ok {
		return
	}
	req := docRequest(r)
	if strings.TrimSpace(req.Path) == "" {
		http.Error(w, "缺少 path 参数", http.StatusBadRequest)
		return
	}
	rc, _, err := svc.Raw(r.Context(), req)
	if err != nil {
		code, msg := docErrStatus(err)
		http.Error(w, msg, code)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src data:; style-src 'unsafe-inline'")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.Copy(w, io.LimitReader(rc, 2<<20))
}

// handleDocRender POST /api/doc/render{text}:markdown 文本 → 块模型(会话流渲染)。
// 服务端解析,前端零 markdown 依赖、零 v-html。
func (s *Server) handleDocRender(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.docSvc(w)
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, docMaxRenderBytes)).Decode(&body); err != nil {
		http.Error(w, "请求体无效或超出上限", http.StatusRequestEntityTooLarge)
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		writeJSON(w, http.StatusOK, &sdk.DocView{Format: sdk.DocFormatMarkdown})
		return
	}
	v, err := svc.Render(r.Context(), body.Text, 0)
	if err != nil {
		code, msg := docErrStatus(err)
		http.Error(w, msg, code)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
