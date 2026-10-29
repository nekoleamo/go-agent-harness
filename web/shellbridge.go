// 桌面壳控制面的同源入口(Go/Wails 壳专用,env 未设时完全不挂载)。
//
// 为什么需要这一层(2026-10-29 实测踩到):SPA 的 CSP 是 `connect-src 'self'`
// (见 guard.go spaCSP,那是为了切断 UI 插件「不经服务端外发」的通道),而桌面壳的控制面
// 在**另一个回环端口**上 —— 页面里的 fetch 被浏览器直接拦掉,表现就是「壳的按钮点了没反应」
// (壳侧一条请求都收不到,日志里连 401 都没有)。跨源这条路走不通,也不该为了它放宽 CSP。
//
// 所以改成同源代理:页面请求 `/shell/*` → gah 转发到壳的控制面。顺带有两个好处:
//  1. 控制面 token 留在**sidecar 的 env 里**,不再经 URL fragment 进页面 ——
//     跑在页面里的 UI 插件 JS 拿不到它;
//  2. 不需要 CORS/预检;鉴权由 gah 自己的 authMiddleware 统一负责(token 模式同样受保护)。
//
// 口径:只转发到**回环**地址;只在 GAH_SHELL_CTL_ADDR + GAH_SHELL_CTL_TOKEN 都存在时挂载。
package web

import (
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

// shellBridgeHandler 构造同源代理;env 缺失或地址非回环时返回 nil(不挂载)。
func shellBridgeHandler() http.Handler {
	addr := strings.TrimSpace(os.Getenv("GAH_SHELL_CTL_ADDR"))
	token := strings.TrimSpace(os.Getenv("GAH_SHELL_CTL_TOKEN"))
	if addr == "" || token == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !isLoopbackHost(host) {
		return nil // 只允许回环:这是本机进程间通道,不接受远端目标
	}
	target := &url.URL{Scheme: "http", Host: addr}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(req *http.Request) {
		base(req)
		req.Host = target.Host
		req.Header.Set("X-Shell-Token", token)
		// 不把 gah 的凭据带给壳,也不带 Cookie(壳不需要,少一条泄露路径)。
		req.Header.Del("Cookie")
		// 壳侧守卫按 Origin 白名单判断「是不是自家页面」;代理之后 Origin 是 gah 自己的,
		// 那一层由 gah 的 authMiddleware 替代,所以这里去掉,免得被壳当外部源拒掉。
		req.Header.Del("Origin")
		req.Header.Del("Referer")
	}
	// 目录选择框/备份/安装都可能阻塞很久(用户翻目录、下 30MB):不要让代理自带超时打断。
	proxy.Transport = &http.Transport{
		Proxy:                 nil,
		ResponseHeaderTimeout: 0,
		IdleConnTimeout:       30 * time.Second,
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		// 壳没起/已退出:给一句能查的话,而不是空 502。
		http.Error(w, "桌面壳控制面不可达:"+err.Error(), http.StatusBadGateway)
	}
	return proxy
}

// isLoopbackHost 判断 host 是否是回环(IP 字面量或 localhost)。
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
