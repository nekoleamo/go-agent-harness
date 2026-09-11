// 安全护栏(M17 硬化):来源校验 + Host 白名单 + 请求体类型约束 + 安全响应头。
//
// 背景:web 服务默认只绑定回环地址,但**浏览器中的任意页面**都能向 127.0.0.1 发起
// CORS 简单请求(无预检),从而驱动宿主执行工具(POST /api/tools/{name}、/api/input);
// DNS rebinding 更能让恶意域与本地地址"同源"(Origin 与 Host 同为攻击域),
// 从而读走全量事件流、明文 api_key,甚至代用户批准审批弹层。
//
// 本中间件在生产/导出路径(Handler()/Start())上兜底四件事:
//  1. Host 白名单         —— 拦截 DNS rebinding(仅回环形式或显式监听地址的 host 可通过)
//  2. Origin/Referer 校验 —— 非 GET/HEAD 的跨站请求直接 403(CSRF)
//  3. Content-Type 约束   —— 状态变更请求须 application/json 或 multipart/form-data
//     (这两种都不是 CORS 简单类型,浏览器必须预检 → 跨站请求被浏览器先行拦下)
//  4. 安全响应头          —— frame-ancestors/X-Frame-Options 禁 iframe 嵌套(审批弹层防点击劫持)、nosniff、同源 Referer
//
// 鉴权不在本文件:data.auth_token 非空时由 authMiddleware 对**全表面**判凭据(/api/* 缺凭据 401,
// 其它路径返回引导页),见 server.go 与 bootstrap.go。
//
// 包内测试直挂 handler()(不套护栏),护栏行为由 guard_test.go 单测覆盖。
package web

import (
	"net/http"
	"net/url"
	"strings"
)

// guardMiddleware 生产护栏栈(Handler()/Start() 使用)。
const (
	// apiBodyLimit 普通 API 请求体上限(JSON/表单都很小;防无界读)。
	apiBodyLimit = 1 << 20 // 1 MiB
	// attachmentBodyLimit 附件上传上限(图片/文件需大额)。
	attachmentBodyLimit = 64 << 20 // 64 MiB
)

func (s *Server) guardMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w)
		if !s.hostAllowed(r.Host) {
			// DNS rebinding:攻击域解析到本机时 Host 为攻击域,必须拒绝
			http.Error(w, "host 不被允许(仅本机/显式监听地址可访问)", http.StatusForbidden)
			return
		}
		if isStateChanging(r.Method) {
			if origin := requestOrigin(r); origin != "" && !sameOrigin(origin, r.Host) {
				// 跨站状态变更(CSRF):浏览器必带 Origin,原生客户端不带 → 放行无 Origin 请求
				http.Error(w, "跨站请求被拒绝", http.StatusForbidden)
				return
			}
			if consumesBody(r.Method) && strings.HasPrefix(r.URL.Path, "/api/") &&
				r.URL.Path != authPath && // 引导通道:凭据可仅走 Authorization 头(无 body);跨站仍被 Origin 校验拦下
				!contentTypeAllowed(r.Header.Get("Content-Type")) {
				// 强制浏览器预检:跨站 simple request(text/plain 等)在此被拒
				http.Error(w, "请求体须为 application/json 或 multipart/form-data", http.StatusUnsupportedMediaType)
				return
			}
			// 请求体上限(防无界读进内存/写盘):附件上传单独给大额,其余 API 1 MiB
			if consumesBody(r.Method) {
				limit := int64(apiBodyLimit)
				if strings.HasPrefix(r.URL.Path, "/api/attachments") {
					limit = attachmentBodyLimit
				}
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// setSecurityHeaders 统一安全响应头(不覆盖端点自设的 CSP:端点用 Set 覆盖即可)。
func setSecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "frame-ancestors 'none'")
	h.Set("Referrer-Policy", "same-origin")
}

// spaCSP 前端主页面(静态 SPA)专属 CSP,由 staticHandler 逐响应设置(不影响 /api/*、
// /attachments/、文档光栅端点——它们各自的自设 CSP 或全局头保持原样)。
//
// 为什么需要:UI 插件经动态 import() 进同源同 realm 的主页面,若无 CSP,已安装插件
// 可把整个会话内容外发到任意主机。本 CSP 不改变插件能力(仍是同源可信代码,见 UIPlugin
// 的 trust_note),只切断"不经服务端的外发"这一条通道。
//
// 逐项依据(改前先确认,勿凭习惯放宽):
//   - default-src 'none'          未列举的一律不发(manifest/worker 等也归此)
//   - script-src 'self'           index.html 无内联脚本,产物是外链模块;UI 插件动态 import 同源
//   - style-src 'self' …'unsafe-inline'  Vue 的 :style 绑定与运行时注入样式
//   - img-src/media-src blob:     附件图片预览用 URL.createObjectURL
//   - connect-src 'self'          WS/EventSource 均同源(CSP3 下 'self' 覆盖同源 ws/wss)
//   - frame-src 'self'            文档预览"同源 iframe"(DocPanel 的 PDF/转换产物/HTML 沙箱预览),
//     不能用 'none'(会直接弄坏预览);跨源嵌套仍被挡
//   - object-src 'none'           禁插件嵌入
//   - base-uri/form-action 'none' 禁改 base 与表单外发(前端无 form/target,_blank)
const spaCSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self'; connect-src 'self'; " +
	"frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// withSPACSP 给静态 SPA 响应加 CSP(其余路径不经过它,故不受影响)。
func withSPACSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", spaCSP)
		next.ServeHTTP(w, r)
	})
}

// isStateChanging 是否为可产生副作用的方法(CSRF 关注面)。
func isStateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// consumesBody 是否会带请求体(POST/PUT/PATCH;DELETE 无体且本就非 CORS 简单方法)。
func consumesBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	}
	return false
}

// contentTypeAllowed 请求体类型白名单(JSON / multipart 上传)。
func contentTypeAllowed(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return false
	}
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct == "application/json" || ct == "multipart/form-data"
}

// requestOrigin 取请求来源:Origin 优先,缺失回退 Referer 的 scheme://host(仅用于同源判定)。
func requestOrigin(r *http.Request) string {
	if o := strings.TrimSpace(r.Header.Get("Origin")); o != "" {
		return o
	}
	ref := strings.TrimSpace(r.Header.Get("Referer"))
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// hostAllowed Host 白名单:回环形式 + 显式监听地址的 host。
// 显式监听通配地址(0.0.0.0/::/空 addr)时宿主主动暴露到网络 → 放行任意 Host
// (此时仍受 Origin 同源校验与 auth_token 保护)。
func (s *Server) hostAllowed(host string) bool {
	h := hostOnly(host)
	if h == "" {
		return false
	}
	addrHost := hostOnly(s.cfg.Addr)
	if addrHost == "0.0.0.0" || addrHost == "::" || addrHost == "[::]" {
		return true
	}
	if addrHost == "" {
		addrHost = "127.0.0.1" // 未显式配置:与 Start 的默认监听(127.0.0.1:2233)一致
	}
	switch h {
	case "localhost", "127.0.0.1", "::1", "[::1]", addrHost:
		return true
	}
	return false
}

// hostOnly 去掉端口并小写(兼容 IPv6 字面量 [::1]:2233)。
func hostOnly(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "[") {
		if i := strings.IndexByte(h, ']'); i > 0 {
			return h[:i+1]
		}
		return h
	}
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[:i], ":") {
		return h[:i]
	}
	return h
}
