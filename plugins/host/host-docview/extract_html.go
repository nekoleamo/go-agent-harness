// HTML 抽取器(D6-4 收口):源码视图 —— 块模型里的一个 html 代码块 + <title> 提标题。
//
// 纪律:
//   - 只读源码,**不做 DOM 解析、不进 HTML 通道**(渲染层零 v-html 红线的结构化解法);
//     HTML 的「沙箱呈现」归 Web 端(/api/doc/html 独立 CSP 路由 + sandbox="" iframe),
//     且默认不自动加载,由用户显式点击(见 web-src DocPanel);
//   - <title> 实体解码用 stdlib html.UnescapeString(纯文本解码,不渲染);
//   - 含 <script> 时显式告警(预览不执行脚本,与沙箱 iframe 语义一致,不静默)。
package hostdocview

import (
	"context"
	"fmt"
	stdhtml "html"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// htmlTitleMax 标题字符上限(rune)。
const htmlTitleMax = 200

// extractHTML HTML 源码视图。
func extractHTML(_ context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	max := req.MaxBytes
	if max <= 0 {
		max = s.budget.MaxPreviewBytes
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, truncated, err := readCap(f, max)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sdk.ErrDocParse, err)
	}
	if truncated {
		data = trimPartialRune(data)
	}
	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	text := normalizeNewlines(string(data))
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "\uFFFD")
		v.Warnings = append(v.Warnings, "内容含非 UTF-8 字节,已替换为 U+FFFD")
	}
	v.Meta = map[string]string{"mime": "text/html"}
	v.Title = htmlTitle(text)
	low := strings.ToLower(text)
	if strings.Contains(low, "<script") || strings.Contains(low, " onerror") || strings.Contains(low, " onload") {
		v.Warnings = append(v.Warnings, "含脚本或内联事件处理器,沙箱预览一律不执行(仅源码可见)")
	}
	v.Blocks = []sdk.DocBlock{{Kind: sdk.DocBlockCode, Text: text, Lang: "html"}}
	if truncated {
		v.Truncated = append(v.Truncated, fmt.Sprintf("bytes:%d/%d", max, fi.Size()))
	}
	return v, nil
}

// htmlTitle 取 <title> 文本(实体解码 + 空白折叠 + 截断);无标题返回空。
func htmlTitle(src string) string {
	low := strings.ToLower(src)
	i := strings.Index(low, "<title")
	if i < 0 {
		return ""
	}
	gt := strings.IndexByte(low[i:], '>')
	if gt < 0 {
		return ""
	}
	start := i + gt + 1
	end := strings.Index(low[start:], "</title")
	if end < 0 {
		return ""
	}
	t := stdhtml.UnescapeString(src[start : start+end])
	t = strings.Join(strings.Fields(t), " ")
	r := []rune(t)
	if len(r) > htmlTitleMax {
		return string(r[:htmlTitleMax]) + "…"
	}
	return string(r)
}
