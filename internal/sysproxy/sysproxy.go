// Package sysproxy 让 gah 在**没有代理环境变量**时感知操作系统的系统代理设置。
//
// 为什么需要:桌面壳(Finder/开始菜单双击)启动的 gah **不继承 shell 环境**,而 Go 标准库的
// http.ProxyFromEnvironment 只认 HTTP_PROXY/HTTPS_PROXY/NO_PROXY,不读 macOS「系统设置 →
// 网络 → 代理」。装了本机代理(Clash/xray 等)的用户双击打开桌面版时,LLM 请求会直连,
// 被网络侧的自签证书拦截 —— 实测报错:
//
//	llm-openai: models 列表请求失败: … x509: “localhost” certificate is not standards compliant
//
// 而同一台机器的终端里 curl 走 shell 里配的 HTTPS_PROXY 完全正常。终端能通、桌面端不通,
// 差别只在环境变量继承了没有。
//
// 策略(尽量少干预):
//   - 用户**已经设了**任一代理 env(大写/小写)→ 原样尊重,一个字都不动;
//   - 只有全都没设,才读系统代理并注入;
//   - NO_PROXY 只在它为空时才写(本地回环 + 系统排除项),已有内容不覆盖。
//
// 平台:macOS 读 `scutil --proxy`(系统自带的只读查询,零依赖);其它平台 no-op
// (Windows 的 WinINET 代理在注册表里,Go 标准库同样不读 —— 留待需要时再补)。
package sysproxy

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// 代理 env 的四种拼写(Go 标准库大小写都认)。
var proxyEnvKeys = []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"}

// Apply 按上述策略注入系统代理。logf 可为 nil(静默)。
func Apply(logf func(msg string, args ...any)) {
	if runtime.GOOS != "darwin" {
		return
	}
	if anyProxyEnvSet() {
		return // 用户显式配了:不干预(终端/服务器模式都走这条)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "scutil", "--proxy").Output()
	if err != nil {
		return // scutil 不在/超时/失败:当作没有系统代理,不阻断启动
	}
	p, ok := Plan(string(out))
	if !ok {
		return
	}
	for k, v := range p.Vars() {
		// 不覆盖已有的(目前只有 NO_PROXY 会走到这里)
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
	if logf != nil {
		logf("boot: 检测到系统代理(桌面端未继承 shell 环境,已自动采用)", "proxy", p.URL)
	}
}

func anyProxyEnvSet() bool {
	for _, k := range proxyEnvKeys {
		if strings.TrimSpace(os.Getenv(k)) != "" {
			return true
		}
	}
	return false
}

// SystemProxy 从 scutil --proxy 输出解出的可用系统代理。
type SystemProxy struct {
	// URL 主代理(HTTPS 优先;只有 HTTP 时也能经 CONNECT 隧道转发 HTTPS;只有 SOCKS 时为 socks5://)。
	URL string
	// HTTPURL 明文 HTTP 的代理;缺省时用 URL。
	HTTPURL string
	// NoProxy 系统排除列表(ExceptionsList)。
	NoProxy []string
}

// Vars 要注入的环境变量集(键大写;调用方负责不覆盖已有值)。
func (p SystemProxy) Vars() map[string]string {
	httpURL := p.HTTPURL
	if httpURL == "" {
		httpURL = p.URL
	}
	if p.URL == "" {
		return nil
	}
	return map[string]string{
		"HTTPS_PROXY": p.URL,
		"HTTP_PROXY":  httpURL,
		"NO_PROXY":    p.noProxyValue(),
	}
}

// noProxyValue 本地回环恒直连 + 系统排除列表(去重)。
func (p SystemProxy) noProxyValue() string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range append([]string{"localhost", "127.0.0.1", "::1"}, p.NoProxy...) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return strings.Join(out, ",")
}

// Plan 解析 `scutil --proxy` 输出,给出要采用的系统代理(纯函数,便于单测)。
// ok=false 表示没有可用代理(未开启 / 输出不认识)。
func Plan(out string) (SystemProxy, bool) {
	kv := map[string]string{}
	inArray := false
	var exceptions []string
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasSuffix(line, "<array> {") || strings.HasSuffix(line, "<array>{") {
			inArray = true
			continue
		}
		if line == "}" || line == "};" {
			inArray = false
			continue
		}
		k, v, ok := splitKV(line)
		if !ok {
			continue
		}
		if inArray {
			if _, err := strconv.Atoi(k); err == nil { // 数组下标 = 排除项
				exceptions = append(exceptions, v)
			}
			continue
		}
		kv[k] = v
	}

	httpURL := ""
	if kv["HTTPEnable"] == "1" {
		httpURL = httpURLOf(kv["HTTPProxy"], kv["HTTPPort"])
	}
	httpsURL := ""
	if kv["HTTPSEnable"] == "1" {
		httpsURL = httpURLOf(kv["HTTPSProxy"], kv["HTTPSPort"])
	}
	socksURL := ""
	if kv["SOCKSEnable"] == "1" {
		socksURL = schemeURL("socks5", kv["SOCKSProxy"], kv["SOCKSPort"])
	}

	switch {
	case httpsURL != "":
		if httpURL == "" {
			httpURL = httpsURL
		}
		return SystemProxy{URL: httpsURL, HTTPURL: httpURL, NoProxy: exceptions}, true
	case httpURL != "":
		return SystemProxy{URL: httpURL, HTTPURL: httpURL, NoProxy: exceptions}, true
	case socksURL != "":
		// 只开了 SOCKS:Go 的 httpproxy 认 socks5://,http.Transport 支持 SOCKS5 隧道。
		return SystemProxy{URL: socksURL, HTTPURL: socksURL, NoProxy: exceptions}, true
	}
	return SystemProxy{}, false
}

// splitKV 拆 "KEY : VALUE"(scutil --proxy 的行格式)。
func splitKV(line string) (k, v string, ok bool) {
	i := strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

func httpURLOf(host, port string) string { return schemeURL("http", host, port) }

// schemeURL 拼 scheme://host:port(host 已带 scheme 时原样返回;端口/主机缺失时尽量降级)。
func schemeURL(scheme, host, port string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if strings.Contains(host, "://") {
		return host
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") { // IPv6 字面量
		host = "[" + host + "]"
	}
	if p := strings.TrimSpace(port); p != "" {
		host += ":" + p
	}
	return scheme + "://" + host
}
