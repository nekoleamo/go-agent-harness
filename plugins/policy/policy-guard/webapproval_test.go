// A7:网页抓取的出口审批(域名级 TOFU)单测。
//
// 两层:① 纯判据(host 匹配/URL 解析/档位);② 走完整 pre-execute 流水线(装配 host-tools +
// policy-guard + 一个 web_fetch 替身),验证"批准→落白名单→下次不再问""无人值守不问也不放行"
// 这类行为语义 —— 这些是安全属性,不能只测纯函数。
package policyguard

import (
	"context"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/internal/prefs"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

type webFetchStub struct{}

func (webFetchStub) Definition() sdk.ToolDefinition {
	return sdk.ToolDefinition{Name: "web_fetch", Description: "stub", InputSchema: map[string]any{"type": "object"}}
}

func (webFetchStub) Execute(context.Context, string) (any, error) { return "fetched", nil }

// buildWebEnv 装配 host-tools + policy-guard + web_fetch 替身,并把偏好落到临时数据根。
func buildWebEnv(t *testing.T, confirm sdk.ConfirmService, approve string) sdk.Ctx {
	t.Helper()
	t.Setenv("GAH_HOME", t.TempDir())
	t.Setenv(webApproveEnv, approve)
	t.Setenv(webAllowEnv, "")
	c := buildTools(t, confirm, map[string]any{"sandbox": "full-access"})
	var tools sdk.ToolRegistry
	if err := c.Inject("ctx.tools", &tools); err != nil {
		t.Fatal(err)
	}
	tools.Register(webFetchStub{})
	return c
}

func TestWebHostMatch(t *testing.T) {
	cases := []struct {
		entry, host string
		want        bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "api.example.com", false}, // 精确匹配不含子域
		{"EXAMPLE.com", "example.com", true},      // 大小写不敏感
		{"*.example.com", "api.example.com", true},
		{"*.example.com", "a.b.example.com", true},
		{"*.example.com", "example.com", false},           // 通配只匹配子域
		{"*.example.com", "example.com.evil.test", false}, // 后缀必须带点对齐
		{"", "example.com", false},
	}
	for _, tc := range cases {
		if got := webHostMatch(tc.entry, tc.host); got != tc.want {
			t.Errorf("webHostMatch(%q,%q) = %v, want %v", tc.entry, tc.host, got, tc.want)
		}
	}
}

