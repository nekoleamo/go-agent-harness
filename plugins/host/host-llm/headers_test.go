// provider 自定义请求头在**宿主侧**的展开与下发(2026-10-06 随 OpenCode Go 接入)。
//
// 为什么这层要单独测:占位符展开与会话 id 的取法都在宿主,适配器只收最终值。
// 取错了不会编译失败,只会在真机上表现为「网关说没有会话头」或「限速规则没生效」。
package hostllm

import (
	"os"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/providerfile"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// headerStub 记录收到的头(实现 sdk.HeaderConfigurable)。
type headerStub struct {
	providerAdapter
	got sdk.ProviderHeaders
}

func (a *headerStub) ConfigureHeaders(h sdk.ProviderHeaders) { a.got = h }

// csStub 假会话服务。
type csStub struct {
	sdk.CwdSessions
	cur, sid string
}

func (c *csStub) Current() string        { return c.cur }
func (c *csStub) CurrentSession() string { return c.sid }

func TestHeadersForExpandsPlaceholders(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("GAH_VERSION", "0.5.5")
	svc := &Service{cs: &csStub{cur: "-Users-x-proj", sid: "sess-7"}}
	p := providerfile.Provider{Name: "go", Headers: map[string]string{
		"x-opencode-session": "${session}",
		"x-ver":              "v${version}",
		"x-dir":              "${cwd}/x",
		"x-raw":              "${unknown}",
	}}
	dir, _ := os.Getwd()
	got := svc.headersFor(p)
	if got["x-opencode-session"] != "sess-7" {
		t.Fatalf("会话头应取 CurrentSession: %q", got["x-opencode-session"])
	}
	if got["x-ver"] != "v0.5.5" {
		t.Fatalf("版本占位符: %q", got["x-ver"])
	}
	if got["x-dir"] != dir+"/x" {
		t.Fatalf("cwd 占位符: %q(期望 %q)", got["x-dir"], dir+"/x")
	}
	if got["x-raw"] != "${unknown}" {
		t.Fatalf("未知占位符应原样保留: %q", got["x-raw"])
	}
}

// TestSessionFallsBackToProjectKey 主会话没有 session id(空)⇒ 回落项目 key。
//
// 官方要的语义是「同一段对话稳定、不同对话不同」;回落项目 key 恰好满足后半句 ——
// 而落空会让所有主会话共用一个空值,那等于没有会话隔离。
func TestSessionFallsBackToProjectKey(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	svc := &Service{cs: &csStub{cur: "-Users-x-proj", sid: ""}}
	got := svc.headersFor(providerfile.Provider{Headers: map[string]string{"x": "${session}"}})
	if got["x"] != "-Users-x-proj" {
		t.Fatalf("应回落项目 key: %q", got["x"])
	}
}

// TestHeadersForNilWhenNoHeaders 没配头就别下发空字典(适配器侧会因此清空,行为已对齐)。
func TestHeadersForNilWhenNoHeaders(t *testing.T) {
	svc := &Service{}
	if got := svc.headersFor(providerfile.Provider{Name: "x"}); got != nil {
		t.Fatalf("无头时应返回 nil: %+v", got)
	}
}

// TestSwitchActiveDeliversHeaders 切 provider 时头要真的下到适配器。
func TestSwitchActiveDeliversHeaders(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("GAH_VERSION", "9.9.9")
	ad := &headerStub{providerAdapter: providerAdapter{name: "openai"}}
	svc := &Service{adapters: map[string]sdk.LLMAdapter{"openai": ad}, order: []string{"openai"},
		cs: &csStub{cur: "proj", sid: "s9"}}
	if err := svc.switchActive(providerfile.Provider{
		Name:    "go",
		BaseURL: "https://opencode.ai/zen/go/v1",
		Model:   "minimax-m3",
		Headers: map[string]string{"x-opencode-session": "${session}", "x-v": "${version}"},
	}); err != nil {
		t.Fatal(err)
	}
	if ad.got["x-opencode-session"] != "s9" || ad.got["x-v"] != "9.9.9" {
		t.Fatalf("头没下发到适配器: %+v", ad.got)
	}
	if ad.configuredURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("端点没切: %q", ad.configuredURL)
	}
}

// TestSetProviderHeaders 三条路径都要过:活跃(立即下发)/ 非活跃(只落盘)/ 不存在(报错)。
//
// 「非活跃只落盘」这条尤其重要:列表里有五个 provider 时,改其中一个的头**不应该**
// 顺手把当前正在用的另一个也改了 —— 而「立即生效」又要求活跃的那个不用切走再切回来。
func TestSetProviderHeaders(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("GAH_VERSION", "0.5.5")
	if err := providerfile.Add(providerfile.Provider{Name: "a", BaseURL: "https://a.test/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := providerfile.Add(providerfile.Provider{Name: "b", BaseURL: "https://b.test/v1"}); err != nil {
		t.Fatal(err)
	}
	// AddProvider 把首个设为 active;这里明确指定 a 为活跃(Add 同名即更新+激活)
	if err := providerfile.SetActive("a"); err != nil {
		t.Fatal(err)
	}
	ad := &headerStub{providerAdapter: providerAdapter{name: "openai"}}
	svc := &Service{adapters: map[string]sdk.LLMAdapter{"openai": ad}, order: []string{"openai"},
		cs: &csStub{cur: "proj", sid: "s1"}}

	// 非活跃:只落盘,不下发
	if err := svc.SetProviderHeaders("b", map[string]string{"x-b": "${session}"}); err != nil {
		t.Fatal(err)
	}
	if len(ad.got) != 0 {
		t.Fatalf("非活跃 provider 的头不该下发给当前适配器: %+v", ad.got)
	}
	f, _ := providerfile.LoadFile()
	for _, p := range f.Providers {
		if p.Name == "b" && p.Headers["x-b"] != "${session}" {
			t.Fatalf("应落盘(占位符原样存,展开在下发时): %+v", p.Headers)
		}
	}

	// 活跃:立即下发(展开后)
	if err := svc.SetProviderHeaders("a", map[string]string{"x-a": "${session}"}); err != nil {
		t.Fatal(err)
	}
	if ad.got["x-a"] != "s1" {
		t.Fatalf("活跃 provider 的头应立即下发且已展开: %+v", ad.got)
	}

	// 不存在:显式报错
	if err := svc.SetProviderHeaders("不存在", map[string]string{"a": "1"}); err == nil {
		t.Fatal("给不存在的 provider 设头应报错")
	}
}

// TestProvidersEchoExpandedHeaders /api/providers 回显的应是**已展开**的头。
//
// 为什么这样才对:设置面板要显示「这个 provider 现在实际会发什么」,而不是一串
// ${session} 占位符 —— 用户看到占位符会以为没生效。
func TestProvidersEchoExpandedHeaders(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv("GAH_VERSION", "0.5.5")
	if err := providerfile.Add(providerfile.Provider{
		Name: "a", BaseURL: "https://a.test/v1",
		Headers: map[string]string{"x-session": "${session}", "x-ver": "${version}"},
	}); err != nil {
		t.Fatal(err)
	}
	svc := &Service{cs: &csStub{cur: "proj", sid: "s2"}}
	ps := svc.Providers()
	if len(ps) != 1 {
		t.Fatalf("应有一条: %+v", ps)
	}
	if ps[0].Headers["x-session"] != "s2" || ps[0].Headers["x-ver"] != "0.5.5" {
		t.Fatalf("回显应是展开后的值: %+v", ps[0].Headers)
	}
}
