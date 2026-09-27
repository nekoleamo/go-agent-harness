// 共享 HTTP client(M6.14):web_fetch 与 web_search 复用同一 client——
// 统一 30s 超时/UA/响应体上限,避免各自造轮子导致超时口径不一致。
//
// 内网守卫(安全审计 F3):web_fetch 抓取的是**模型给出的任意 URL**,默认拒绝
// 环回/私网/链路本地目标(SSRF:本机未鉴权服务、云元数据端点 169.254.169.254),
// 逃生舱 GAH_WEB_ALLOW_PRIVATE=1. 守卫只作用于 web_fetch:web_search 的端点
// 是用户在 search.yaml 选定的,可能就是本地搜索服务。
//
// 经代理(HTTP(S)_PROXY)时的豁免(A10):代理模式下**拨号面看到的是代理地址**(本地代理常为
// 127.0.0.1),拿它判内网 = 把整个代理出口误伤掉。故在 Transport 外层(能同时看到逻辑目标与代理
// 选择)做两件事:① 对**逻辑目标**跑一次守卫(字面量内网 IP / localhost 仍拒 —— 本地代理是能从
// 本机访问内网服务的),② 给 ctx 打标,让 guardedDial 知道"这次拨的是代理",跳过拨号地址判定。
//
// 已知限制(登记):`http.ProxyFromEnvironment` 有**进程级一次性 env 缓存**(Go 标准库
// `envProxyOnce`)→ 代理 env 改了要重启进程才生效(与 Go 自身行为一致)。
package toolweb

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
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
	return &http.Client{Timeout: fetchTimeout, Transport: &fetchTransport{base: tr}}
}

// fetchTransport 内网守卫的 Transport 外层:代理模式下改判**逻辑目标**并给 ctx 打标。
type fetchTransport struct{ base *http.Transport }

// proxyDialMark ctx 标记:本次拨的是代理(不是目标自身)。
type proxyDialMark struct{}

func (t *fetchTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if allowPrivate() || req == nil || req.URL == nil {
		return t.base.RoundTrip(req)
	}
	if proxy, err := t.base.Proxy(req); err == nil && proxy != nil {
		if err := guardLogicalHost(req.URL.Hostname()); err != nil {
			return nil, err
		}
		req = req.WithContext(context.WithValue(req.Context(), proxyDialMark{}, true))
	}
	return t.base.RoundTrip(req)
}

// guardLogicalHost 代理模式下对**逻辑目标**的守卫:字面量内网 IP 与 localhost 仍拒;
// 普通域名交给代理解析(本机解析结果不代表隧道出口看到的东西,DNS rebinding 判定在这里无意义)。
func guardLogicalHost(host string) error {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" {
		return nil
	}
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return fmt.Errorf("web_fetch: 拒绝访问本机地址 %s(防 SSRF;确需访问本地服务请设 %s=1)", host, allowPrivateEnv)
	}
	if ip := net.ParseIP(h); ip != nil && blockedIP(ip) {
		return errBlocked(ip)
	}
	return nil
}

// guardedDial 按**实际连上的地址**判定内网:字面量 IP 在拨号前就拒,
// 域名在连接建立后校验解析结果(DNS rebinding 无窗口)。
// ctx 带 proxyDialMark = 本次拨的是代理地址(目标在隧道里):拨号层不做内网判定。
func guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	marked, _ := ctx.Value(proxyDialMark{}).(bool)
	if !allowPrivate() && !marked {
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
	if allowPrivate() || marked {
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
