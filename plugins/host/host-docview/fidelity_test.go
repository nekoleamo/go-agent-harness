// 真实文档保真语料 harness(E-D;opt-in,默认不进 CI)。
//
// 动机(见 DESIGN §14.1「G 组剩余项评测分析」):D2–D4 的抽取器此前只对手工构造的 OOXML/PDF
// 夹具验证过;真实世界文档(第三方写出器、样式重、合并多、多页 PDF)从未回归过。本 harness
// 把「真实文档 → 抽取结果」变成可核对报告,并给出**独立参照实现**的覆盖率对照:
//
//	PDF:poppler `pdftotext`(完全独立实现)与 gopdf 路径的中文字符覆盖率对照
//
// 用法:
//
//	bash scripts/gen-doc-corpus.sh                 # 用本机第三方工具(textutil/CUPS/系统 PDF)造语料
//	GAH_DOC_CORPUS=<目录> go test ./plugins/host/host-docview/ -run TestFidelityCorpus -v
//
// 判定:
//   - 硬性(必过,任何语料都不得违反):不 panic、返回结构化结果、不静默空视图、
//     不支持格式必须显式标记、块/文本预算字段自洽;
//   - 报告性(默认只打印):每文件的格式/块类型直方图/警告/截断 + PDF 覆盖率;
//     设 GAH_DOC_CORPUS_STRICT=1 时覆盖率低于 GAH_DOC_CORPUS_MIN_COVER(默认 0.85)判失败。
package hostdocview

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// corpusMaxFiles 单次语料文件上限(确定性;按路径排序后取前 N)。
const corpusMaxFiles = 40

