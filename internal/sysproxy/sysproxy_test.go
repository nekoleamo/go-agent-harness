package sysproxy

import (
	"strings"
	"testing"
)

// 真实 `scutil --proxy` 输出(2026-10-09 本机实测形状:HTTP/HTTPS/SOCKS 全开,排除列表三项)。
const scutilAll = `<dictionary> {
  ExceptionsList : <array> {
    0 : localhost
    1 : 127.0.0.0/8
    2 : ::1
  }
  HTTPEnable : 1
  HTTPPort : 10808
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 10808
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 10808
  SOCKSProxy : 127.0.0.1
}
`

func TestPlanPrefersHTTPS(t *testing.T) {
	p, ok := Plan(scutilAll)
	if !ok {
		t.Fatal("应解出系统代理")
	}
	if p.URL != "http://127.0.0.1:10808" {
		t.Fatalf("主代理应为 HTTPS 那条,实际 %q", p.URL)
	}
	v := p.Vars()
	if v["HTTPS_PROXY"] != "http://127.0.0.1:10808" || v["HTTP_PROXY"] != "http://127.0.0.1:10808" {
		t.Fatalf("HTTP(S)_PROXY 都该指向系统代理,实际 %v", v)
	}
	for _, want := range []string{"localhost", "127.0.0.1", "::1", "127.0.0.0/8"} {
		if !strings.Contains(v["NO_PROXY"], want) {
			t.Fatalf("NO_PROXY 应含系统排除项 %q,实际 %q", want, v["NO_PROXY"])
		}
	}
}

func TestPlanHTTPOnlyStillTunnelsHTTPS(t *testing.T) {
	// 只开 HTTP:HTTP 代理经 CONNECT 转发 HTTPS 是标准用法,HTTPS_PROXY 也要喂同一个 URL。
	out := "<dictionary> {\n  HTTPEnable : 1\n  HTTPPort : 7890\n  HTTPProxy : 10.0.0.2\n}\n"
	p, ok := Plan(out)
	if !ok {
		t.Fatal("应有代理")
	}
	if p.URL != "http://10.0.0.2:7890" {
		t.Fatalf("实际 %q", p.URL)
	}
	if v := p.Vars(); v["HTTPS_PROXY"] != "http://10.0.0.2:7890" {
		t.Fatalf("只有 HTTP 时 HTTPS_PROXY 也须可用,实际 %q", v["HTTPS_PROXY"])
	}
}

func TestPlanSocksOnly(t *testing.T) {
	out := "<dictionary> {\n  SOCKSEnable : 1\n  SOCKSPort : 1080\n  SOCKSProxy : ::1\n}\n"
	p, ok := Plan(out)
	if !ok {
		t.Fatal("socks 也算可用代理")
	}
	if p.URL != "socks5://[::1]:1080" {
		t.Fatalf("IPv6 socks 应加方括号,实际 %q", p.URL)
	}
}

func TestPlanDisabled(t *testing.T) {
	out := "<dictionary> {\n  HTTPEnable : 0\n  HTTPSEnable : 0\n  SOCKSEnable : 0\n}\n"
	if _, ok := Plan(out); ok {
		t.Fatal("全关时不该有代理")
	}
}

func TestPlanGarbage(t *testing.T) {
	if _, ok := Plan("not a plist\nnonsense"); ok {
		t.Fatal("认不出的输出不该给代理")
	}
}

func TestVarsDedupNoProxy(t *testing.T) {
	p, _ := Plan(scutilAll)
	v := p.Vars()
	// 系统排除项里已有 127.0.0.0/8 与 ::1,回环三项不得重复出现。
	if n := strings.Count(v["NO_PROXY"], "::1"); n != 1 {
		t.Fatalf("::1 应只出现一次,实际 %d 次(%q)", n, v["NO_PROXY"])
	}
}
