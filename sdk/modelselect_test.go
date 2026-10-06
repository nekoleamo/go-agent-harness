package sdk

import (
	"strings"
	"testing"
)

// TestModelSelectRankAutoRouterOnTop 自动路由模型必须**置顶**,且压过一切免费模型。
//
// 钉它的理由:免费清单实测一天内换了一半,写死某个免费模型名做默认必然过期;
// `openrouter/free` 是官方维护的自动路由 id,不会失效 —— 所以它的价值就在于「不挑」,
// 排序上就该浮到最前。AutoRouter 的判定(不误标)由上一条用例兜。
func TestModelSelectRankAutoRouterOnTop(t *testing.T) {
	router := ModelSelectRank(ModelInfo{
		ID: FreeRouterModelID, SupportsTools: boolPtr(true),
		ContextWindow: 200 * 1024, PriceKnown: true,
	})
	biggerFree := ModelSelectRank(ModelInfo{
		ID: "some/free:free", SupportsTools: boolPtr(true),
		ContextWindow: 1024 * 1024, PriceKnown: true,
	})
	if !(router < biggerFree) {
		t.Fatalf("自动路由应压过上下文更大的免费模型: %d vs %d", router, biggerFree)
	}
	// 且明显小于付费普通模型
	paid := ModelSelectRank(ModelInfo{ID: "paid", SupportsTools: boolPtr(true), ContextWindow: 256 * 1024, PromptPrice: 1, PriceKnown: true})
	if !(router < paid) {
		t.Fatalf("自动路由应压过普通付费模型: %d vs %d", router, paid)
	}
}

// TestSortModelSelectEntriesStableTieBreak 同权重必须按 Value 字典序稳定排列。
//
// 为什么钉:跨 provider 合并后若 tie-break 不确定,用户每次打开列表顺序都可能不同,
// 观感上就是「列表在动」。这是**稳定性契约**,不是实现细节。
func TestSortModelSelectEntriesStableTieBreak(t *testing.T) {
	mk := func(id string) ModelSelectEntry {
		return NewModelSelectEntry(id, ModelInfo{ID: id}, "src")
	}
	got := SortModelSelectEntries([]ModelSelectEntry{mk("b"), mk("a"), mk("c")})
	if len(got) != 3 {
		t.Fatalf("排序不应丢条目: %d", len(got))
	}
	if got[0].Value != "a" || got[1].Value != "b" || got[2].Value != "c" {
		t.Fatalf("同权重应按 Value 升序: %v %v %v", got[0].Value, got[1].Value, got[2].Value)
	}
	// 输入切片不得被就地改(调用方可能还要用原顺序做别的事)
	in := []ModelSelectEntry{mk("b"), mk("a")}
	_ = SortModelSelectEntries(in)
	if in[0].Value != "b" {
		t.Fatal("排序不得就地修改入参")
	}
	// 空/单条不炸
	if n := len(SortModelSelectEntries(nil)); n != 0 {
		t.Fatalf("空输入应返回空: %d", n)
	}
	if n := len(SortModelSelectEntries([]ModelSelectEntry{mk("only")})); n != 1 {
		t.Fatalf("单条应原样返回: %d", n)
	}
}

// TestModelOptionDescPieces 三处拼接(来源/归属/标签/警告)分别覆盖。
func TestModelOptionDescPieces(t *testing.T) {
	// ① 厂商与来源一致 ⇒ 去重(厂商已写在 id 里)
	d := ModelOptionDesc(ModelInfo{ID: "vendor/x", OwnedBy: "vendor"}, "openrouter")
	if strings.Contains(d, "归属") {
		t.Errorf("厂商已含在 id 里不应重复标注: %q", d)
	}
	if !strings.Contains(d, "来源 openrouter") {
		t.Errorf("应带来源: %q", d)
	}

	// ② 厂商与来源不同 ⇒ 两个都标
	d = ModelOptionDesc(ModelInfo{ID: "Qwen/Q", OwnedBy: "alibaba"}, "siliconflow")
	if !strings.Contains(d, "来源 siliconflow/归属 alibaba") {
		t.Errorf("归属不同应同时标注: %q", d)
	}

	// ③ 标签与警告都在(可用 + 免费 + 有警告的场景:付费不触发警告,
	//    故这里用一个「未声明价格但免费判不出来」的模型走标签分支,警告另有一条用例)
	d = ModelOptionDesc(ModelInfo{ID: "n/x:free", SupportsTools: boolPtr(true), ContextWindow: 1024 * 1024, PriceKnown: true}, "openrouter")
	if !strings.Contains(d, "免费") || !strings.Contains(d, "1M 上下文") {
		t.Errorf("标签应进描述: %q", d)
	}
	if strings.Contains(d, "⚠") {
		t.Errorf("无警告时不该出现 ⚠: %q", d)
	}

	// ④ 警告进描述
	d = ModelOptionDesc(ModelInfo{ID: "n/no-tools", SupportsTools: boolPtr(false)}, "openrouter")
	if !strings.Contains(d, "⚠") || !strings.Contains(d, "工具调用") {
		t.Errorf("不可用项的描述必须带原因: %q", d)
	}

	// ⑤ 无标签无警告 ⇒ 只有「id (来源 x)」,不留空分隔符
	d = ModelOptionDesc(ModelInfo{ID: "plain"}, "src")
	if d != "plain (来源 src)" {
		t.Errorf("最小形态不该有多余符号: %q", d)
	}
}

// TestVendorHintAndUsableHelpers 两个便捷函数的口径。
func TestVendorHintAndUsableHelpers(t *testing.T) {
	if (ModelInfo{ID: "openai/gpt", OwnedBy: "o"}).VendorHint() != "o" {
		t.Error("有 owned_by 时用它")
	}
	if (ModelInfo{ID: "openai/gpt"}).VendorHint() != "openai" {
		t.Error("无 owned_by 时回退 id 前缀")
	}
	if (ModelInfo{ID: "no-slash"}).VendorHint() != "" {
		t.Error("无前缀无 owned_by ⇒ 空串(不猜)")
	}
	if !ModelIsAgentUsable(ModelInfo{ID: "legacy"}) {
		t.Error("未声明能力的模型应判可用(不误杀)")
	}
	if ModelIsAgentUsable(ModelInfo{ID: "x", SupportsTools: boolPtr(false)}) {
		t.Error("明确声明不支持的应判不可用")
	}
}
