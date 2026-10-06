// /cache 单测(缓存命中率的只读诊断)。
//
// 为什么要测:这条命令的价值全在**输出能不能指导下一步**——「哪条消息在动前缀」是它的
// 全部意义;若某条分支下它什么也说不出来,用户会拿一段没有结论的输出当结论。
package hostintcmd

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

type stubProbe struct {
	samples []sdk.PrefixSample
	turns   int
}

func (s *stubProbe) PrefixSamples(limit int) []sdk.PrefixSample {
	if limit <= 0 || limit > len(s.samples) {
		return s.samples
	}
	return s.samples[len(s.samples)-limit:]
}

func (s *stubProbe) PrefixTurns() int { return s.turns }

// 探针未装配:必须**明说**,不能留一段看起来正常的空输出。
func TestCmdCacheNoProbe(t *testing.T) {
	c, cmds := buildEnv(t)
	startCmds(t, c)
	out, err := run(t, cmds, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "未装配") {
		t.Errorf("探针缺失时应明说:\n%s", out)
	}
}

// 有 usage 但还没发过请求:命中率行不能编数字。
func TestCmdCacheNoRequests(t *testing.T) {
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.usageStats", sdk.UsageStatsService(&stubUsage{st: sdk.UsageStats{}})); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.prefixProbe", sdk.PrefixProbe(&stubProbe{})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "cache")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "尚无请求") {
		t.Errorf("没有请求时应如实说:\n%s", out)
	}
}

// 主路径:累计命中率 + 指纹序列 + 变化点 + 判读指引,四样都要在。
func TestCmdCacheFull(t *testing.T) {
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.usageStats", sdk.UsageStatsService(&stubUsage{st: sdk.UsageStats{
		PromptTokens: 10000, CompletionTokens: 200, CachedTokens: 2500, Requests: 4, Window: 65536,
	}})); err != nil {
		t.Fatal(err)
	}
	if err := c.Provide("ctx.prefixProbe", sdk.PrefixProbe(&stubProbe{
		turns: 9,
		samples: []sdk.PrefixSample{
			{Seq: 7, At: "12:00:00", Hash: "aaaa1111", SysChars: 3200, MsgCount: 8, DiffToPrev: "same"},
			{Seq: 8, At: "12:00:31", Hash: "bbbb2222", SysChars: 3200, MsgCount: 9,
				DiffToPrev: "changed", DiffWhere: "第 3 条(user): 帮我看下这个"},
			{Seq: 9, At: "12:01:02", Hash: "cccc3333", SysChars: 3200, MsgCount: 10, DiffToPrev: "same"},
		},
	})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "cache")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"缓存命中 2500(25%)", "前缀指纹", "aaaa1111", "bbbb2222", "第 3 条(user)", "判读"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺少 %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "共发出 9 次请求") {
		t.Errorf("应报总请求数(与样本条数不同):\n%s", out)
	}
}

// limit 参数:取最近 n 条;非法输入退回默认而不是报错或取 0 条。
func TestCmdCacheLimit(t *testing.T) {
	c, cmds := buildEnv(t)
	if err := c.Provide("ctx.prefixProbe", sdk.PrefixProbe(&stubProbe{
		turns: 3,
		samples: []sdk.PrefixSample{
			{Seq: 1, At: "10:00:00", Hash: "h1111111", MsgCount: 2, DiffToPrev: "first"},
			{Seq: 2, At: "10:00:10", Hash: "h2222222", MsgCount: 3, DiffToPrev: "same"},
			{Seq: 3, At: "10:00:20", Hash: "h3333333", MsgCount: 4, DiffToPrev: "same"},
		},
	})); err != nil {
		t.Fatal(err)
	}
	startCmds(t, c)
	out, err := run(t, cmds, "cache", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "h3333333") || strings.Contains(out, "h1111111") {
		t.Errorf("limit=1 只应显示最近一条:\n%s", out)
	}
	// 非法参数:安静退回默认(8),仍要有输出
	out2, err := run(t, cmds, "cache", "abc")
	if err != nil {
		t.Fatalf("非法参数不应报错:%v", err)
	}
	if !strings.Contains(out2, "前缀指纹") {
		t.Errorf("非法参数后仍应给出诊断:\n%s", out2)
	}
	// 超大值:按上限截断,不能整段没输出
	out3, err := run(t, cmds, "cache", "9999")
	if err != nil {
		t.Fatalf("超大参数不应报错:%v", err)
	}
	if !strings.Contains(out3, "h1111111") {
		t.Errorf("超大 limit 应显示全部可用样本:\n%s", out3)
	}
}