func TestFidelityCorpus(t *testing.T) {
	dir := os.Getenv("GAH_DOC_CORPUS")
	if strings.TrimSpace(dir) == "" {
		t.Skip("未设 GAH_DOC_CORPUS,跳过真实语料保真(见 scripts/gen-doc-corpus.sh)")
	}
	files, err := corpusFiles(dir)
	if err != nil {
		t.Fatalf("语料目录不可读: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("语料目录无可用文档: %s", dir)
	}
	strict := os.Getenv("GAH_DOC_CORPUS_STRICT") == "1"
	// gapStrict(DOC-1):出现 content 级缺口即判失败(默认只报告;D6-3 判定由人拍板)
	gapStrict := os.Getenv("GAH_DOC_CORPUS_GAP_STRICT") == "1"
	minCover := 0.85
	t.Logf("语料文件 %d 个(strict=%v,gapStrict=%v,minCover=%.2f): %s", len(files), strict, gapStrict, minCover, dir)

	// 语料在 workspace 之外:按 CLI/TUI 单机语义构造无沙箱服务(strict 策略不适用)
	s := New(Options{Home: t.TempDir(), Logger: slog.New(slog.DiscardHandler)})
	// 跨文件特性聚合(GAPSUMMARY:一屏看清"真实语料到底用了哪些高级特性")
	agg := map[string][]string{}
	byFormat := map[string]int{}
	for _, f := range files {
		rel := filepath.Base(f)
		if _, ok := FormatByName(f); !ok {
			t.Fatalf("%s: 语料文件应可识别格式", rel)
		}
		view, err := s.Preview(context.Background(), sdk.DocRequest{Path: f})
		if err != nil {
			t.Fatalf("%s: 真实文档不应报错(got %v)", rel, err)
		}
		if view == nil {
			t.Fatalf("%s: 不应返回空视图", rel)
		}
		// —— 硬性不变量 ——
		if len(view.Blocks) == 0 && len(view.Warnings) == 0 {
			t.Fatalf("%s: 既无块也无警告(疑似静默失败)", rel)
		}
		if view.Format == sdk.DocFormatUnsupported && len(view.Warnings) == 0 {
			t.Fatalf("%s: 不支持格式必须显式警告", rel)
		}
		// 文本族/Office 文档必须产出可读块(空视图 = 静默失败)
		switch view.Format {
		case sdk.DocFormatDOCX, sdk.DocFormatXLSX, sdk.DocFormatPPTX, sdk.DocFormatMarkdown, sdk.DocFormatText:
			if len(view.Blocks) == 0 {
				t.Fatalf("%s: %s 抽取结果为空(真实文档不应产出空视图)", rel, view.Format)
			}
		}
		if view.ModTime.IsZero() || view.Size < 0 {
			t.Fatalf("%s: 元信息异常 size=%d mod=%v", rel, view.Size, view.ModTime)
		}
		byFormat[string(view.Format)]++

		kinds := map[string]int{}
		chars := 0
		for _, b := range view.Blocks {
			kinds[string(b.Kind)]++
			for _, r := range b.Text {
				if !unicode.IsSpace(r) {
					chars++
				}
			}
			for _, run := range b.Runs {
				for _, r := range run.Text {
					if !unicode.IsSpace(r) {
						chars++
					}
				}
			}
			for _, h := range b.Head {
				chars += len([]rune(strings.TrimSpace(h)))
			}
			for _, row := range b.Rows {
				for _, cell := range row {
					chars += len([]rune(strings.TrimSpace(cell.Text)))
				}
			}
		}
		t.Logf("REPORT file=%s format=%s kind=%s blocks=%d kinds=%s chars=%d pages=%d warnings=%d truncated=%v",
			rel, view.Format, view.Kind, len(view.Blocks), kindsSummary(kinds), chars, view.Pages, len(view.Warnings), view.Truncated)
		for _, w := range view.Warnings {
			t.Logf("REPORT warn file=%s %s", rel, w)
		}

		// 文本投影自洽(CLI/工具共用路径不得报错且行数一致)
		tx, err := s.Text(context.Background(), sdk.DocRequest{Path: f})
		if err != nil {
			t.Fatalf("%s: Text() 不应报错: %v", rel, err)
		}
		if tx.TotalLines != len(tx.Lines) && tx.TruncatedByBytes {
			t.Logf("REPORT note file=%s 行截断 total=%d shown=%d", rel, tx.TotalLines, len(tx.Lines))
		}

		// DOC-1:源高级特性 GAP 报告(D6-3 判定依据;不改抽取器)
		if gaps, gerr := probeGaps(view.Format, f); gerr != nil {
			t.Logf("REPORT note file=%s GAP 探针失败: %v", rel, gerr)
		} else if len(gaps) > 0 {
			contentGaps := 0
			for _, g := range gaps {
				if g.Severity == gapContent {
					contentGaps++
				}
				t.Logf("GAP file=%s feature=%s source_hits=%d parts=%s ours=%s severity=%s",
					rel, g.Feature, g.Hits, strings.Join(g.Parts, ";"), g.Ours, g.Severity)
				agg[g.Feature] = append(agg[g.Feature], g.Severity)
			}
			t.Logf("REPORT gap file=%s features=%d summary=%s content_gaps=%d",
				rel, len(gaps), gapSummary(gaps), contentGaps)
			if gapStrict && contentGaps > 0 {
				t.Errorf("%s: 存在 %d 项内容级缺口(见上方 GAP 行;D6-3 判定依据)", rel, contentGaps)
			}
		}

		// PDF:与 poppler 独立实现对照
		if view.Format == sdk.DocFormatPDF {
			ref, ok := pdftotextText(t, f)
			if !ok {
				t.Logf("REPORT skip file=%s 无 poppler pdftotext,跳过覆盖率对照", rel)
				continue
			}
			ours := flattenViewText(view)
			cover, refChars := charCoverage(ref, ours)
			ourChars := countNonSpace(ours)
			t.Logf("REPORT coverage file=%s poppler_chars=%d ours_chars=%d coverage=%.3f",
				rel, refChars, ourChars, cover)
			// 硬性不变量(与阈值无关,任何语料都不得违反):
			//   ① 参照实现抽到文本而我们几乎抽不到 → 真实保真崩塌;
			//   ② 双方都无文本 → Kind 不得声称 text(必须显式 scanned/image_only/empty)。
			if refChars >= 20 && ourChars == 0 {
				t.Errorf("%s: poppler 抽到 %d 字符而我们抽到 0(文本层丢失)", rel, refChars)
			}
			if refChars == 0 && ourChars == 0 && view.Kind == "text" {
				t.Errorf("%s: 无文本 PDF 不应判定 Kind=text(得 %q)", rel, view.Kind)
			}
			if strict && refChars >= 40 && cover < minCover {
				t.Errorf("%s: PDF 文本覆盖率 %.3f 低于阈值 %.2f(真实文档保真回退)", rel, cover, minCover)
			}
		}
	}
	// 跨文件 GAP 汇总(内容级优先列出;D6-3 判定据此拍板)
	for feat, sevs := range agg {
		worst := gapInfo
		for _, sev := range sevs {
			if sev == gapContent || (sev == gapStyle && worst == gapInfo) {
				worst = sev
			}
		}
		t.Logf("GAPSUMMARY feature=%s files=%d severity=%s", feat, len(sevs), worst)
	}
	t.Logf("SUMMARY formats=%s files=%d gap_features=%d", kindsSummary(byFormat), len(files), len(agg))
}

// corpusFiles 列举语料目录中的可识别文档(排序 + 上限;忽略隐藏文件与目录)。
func corpusFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单条不可读不阻断(报告在调用方)
		}
		name := d.Name()
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && p != dir {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		if _, ok := FormatByName(p); ok {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	if len(out) > corpusMaxFiles {
		out = out[:corpusMaxFiles]
	}
	return out, nil
}

// pdftotextText 调用 poppler pdftotext 取参照文本(未安装 = ok=false,不失败)。
func pdftotextText(t *testing.T, path string) (string, bool) {
	t.Helper()
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", false
	}
	cmd := exec.CommandContext(context.Background(), bin, "-q", "-enc", "UTF-8", path, "-")
	out, err := cmd.Output()
	if err != nil {
		t.Logf("REPORT note poppler pdftotext 执行失败(%v): %s", err, filepath.Base(path))
		return "", false
	}
	return string(out), true
}

