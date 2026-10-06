// 模型选择器的排序与标签(2026-10-06 随「免费模型可见性」新增)。
//
// 钉它的理由:OpenRouter 一次给 400+ 个模型,列表顺序就是唯一的信息架构。
// 「免费/能当 agent 用/上下文大」的浮到上面,不可用的沉到底但**保留**(藏了用户会以为不存在)
// —— 这三条一旦被改回去,用户在 400 个模型里就又找不到自己要的那个。
package hostintcmd

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func boolPtr(b bool) *bool { return &b }

// 这两条测的是 sdk.ModelSelectRank / sdk.ModelOptionDesc —— 排序与描述已上移到 sdk,
// 因为模型选择器有 TUI 与宿主命令两份实现,各写一份必然漂移(改一边另一边不动)。
func TestModelRankOfPutsAgentUsableFreeAndLargeFirst(t *testing.T) {
	freeUsable := sdk.ModelInfo{ID: "free:free", SupportsTools: boolPtr(true), ContextWindow: 128 * 1024, PriceKnown: true}
	paidUsable := sdk.ModelInfo{ID: "paid", SupportsTools: boolPtr(true), ContextWindow: 128 * 1024, PromptPrice: 3, PriceKnown: true}
	notUsable := sdk.ModelInfo{ID: "classifier", SupportsTools: boolPtr(false), ContextWindow: 128 * 1024, PriceKnown: true}
	undeclared := sdk.ModelInfo{ID: "legacy"} // 端点没给能力字段:不该被排到最后
	bigCtx := sdk.ModelInfo{ID: "big", SupportsTools: boolPtr(true), ContextWindow: 1024 * 1024, PromptPrice: 3, PriceKnown: true}

	rFree := sdk.ModelSelectRank(freeUsable)
	rPaid := sdk.ModelSelectRank(paidUsable)
	rNo := sdk.ModelSelectRank(notUsable)
	rUndeclared := sdk.ModelSelectRank(undeclared)
	rBig := sdk.ModelSelectRank(bigCtx)

	if !(rFree < rPaid) {
		t.Errorf("免费应排在同能力的付费前面: %d vs %d", rFree, rPaid)
	}
	// 不可用沉底:「未声明能力」与「明确声明不支持」不是一回事 ——
	// 未声明(rank 0,Usable=true)必须排在明确不支持(rank 990,Usable=false)之前,
	// 否则只给 id 的常规 provider 会被判得比真正的残废模型还靠后。
	if !(rUndeclared < rNo) {
		t.Errorf("未声明能力不应被判死(应排在明确不支持之前): %d vs %d", rUndeclared, rNo)
	}
	if !(rPaid < rNo) {
		t.Errorf("不可用应沉到底: %d vs %d", rPaid, rNo)
	}
	// 大上下文优先于小上下文(同能力同价时)
	if !(rBig < rPaid) {
		t.Errorf("1M 上下文应排在 128K 前面: %d vs %d", rBig, rPaid)
	}
}

func TestModelOptionsOfSortsAndLabels(t *testing.T) {
	infos := []sdk.ModelInfo{
		{ID: "vendor/plain", OwnedBy: "vendor"},
		{ID: "vendor/big:free", SupportsTools: boolPtr(true), ContextWindow: 1024 * 1024, PriceKnown: true},
		{ID: "vendor/no-tools:free", SupportsTools: boolPtr(false), ContextWindow: 64 * 1024, PriceKnown: true},
	}
	entries := make([]sdk.ModelSelectEntry, 0, len(infos))
	for _, m := range infos {
		entries = append(entries, sdk.NewModelSelectEntry("openrouter|"+m.ID, m, "openrouter"))
	}
	opts := modelEntriesToOptions(entries)
	if len(opts) != 3 {
		t.Fatalf("应保留全部条目(不可用的只是沉底,不藏): %+v", opts)
	}
	if !strings.HasSuffix(opts[0].Value, "big:free") {
		t.Fatalf("免费+1M 的应排第一: %+v", opts[0])
	}
	if !strings.HasSuffix(opts[2].Value, "no-tools:free") {
		t.Fatalf("不支持工具调用的应沉到末尾: %+v", opts[2])
	}
	// 标签:免费/工具 + 上下文档
	if !strings.Contains(opts[0].Desc, "免费") || !strings.Contains(opts[0].Desc, "工具") {
		t.Errorf("应带免费/工具标签: %q", opts[0].Desc)
	}
	if !strings.Contains(opts[0].Desc, "1M 上下文") {
		t.Errorf("1M 上下文应单独成标签: %q", opts[0].Desc)
	}
	// 不可用项必须带原因(不能只靠排序暗示)
	last := opts[2].Desc
	if !strings.Contains(last, "⚠") || !strings.Contains(last, "工具调用") {
		t.Errorf("不可用项应带人话原因: %q", last)
	}
	// 归属与来源一致时不重复标注
	if !strings.Contains(opts[0].Desc, "来源 openrouter") {
		t.Errorf("应带来源: %q", opts[0].Desc)
	}
}

func TestModelDescOfVendorAndTags(t *testing.T) {
	// owned_by 缺失时厂商由 id 前缀补;若前缀已等于该段(id 里本就写着厂商),不重复标注
	m := sdk.ModelInfo{ID: "nvidia/nemotron-3-ultra:free", SupportsTools: boolPtr(true), PriceKnown: true}
	d := sdk.ModelOptionDesc(m, "openrouter")
	if !strings.Contains(d, "来源 openrouter") {
		t.Errorf("应带来源: %q", d)
	}
	if strings.Contains(d, "归属") {
		t.Errorf("厂商已写在 id 里时应去重,不重复标注: %q", d)
	}

	// owned_by 与 id 前缀不一致 ⇒ 两个来源都要说清(否则用户不知道模型到底是谁家的)
	m2 := sdk.ModelInfo{ID: "Qwen/Qwen2.5-72B", OwnedBy: "alibaba", SupportsTools: boolPtr(true)}
	d2 := sdk.ModelOptionDesc(m2, "siliconflow")
	if !strings.Contains(d2, "来源 siliconflow") || !strings.Contains(d2, "归属 alibaba") {
		t.Errorf("归属与来源不同应同时标注: %q", d2)
	}
}
