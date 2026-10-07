// /websearch 只读诊断命令的单测。
//
// 钉它的理由:这条命令的全部价值是「把排查搜索问题从翻文件+猜变成一条命令」。如果它
// 在关键情形下说不清(哪一家/端点/key/能不能用),用户还是得回去翻那份带凭据、路径带空格
// 的 search.yaml —— 那就等于没加。
package hostintcmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

type fakeSearch struct {
	info sdk.SearchInfo
	// probeErr 非空 ⇒ TryProbe 失败
	probeErr error
	gotQuery string
}

func (f *fakeSearch) SearchInfo() sdk.SearchInfo { return f.info }

func (f *fakeSearch) TryProbe(_ context.Context, q string, _ int) sdk.SearchInfo {
	f.gotQuery = q
	if f.probeErr != nil {
		out := f.info
		out.Err = f.probeErr.Error()
		return out
	}
	out := f.info
	out.Ok = true
	out.ResultCount = 3
	out.LatencyMS = 120
	out.ResultTitles = []string{"2025 年报全文"}
	return out
}

func TestCmdSearchReportsProviderEndpointKey(t *testing.T) {
	c, cmds := buildEnv(t)
	f := &fakeSearch{info: sdk.SearchInfo{
		Provider: "exa", EndpointHost: "api.exa.ai",
		HasKey: false, ConfiguredKeyMask: "",
		Note: "exa 按量付费:匿名调用会被拒(402/401)。缺省且国内直连的是 anysearch",
	}}
	if err := c.Provide("ctx.search", sdk.SearchService(f)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "websearch")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"exa", "api.exa.ai", "未配置(匿名调用)", "402"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出应含 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "通(") {
		t.Fatalf("没带词就不该出现实跑结果:\n%s", out)
	}
}

func TestCmdSearchMasksKey(t *testing.T) {
	c, cmds := buildEnv(t)
	f := &fakeSearch{info: sdk.SearchInfo{
		Provider: "anysearch", EndpointHost: "api.anysearch.com",
		HasKey: true, ConfiguredKeyMask: "sk-****abc",
	}}
	if err := c.Provide("ctx.search", sdk.SearchService(f)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "websearch")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "已配置(sk-****abc)") {
		t.Fatalf("应显示打码后的 key:\n%s", out)
	}
	if strings.Contains(out, "sk-or-v1") {
		t.Fatalf("不得回显完整 key:\n%s", out)
	}
}

// 实跑:成功与失败都要说清(用户要的是「到底能不能用」)。
func TestCmdSearchProbeSuccessAndFailure(t *testing.T) {
	c, cmds := buildEnv(t)
	f := &fakeSearch{info: sdk.SearchInfo{Provider: "anysearch", EndpointHost: "api.anysearch.com"}}
	if err := c.Provide("ctx.search", sdk.SearchService(f)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "websearch", "测试", "2025", "年报")
	if err != nil {
		t.Fatal(err)
	}
	if f.gotQuery != "2025 年报" {
		t.Fatalf("多词应拼成一个查询,得 %q", f.gotQuery)
	}
	if !strings.Contains(out, "通(3 条)") || !strings.Contains(out, "2025 年报全文") {
		t.Fatalf("成功时应报条数与样例标题:\n%s", out)
	}

	// 失败路径单独起一套环境(ctx 是单注册,同名服务不能重复 Provide)
	c2, cmds2 := buildEnv(t)
	f2 := &fakeSearch{info: f.info, probeErr: errors.New("搜索服务额度已用尽(402)。换 provider:GAH_SEARCH_PROVIDER 写 anysearch")}
	if err := c2.Provide("ctx.search", sdk.SearchService(f2)); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c2)
	out2, err := run(t, cmds2, "websearch", "测试", "财报")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "失败:") || !strings.Contains(out2, "402") {
		t.Fatalf("失败时应把原因原样带出来:\n%s", out2)
	}
}

// tool-web 未加载时:明说「未就绪」,不返回一堆空字段让人以为配置是空的。
func TestCmdSearchServiceMissing(t *testing.T) {
	c, cmds := buildEnv(t)
	startCmds(t, c)
	if _, err := run(t, cmds, "websearch"); err == nil {
		t.Fatal("搜索服务缺失时应显式报错")
	} else if !strings.Contains(err.Error(), "搜索能力未就绪") {
		t.Fatalf("错误文案应说明是 tool-web 未就绪,得 %q", err.Error())
	}
}
