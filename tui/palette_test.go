// 语义色 token 表守卫:默认调色板与既有渲染基线逐项一致(防误改导致输出漂移),
// 且每个 token 均有非空色值(新 token 漏配表项立即暴露)。
package tui

import "testing"

// baseline 默认样式基线(token → 256 色索引/hex;M15 pi 式默认样式 + P5 视觉升级同步)。
var baseline = map[Token]string{
	TokUser: "81", TokAssistant: "253", TokTool: "246", TokToolOK: "114", TokMeta: "245",
	TokThinking: "242", TokError: "203", TokPrompt: "207", TokPick: "207", TokCursor: "214",

	TokUserBg: "236", TokToolBg: "235", TokToolOKBg: "237", TokToolErrBg: "237",

	TokStatus: "252", TokBusy: "214", TokWidget: "243",

	TokBarThumb: "214", TokBarTrack: "240", TokBarHover: "172", TokBarEnd: "214",

	TokMdCode: "179", TokMdBold: "231", TokMdTitle: "51", TokMdList: "220", TokMdHr: "245",
	TokMdLink: "#83a598", TokMdQuote: "#d5c4a1",
	TokMdKey: "141", TokMdStr: "215", TokMdCmt: "244",

	TokSyntaxVar: "#ebdbb2", TokSyntaxNum: "#fe8019", TokSyntaxType: "#8ec07c",
	TokSyntaxOp: "#8ec07c", TokSyntaxPunct: "#bdae93",

	TokThinkOff: "#665c54", TokThinkLow: "#83a598", TokThinkMed: "#8ec07c", TokThinkHigh: "#d3869b",

	TokSearchBg: "238", TokSearchCurBg: "214",

	TokDiffAdd: "114", TokDiffDel: "167", TokDiffHdr: "245",
}

func TestPaletteBaseline(t *testing.T) {
	if len(DefaultPalette) != len(baseline) {
		t.Fatalf("调色板表项数漂移: %d vs 基线 %d", len(DefaultPalette), len(baseline))
	}
	for tok, want := range baseline {
		got, ok := DefaultPalette[tok]
		if !ok {
			t.Fatalf("缺少 token %q 色值", tok)
		}
		if got != want {
			t.Errorf("token %q = %s,want 基线 %s(改动将改变渲染输出)", tok, got, want)
		}
	}
	for tok := range DefaultPalette {
		if _, ok := baseline[tok]; !ok {
			t.Errorf("调色板多余 token %q(基线未收口,需同步)", tok)
		}
	}
}

func TestPaletteNoEmptyColor(t *testing.T) {
	for tok, c := range DefaultPalette {
		if c == "" {
			t.Errorf("token %q 色值为空(样式将渲染无色)", tok)
		}
	}
}

// TestApplyTheme M13:主题覆盖生效/部分覆盖/空值回落/非法输入显式报错/重置回默认。
func TestApplyTheme(t *testing.T) {
	defer ResetTheme()

	// 覆盖生效
	if err := ApplyTheme(map[string]string{"user": "196"}); err != nil {
		t.Fatalf("ApplyTheme: %v", err)
	}
	if got := colorVal(TokUser); got != "196" {
		t.Errorf("覆盖后 TokUser = %q,want 196", got)
	}
	// 未覆盖 token 保持默认
	if got := colorVal(TokAssistant); got != DefaultPalette[TokAssistant] {
		t.Errorf("未覆盖 TokAssistant 漂移: %q", got)
	}

	// 空值 = 回落默认
	if err := ApplyTheme(map[string]string{"user": ""}); err != nil {
		t.Fatalf("ApplyTheme(空值): %v", err)
	}
	if got := colorVal(TokUser); got != DefaultPalette[TokUser] {
		t.Errorf("空值回落失败: TokUser = %q", got)
	}

	// 非法输入显式报错(未知 token / 非数字非 hex 色值 / 坏 hex)
	if err := ApplyTheme(map[string]string{"nope": "1"}); err == nil {
		t.Error("未知 token 应显式报错")
	}
	if err := ApplyTheme(map[string]string{"user": "abc"}); err == nil {
		t.Error("非数字非 hex 色值应报错")
	}
	if err := ApplyTheme(map[string]string{"user": "#zzz"}); err == nil {
		t.Error("坏 hex 色值应报错")
	}
	// 合法 hex/数字
	if err := ApplyTheme(map[string]string{"user": "#ff0000", "assistant": "255"}); err != nil {
		t.Errorf("合法 hex/数字应通过: %v", err)
	}

	// 重置回默认
	ResetTheme()
	for tok, want := range baseline {
		if got := colorVal(tok); got != want {
			t.Errorf("ResetTheme 后 token %q = %q,want 基线 %q", tok, got, want)
		}
	}
}

// TestApplyThemeNonMutation 失败覆盖不得写入任何 token(原子性)。
func TestApplyThemeNonMutation(t *testing.T) {
	defer ResetTheme()
	before := ThemeSnapshot()
	if err := ApplyTheme(map[string]string{"user": "196", "nope": "1"}); err == nil {
		t.Fatal("应因未知 token 失败")
	}
	after := ThemeSnapshot()
	for tok, want := range before {
		if after[tok] != want {
			t.Errorf("失败覆盖污染了 token %q: %q → %q", tok, want, after[tok])
		}
	}
}