// flattenViewText 视图全部可见文本(块 + 行内 runs + 表头/单元格)。
func flattenViewText(v *sdk.DocView) string {
	var sb strings.Builder
	for _, b := range v.Blocks {
		sb.WriteString(b.Text)
		for _, run := range b.Runs {
			sb.WriteString(run.Text)
		}
		for _, h := range b.Head {
			sb.WriteString(h)
		}
		for _, row := range b.Rows {
			for _, cell := range row {
				sb.WriteString(cell.Text)
			}
		}
	}
	return sb.String()
}

// charCoverage 字符多重集覆盖率:参照文本的非空白字符有多少能被 ours 覆盖(0..1)。
// 返回覆盖率与参照非空白字符数(过少视为无意义,调用方跳过阈值判定)。
func charCoverage(ref, ours string) (float64, int) {
	pool := map[rune]int{}
	for _, r := range ours {
		if !unicode.IsSpace(r) {
			pool[r]++
		}
	}
	total, hit := 0, 0
	for _, r := range ref {
		if unicode.IsSpace(r) {
			continue
		}
		total++
		if pool[r] > 0 {
			pool[r]--
			hit++
		}
	}
	if total == 0 {
		return 1, 0
	}
	return float64(hit) / float64(total), total
}

// countNonSpace 非空白字符数(报告用)。
func countNonSpace(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// kindsSummary "key=n" 稳定序摘要(报告可读 + 可 grep)。
func kindsSummary(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+itoa(m[k]))
	}
	return strings.Join(parts, ",")
}

// itoa 避免引入 strconv(报告专用)。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
