// Jupyter notebook(.ipynb)抽取器(D1):只读——markdown 单元走 markdown 块模型,
// 代码单元走 code 块,文本输出走 code 块;二进制(图片等)输出只给说明,不解码。
package hostdocview

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/yuin/goldmark/text"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// nbDoc .ipynb 顶层结构(只取需要的字段)。
type nbDoc struct {
	Metadata struct {
		Kernelspec struct {
			Language string `json:"language"`
		} `json:"kernelspec"`
		LanguageInfo struct {
			Name string `json:"name"`
		} `json:"language_info"`
	} `json:"metadata"`
	Cells []nbCell `json:"cells"`
}

type nbCell struct {
	CellType string          `json:"cell_type"`
	Source   json.RawMessage `json:"source"`
	Outputs  []nbOutput      `json:"outputs"`
}

type nbOutput struct {
	OutputType string                     `json:"output_type"`
	Text       json.RawMessage            `json:"text"`
	Data       map[string]json.RawMessage `json:"data"`
	Ename      string                     `json:"ename"`
	Evalue     string                     `json:"evalue"`
	Traceback  []string                   `json:"traceback"`
}

// nbLines 兼容 nbformat 的 source 两种形态([]string 或 string)。
func nbLines(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		out := ""
		for _, l := range arr {
			out += l
		}
		return out
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return ""
}

// extractNotebook .ipynb → 块模型。
func extractNotebook(_ context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
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

	var nb nbDoc
	if err := json.Unmarshal(data, &nb); err != nil {
		return nil, fmt.Errorf("%w: ipynb JSON 解析失败: %v", sdk.ErrDocParse, err)
	}
	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	if truncated {
		v.Truncated = append(v.Truncated, fmt.Sprintf("bytes:%d/%d", max, fi.Size()))
		v.Warnings = append(v.Warnings, "notebook 源超出预览字节预算,末尾单元未解析")
	}
	lang := nb.Metadata.Kernelspec.Language
	if lang == "" {
		lang = nb.Metadata.LanguageInfo.Name
	}

	codeBlocks := 0
	for i, cell := range nb.Cells {
		switch cell.CellType {
		case "markdown":
			src := nbLines(cell.Source)
			m := &mdCtx{s: s, abs: abs, src: []byte(src), req: req, seen: map[string]bool{}}
			doc := mdParser.Parser().Parse(text.NewReader([]byte(src)))
			for c := doc.FirstChild(); c != nil; c = c.NextSibling() {
				m.appendBlock(c, 0)
			}
			v.Blocks = append(v.Blocks, m.blocks...)
			v.Warnings = append(v.Warnings, m.warns...)
		case "code":
			codeBlocks++
			v.Blocks = append(v.Blocks, sdk.DocBlock{
				Kind: sdk.DocBlockCode, Lang: lang, Page: i + 1,
				Text: normalizeNewlines(nbLines(cell.Source)),
				Meta: map[string]string{"cell": "code"},
			})
			for _, out := range cell.Outputs {
				v.Blocks = append(v.Blocks, notebookOutputBlocks(out)...)
			}
		case "raw":
			v.Blocks = append(v.Blocks, sdk.DocBlock{Kind: sdk.DocBlockCode, Lang: "text", Text: normalizedOrEmpty(nbLines(cell.Source))})
		default:
			v.Warnings = append(v.Warnings, fmt.Sprintf("notebook 第 %d 单元类型 %q 未支持,已跳过", i+1, cell.CellType))
		}
		if len(v.Blocks) >= s.budget.MaxBlocks {
			v.Blocks = v.Blocks[:s.budget.MaxBlocks]
			v.Truncated = append(v.Truncated, fmt.Sprintf("blocks:%d/%d", s.budget.MaxBlocks, len(v.Blocks)))
			v.Warnings = append(v.Warnings, fmt.Sprintf("notebook 块数超出预算 %d,已截断", s.budget.MaxBlocks))
			break
		}
	}
	if len(v.Blocks) == 0 {
		v.Blocks = []sdk.DocBlock{{Kind: sdk.DocBlockNote, Text: "空 notebook(无单元)"}}
	}
	v.Meta = map[string]string{"cells": fmt.Sprintf("%d", len(nb.Cells)), "code_cells": fmt.Sprintf("%d", codeBlocks)}
	return v, nil
}

// notebookOutputBlocks 单元输出 → 块(文本类;二进制只给说明)。
func notebookOutputBlocks(out nbOutput) []sdk.DocBlock {
	switch out.OutputType {
	case "stream", "execute_result", "display_data":
		if txt := nbLines(out.Text); txt != "" {
			return []sdk.DocBlock{{Kind: sdk.DocBlockCode, Lang: "text", Text: normalizeNewlines(txt), Meta: map[string]string{"output": out.OutputType}}}
		}
		if raw, ok := out.Data["text/plain"]; ok {
			if txt := nbLines(raw); txt != "" {
				return []sdk.DocBlock{{Kind: sdk.DocBlockCode, Lang: "text", Text: normalizeNewlines(txt), Meta: map[string]string{"output": out.OutputType}}}
			}
		}
		// 非文本输出(图片/HTML/JSON 等):只报类型,不解码
		keys := make([]string, 0, len(out.Data))
		for k := range out.Data {
			keys = append(keys, k)
		}
		if len(keys) > 0 {
			return []sdk.DocBlock{{Kind: sdk.DocBlockNote, Text: fmt.Sprintf("单元输出为二进制/富媒体(%v),本期不解码", keys)}}
		}
	case "error":
		return []sdk.DocBlock{{
			Kind: sdk.DocBlockCode, Lang: "text",
			Text: normalizeNewlines(out.Ename + ": " + out.Evalue + "\n" + joinLines(out.Traceback)),
			Meta: map[string]string{"output": "error"},
		}}
	}
	return nil
}

func normalizedOrEmpty(s string) string { return normalizeNewlines(s) }

func joinLines(ss []string) string {
	out := ""
	for _, s := range ss {
		out += s + "\n"
	}
	return out
}
