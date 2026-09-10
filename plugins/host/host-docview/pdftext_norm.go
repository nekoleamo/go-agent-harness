// PDF 文本码位修正:E-D 真实语料 harness 发现的第一处真实保真缺口。
//
// 现象(实测,CUPS 产出 PDF,语料 corpus-cups.pdf):gopdf 经字体 ToUnicode/CMap 取文本时,
// 统一表意文字「文」(U+6587)被判成**康熙部首**「⽂」(U+2F42)→ 展示形近而异码,
// 检索/复制/模型阅读都会错位。根因在 PDF 字体的 CMap 把该字形映射到 Kangxi 区。
//
// 修法:只对 **CJK 部首补充(U+2E80–U+2EFF)与康熙部首(U+2F00–U+2FDF)** 做单字符 NFKC
// (这两个区段的兼容分解就是统一表意文字);**不做全局 NFKC** —— 中文文档的全角标点
// (，；：（）U+FF01–U+FF5E)在 NFKC 下会被改成 ASCII,属不可接受的副作用。
package hostdocview

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// isCJKRadicalRune 是否落在 CJK 部首区(康熙部首 + 部首补充)。
func isCJKRadicalRune(r rune) bool {
	return (r >= 0x2E80 && r <= 0x2EFF) || (r >= 0x2F00 && r <= 0x2FDF)
}

// normalizeCJKRadicals 把部首码位归一为统一表意文字(其余字符原样保留)。
// 无部首字符时零分配直接返回(热路径:每页文本都会过这里)。
func normalizeCJKRadicals(s string) string {
	if s == "" || !strings.ContainsFunc(s, isCJKRadicalRune) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if !isCJKRadicalRune(r) {
			return r
		}
		if rs := []rune(norm.NFKC.String(string(r))); len(rs) == 1 && rs[0] != r {
			return rs[0]
		}
		return r // 无兼容分解(如 U+2E80–U+2EF3 中的部分变体):保留原码位不退化为空白
	}, s)
}