func TestWebURLHost(t *testing.T) {
	cases := map[string]string{
		"https://Example.COM/a?b=1":     "example.com",
		"http://api.example.com:8080/x": "api.example.com",
		"https://user:pw@example.com/":  "example.com",
		"https://例え.jp/x":               "例え.jp",
		"not a url":                     "",
		"https://":                      "",
	}
	for raw, want := range cases {
		if got := webURLHost(raw); got != want {
			t.Errorf("webURLHost(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestWebApproveModeOf 档位解析:空/非法值都落到 auto(auto 才是缺省口径;非法值不得被
// 当成 off —— 拼错一个词就静默放开所有出口,方向必须安全)。
func TestWebApproveModeOf(t *testing.T) {
	for raw, want := range map[string]string{"": "auto", "OFF": "off", "query": "query", "new-host": "new-host", "bogus": "auto"} {
		t.Setenv(webApproveEnv, raw)
		if got := webApproveModeOf(); got != want {
			t.Errorf("mode(%q) = %q, want %q", raw, got, want)
		}
	}
}

// TestResolveWebApproveModeAuto auto 跟随审批档:open/smart 直放行,strict 才逐域名确认。
// 显式设过 env 的档位不受审批档影响(env 优先,与沙箱/审批的联动口径一致)。
func TestResolveWebApproveModeAuto(t *testing.T) {
	t.Setenv(webApproveEnv, "")
	for mode, want := range map[sdk.ApprovalMode]string{
		sdk.ApprovalOpen:   "off",
		sdk.ApprovalSmart:  "off",
		sdk.ApprovalStrict: "new-host",
	} {
		if got := resolveWebApproveMode(mode); got != want {
			t.Errorf("resolveWebApproveMode(%s) = %q, want %q", mode, got, want)
		}
	}
	t.Setenv(webApproveEnv, "query")
	if got := resolveWebApproveMode(sdk.ApprovalOpen); got != "query" {
		t.Errorf("显式 env 优先于审批档, got %q", got)
	}
}

// TestSmartApprovalWebFetchNoPrompt smart 档下 web_fetch 不再弹确认(2026-10-03 用户要求:
// 「不需要每次请求都先确定再加入白名单」)。反提示注入与内网拦截不依赖这道门(见
// webapproval.go 文件头),故直放行不放宽那两条硬边界。
func TestSmartApprovalWebFetchNoPrompt(t *testing.T) {
	t.Setenv(webApproveEnv, "")
	t.Setenv(webAllowEnv, "")
	cf := &countingConfirm{resp: true}
	if err := checkWebToolFetch(context.Background(), cf, "web_fetch",
		`{"url":"https://never-approved.example/a"}`, sdk.ApprovalSmart); err != nil {
		t.Fatalf("smart 档应直接放行: %v", err)
	}
	if cf.count() != 0 {
		t.Fatalf("smart 档不应弹确认,got %d 次", cf.count())
	}
}

// TestCheckWebToolFetchPure 纯裁决路径(不装配插件):白名单命中/off/无确认通道/无人值守。
func TestCheckWebToolFetchPure(t *testing.T) {
	ctxb := context.Background()
	args := `{"url":"https://news.example.com/a"}`

	// off:不问也不拒
	t.Setenv(webApproveEnv, "off")
	t.Setenv(webAllowEnv, "")
	if err := checkWebToolFetch(ctxb, nil, "web_fetch", args, sdk.ApprovalStrict); err != nil {
		t.Fatalf("off 档应放行: %v", err)
	}
	// 非抓取工具:完全不参与(即使 mode 是 query、无确认通道)
	t.Setenv(webApproveEnv, "query")
	if err := checkWebToolFetch(ctxb, nil, "deploy_tool", args, sdk.ApprovalStrict); err != nil {
		t.Fatalf("非抓取工具不应被拦: %v", err)
	}
	// 静态白名单命中(含通配)
	t.Setenv(webAllowEnv, "a.com, *.example.com")
	if err := checkWebToolFetch(ctxb, nil, "web_fetch", args, sdk.ApprovalStrict); err != nil {
		t.Fatalf("静态白名单命中应放行: %v", err)
	}
	// 白名单未命中的 host 本身(example.com ≠ *.example.com)
	if err := checkWebToolFetch(ctxb, nil, "web_fetch", `{"url":"https://example.com/"}`, sdk.ApprovalStrict); err == nil {
		t.Fatal("*.example.com 不应放行 example.com 本身")
	}
	// 无确认通道 → 拒绝,且文案给出加白名单的办法
	t.Setenv(webAllowEnv, "")
	err := checkWebToolFetch(ctxb, nil, "web_fetch", args, sdk.ApprovalStrict)
	if err == nil {
		t.Fatal("无确认通道应拒绝")
	}
	for _, want := range []string{webAllowEnv, "web_allow_hosts", "news.example.com"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("拒绝文案应含 %q,得 %v", want, err)
		}
	}
	// 无人值守:即使有确认通道也拒绝,且**不弹窗**(没人在场)
	cf := &countingConfirm{resp: true}
	if err := checkWebToolFetch(sdk.WithUnattended(ctxb), cf, "web_fetch", args, sdk.ApprovalStrict); err == nil {
		t.Fatal("无人值守应拒绝")
	}
	if cf.count() != 0 {
		t.Fatalf("无人值守不应弹确认,实际弹了 %d 次", cf.count())
	}
	// URL 解析不出主机 → 安全默认拒
	if err := checkWebToolFetch(ctxb, cf, "web_fetch", `{"url":"///"}`, sdk.ApprovalStrict); err == nil {
		t.Fatal("URL 解析不出主机应拒绝")
	}
	// 缺 url 字段 → 不由本层抢答(交给工具自身报错)
	if err := checkWebToolFetch(ctxb, nil, "web_fetch", `{}`, sdk.ApprovalStrict); err != nil {
		t.Fatalf("缺 url 时本层不应报错: %v", err)
	}
}

// TestWebApprovalGateNewHost 走完整流水线:首次问 → 批准 → 落白名单 → 第二次不问;拒绝则 veto。
func TestWebApprovalGateNewHost(t *testing.T) {
	rec := &recordingConfirm{resp: true}
	c := buildWebEnv(t, rec, "new-host")
	args := `{"url":"https://docs.example.com/guide"}`

	if res := execTool(t, c, "web_fetch", args); res.Error != "" {
		t.Fatalf("批准后应放行: %+v", res)
	}
	if len(rec.prompts) != 1 || !strings.Contains(rec.prompts[0], "docs.example.com") {
		t.Fatalf("确认文案应含域名: %q", rec.prompts)
	}
	// 批准即落白名单(用户可复核的文件)
	if got := prefs.Load().WebAllowHosts; len(got) != 1 || got[0] != "docs.example.com" {
		t.Fatalf("批准后应记入 TOFU 白名单,得 %v", got)
	}
	// 第二次同域名:不再问(白名单命中)
	if res := execTool(t, c, "web_fetch", args); res.Error != "" {
		t.Fatalf("白名单命中应放行: %+v", res)
	}
	if len(rec.prompts) != 1 {
		t.Fatalf("白名单命中不应再问,实际 %d 次: %q", len(rec.prompts), rec.prompts)
	}
}

func TestWebApprovalGateDenyAndQuery(t *testing.T) {
	// 拒绝 → veto,且不落白名单
	rec := &recordingConfirm{resp: false}
	c := buildWebEnv(t, rec, "new-host")
	if res := execTool(t, c, "web_fetch", `{"url":"https://evil.test/x"}`); res.Error == "" {
		t.Fatal("用户拒绝应拦截")
	}
	if got := prefs.Load().WebAllowHosts; len(got) != 0 {
		t.Fatalf("拒绝不应落白名单,得 %v", got)
	}
	// query 档:每次都问,且**不**落白名单(高敏感场景的语义)
	rec2 := &recordingConfirm{resp: true}
	c2 := buildWebEnv(t, rec2, "query")
	for i := 0; i < 2; i++ {
		if res := execTool(t, c2, "web_fetch", `{"url":"https://a.test/x"}`); res.Error != "" {
			t.Fatalf("query 档批准后应放行: %+v", res)
		}
	}
	if len(rec2.prompts) != 2 {
		t.Fatalf("query 档每次都应问,实际 %d 次", len(rec2.prompts))
	}
	if got := prefs.Load().WebAllowHosts; len(got) != 0 {
		t.Fatalf("query 档不应落白名单,得 %v", got)
	}
	// off 档:不问不拒(但白名单文件不被改动)
	c3 := buildWebEnv(t, &recordingConfirm{resp: false}, "off")
	if res := execTool(t, c3, "web_fetch", `{"url":"https://free.test/x"}`); res.Error != "" {
		t.Fatalf("off 档应放行: %+v", res)
	}
}

// TestWebApprovalCanceledIsAborted 用户按停止导致确认取消 → 错误链带 sdk.ErrAborted,
// 且文案不再说「确认失败」(2026-10-03 实机反馈:停止后弹红色 blocked)。
func TestWebApprovalCanceledIsAborted(t *testing.T) {
	t.Setenv(webApproveEnv, "new-host")
	t.Setenv(webAllowEnv, "")
	ctx, cancel := context.WithCancel(context.Background())
	confirm := &signalConfirm{called: make(chan struct{})}
	errCh := make(chan error, 1)
	go func() {
		errCh <- checkWebToolFetch(ctx, confirm, "web_fetch", `{"url":"https://not-allowed.example/a"}`, sdk.ApprovalStrict)
	}()
	<-confirm.called // 弹层已呈现 = 用户此刻按停止
	cancel()
	err := <-errCh
	if err == nil {
		t.Fatal("取消应返回错误")
	}
	if !sdk.IsAborted(err) {
		t.Fatalf("应是中止而非策略拒绝: %v", err)
	}
	if strings.Contains(err.Error(), "网页抓取确认失败") {
		t.Fatalf("中止不该说成确认失败: %v", err)
	}
}

// TestWebApprovalDeniedStillNotAborted 用户真拒绝(非取消)仍是策略拒绝,文案不变。
func TestWebApprovalDeniedStillNotAborted(t *testing.T) {
	t.Setenv(webApproveEnv, "new-host")
	err := checkWebToolFetch(context.Background(), &staticConfirm{ok: false},
		"web_fetch", `{"url":"https://not-allowed.example/a"}`, sdk.ApprovalStrict)
	if err == nil || sdk.IsAborted(err) {
		t.Fatalf("拒绝应是策略拒绝: %v", err)
	}
	if !strings.Contains(err.Error(), "用户拒绝") {
		t.Fatalf("文案不符: %v", err)
	}
}

// signalConfirm 先点亮 called 再阻塞到 ctx 结束(= 弹层已呈现、用户还没答)。
type signalConfirm struct{ called chan struct{} }

func (s *signalConfirm) Confirm(ctx context.Context, _ string) (bool, error) {
	close(s.called)
	<-ctx.Done()
	return false, ctx.Err()
}

// staticConfirm 固定应答的确认桩。
type staticConfirm struct{ ok bool }

func (s *staticConfirm) Confirm(context.Context, string) (bool, error) { return s.ok, nil }
