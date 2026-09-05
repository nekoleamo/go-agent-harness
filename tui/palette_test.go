// 语义色 token 表守卫:默认调色板与既有渲染基线逐项一致(防误改导致输出漂移),
// 且每个 token 均有非空色值(新 token 漏配表项立即暴露)。
package tui

import "testing"

// baseline 既有硬编码色值基线(token → 原 256 色索引)。
var baseline = map[Token]string{
	TokUser: "81", TokAssistant: "120", TokTool: "220", TokMeta: "245",
	TokError: "203", TokPrompt: "207", TokPick: "207", TokCursor: "214",

	TokStatus: "250", TokBusy: "214",

	TokBarThumb: "214", TokBarTrack: "240", TokBarHover: "172", TokBarEnd: "214",

	TokMdCode: "179", TokMdBold: "231", TokMdTitle: "51", TokMdList: "220", TokMdHr: "245",
	TokMdKey: "141", TokMdStr: "215", TokMdCmt: "244",

	TokSearchBg: "238", TokSearchCurBg: "214",
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
