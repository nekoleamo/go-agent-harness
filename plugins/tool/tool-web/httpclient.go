// 共享 HTTP client(M6.14):web_fetch 与 web_search 复用同一 client——
// 统一 30s 超时/UA/响应体上限,避免各自造轮子导致超时口径不一致。
//
// 内网守卫(安全审计 F3):web_fetch 抓取的是**模型给出的任意 URL**,默认拒绝
// 环回/私网/链路本地目标(SSRF:本机未鉴权服务、云元数据端点 169.254.169.254),
// 逃生舱 GAH_WEB_ALLOW_PRIVATE=1. 守卫只作用于 web_fetch:web_search 的端点
// 是用户在 search.yaml 选定的,可能就是本地搜索服务。
//
// 经代理(HTTP(S)_PROXY)时守卫看到的是代理地址(本地代理常为 127.0.0.1):
// 不豁免 —— 出口在隧道里,守卫本就看不见真实目标;这类用户设逃生舱即可,失败是显式的。
package toolweb

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

const (
	fetchTimeout = 30 * time.Second
	maxBody      = 1 << 20 // 1MB 响应体上限
)

// userAgent 统一 UA(fetch/search 同源,便于目标站识别与限流配额)。
const userAgent = "gah-web-tool/1.0 (go-agent-harness)"

// allowPrivateEnv 放行内网目标(fetch 专用)。
const allowPrivateEnv = "GAH_WEB_ALLOW_PRIVATE"

// newHTTPClient 构造共享 client(不含内网守卫:search 端点由用户配置)。
func newHTTPClient() *http.Client {
	return &http.Client{Timeout: fetchTimeout}
}

// newFetchClient 构造带内网守卫的 client(web_fetch 专用)。
func newFetchClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone() // 保留代理等既有语义
	tr.DialContext = guardedDial
	return &http.Client{Timeout: fetchTimeout, Transport: tr}
}

// guardedDial 按**实际连上的地址**判定内网:字面量 IP 在拨号前就拒,
// 域名在连接建立后校验解析结果(DNS rebinding 无窗口)。
func guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	if !allowPrivate() {
		if h, _, err := net.SplitHostPort(addr); err == nil {
			if ip := net.ParseIP(h); ip != nil && blockedIP(ip) {
				return nil, errBlocked(ip)
			}
		}
	}
	d := &net.Dialer{Timeout: fetchTimeout}
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if allowPrivate() {
		return conn, nil
	}
	host, _, _ := net.SplitHostPort(addr)
	if ra, ok := conn.RemoteAddr().(*net.TCPAddr); ok { // tcp 恒成立:以连上的远端为准
		host = ra.IP.String()
	}
	if ip := net.ParseIP(host); ip != nil && blockedIP(ip) {
		conn.Close()
		return nil, errBlocked(ip)
	}
	return conn, nil
}

// errBlocked 统一的可读拒绝原因(带逃生舱提示)。
func errBlocked(ip net.IP) error {
	return fmt.Errorf("web_fetch: 拒绝访问内网/环回/链路本地地址 %s(防 SSRF;"+
		"若这是你配置的 HTTP 代理,或确需访问本地服务,设 %s=1)", ip, allowPrivateEnv)
}

// blockedIP 判定内网类地址。
func blockedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast()
}

// allowPrivate 内网放行开关(每次读取:测试与用户在运行期都改得动)。
func allowPrivate() bool {
	v := os.Getenv(allowPrivateEnv)
	return v == "1" || v == "true"
}
