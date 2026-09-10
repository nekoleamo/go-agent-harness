// CSV/TSV 抽取器(D1):定界符嗅探(只在 , ; \t | 中按行一致性打分,**不是格式嗅探**),
// 解析为 table 块;行/列/单元格三重封顶并写 Truncated(绝不静默截断)。
package hostdocview

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// csvCandidateDelims 允许的定界符候选。
var csvCandidateDelims = []rune{',', ';', '\t', '|'}

// sniffDelimiter 按"各行出现次数一致"打分选定界符(默认逗号)。
func sniffDelimiter(sample string) rune {
	lines := strings.Split(strings.TrimRight(sample, "\n"), "\n")
	if len(lines) > 5 {
		lines = lines[:5]
	}
	best, bestScore := ',', -1.0
	for _, d := range csvCandidateDelims {
		counts := make([]int, 0, len(lines))
		for _, l := range lines {
			if strings.TrimSpace(l) == "" {
				continue
			}
			counts = append(counts, strings.Count(l, string(d)))
		}
		if len(counts) == 0 || counts[0] == 0 {
			continue
		}
		// 一致性:全部相同的次数越多越好;0 次出现不给分
		same := 0
		for _, c := range counts {
			if c == counts[0] {
				same++
			}
		}
		score := float64(same) / float64(len(counts)) * float64(counts[0])
		if score > bestScore {
			best, bestScore = d, score
		}
	}
	return best
}

// extractTabular CSV/TSV → 单个 table 块。
func extractTabular(_ context.Context, s *Service, abs string, fi os.FileInfo, req sdk.DocRequest, format sdk.DocFormat) (*sdk.DocView, error) {
	max := req.MaxBytes
	if max <= 0 {
		max = s.budget.MaxPreviewBytes
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sample := make([]byte, 8192)
	n, _ := io.ReadFull(f, sample)
	delim := sniffDelimiter(string(sample[:n]))
	if _, err := f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("%w: %v", sdk.ErrDocParse, err)
	}

	v := baseView(req, abs, fi.Size(), fi.ModTime().UnixNano(), format)
	// 只按预算读入(表格化需要完整 CSV 语法,大文件由预算与行列封顶共同兜底)
	data, truncated, err := readCap(f, max)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", sdk.ErrDocParse, err)
	}
	if truncated {
		v.Truncated = append(v.Truncated, fmt.Sprintf("bytes:%d/%d", max, fi.Size()))
		v.Warnings = append(v.Warnings, "CSV 源超出预览字节预算,末尾行未解析")
	}

	r := csv.NewReader(strings.NewReader(string(data)))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	r.ReuseRecord = false

	blk := sdk.DocBlock{Kind: sdk.DocBlockTable}
	rowIdx, totalRows := 0, 0
	maxCols := 0
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			v.Warnings = append(v.Warnings, fmt.Sprintf("CSV 第 %d 行起解析失败: %v", totalRows+1, err))
			break
		}
		totalRows++
		if len(rec) > maxCols {
			maxCols = len(rec)
		}
		if rowIdx == 0 {
			for _, c := range rec {
				blk.Head = append(blk.Head, truncCell(c, s.budget.MaxCellChars))
			}
			rowIdx++
			continue
		}
		if len(blk.Rows) >= s.budget.MaxTableRows {
			continue // 继续计数以便写 Truncated
		}
		row := make([]sdk.DocCell, 0, len(rec))
		for _, c := range rec {
			row = append(row, sdk.DocCell{Text: truncCell(c, s.budget.MaxCellChars), Numeric: isNumeric(c)})
		}
		blk.Rows = append(blk.Rows, row)
		rowIdx++
	}
	if totalRows == 0 {
		v.Blocks = []sdk.DocBlock{{Kind: sdk.DocBlockNote, Text: "空表格(无数据行)"}}
		return v, nil
	}
	if dataRows := totalRows - 1; dataRows > len(blk.Rows) {
		v.Truncated = append(v.Truncated, fmt.Sprintf("rows:%d/%d", len(blk.Rows), dataRows))
		v.Warnings = append(v.Warnings, fmt.Sprintf("表格数据行 %d 超出预算 %d,已截断", dataRows, s.budget.MaxTableRows))
	}
	if maxCols > s.budget.MaxTableCols {
		cols := s.budget.MaxTableCols
		if len(blk.Head) > cols {
			blk.Head = blk.Head[:cols]
		}
		for i := range blk.Rows {
			if len(blk.Rows[i]) > cols {
				blk.Rows[i] = blk.Rows[i][:cols]
			}
		}
		v.Truncated = append(v.Truncated, fmt.Sprintf("cols:%d/%d", cols, maxCols))
		v.Warnings = append(v.Warnings, fmt.Sprintf("表格列数 %d 超出预算 %d,已截断", maxCols, s.budget.MaxTableCols))
	}
	v.Meta = map[string]string{"delimiter": string(delim), "rows": fmt.Sprintf("%d", totalRows)}
	v.Blocks = []sdk.DocBlock{blk}
	return v, nil
}

func truncCell(s string, max int) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	t, cut := truncText(s, max)
	if cut {
		return t + "…"
	}
	return t
}

// isNumeric 判定纯数值单元格(右对齐提示用)。
func isNumeric(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	dot, digits := false, 0
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.' && !dot:
			dot = true
		case (r == '-' || r == '+') && i == 0:
		case r == ',' || r == '%' || r == '$':
		default:
			return false
		}
	}
	return digits > 0
}
