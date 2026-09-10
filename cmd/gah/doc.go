// `gah doc` 文档阅读 CLI(D 组文档预览 D1;对齐 docs/DOC_PREVIEW_PLAN.md §6.3)。
//
//	gah doc <path> [--json|--md|--text] [--page N] [--sheet S] [--max-bytes B] [--tree] [--lines N] [--convert] [--raster]
//
// 退出码:0 成功 / 1 其它错误 / 2 用法 / 3 不支持格式 / 4 超预算 / 5 解析失败。
// 不启动插件装配(纯读命令):直接构造 host-docview 服务。
// 默认零写盘;**--convert**(D6-2 可选高保真档)启用时,旧二进制 Office 会经本机
// LibreOffice 转 PDF,产物落 $GAH_HOME/cache/doc/(便携纪律;超 7 天按龄清理)。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	hostdocview "github.com/nekoleamo/go-agent-harness/plugins/host/host-docview"
	"github.com/nekoleamo/go-agent-harness/sdk"
)

// docExitCode 哨兵错误 → 退出码(§6.3 冻结)。
func docExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, sdk.ErrDocUnsupported):
		return 3
	case errors.Is(err, sdk.ErrDocTooLarge):
		return 4
	case errors.Is(err, sdk.ErrDocParse):
		return 5
	default:
		return 1
	}
}

// runDocCmd 执行 `gah doc`(返回进程退出码)。
func runDocCmd(args []string) int {
	fs := flag.NewFlagSet("gah doc", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "输出 DocView JSON(完整块模型)")
	asMD := fs.Bool("md", false, "输出 Markdown(不带行号)")
	asText := fs.Bool("text", false, "输出带行号的文本(默认)")
	tree := fs.Bool("tree", false, "列出目录树(工作台入口)")
	page := fs.Int("page", 0, "起始页(PDF)/ 幻灯片序号")
	sheet := fs.Int("sheet", 0, "工作表序号(xlsx)")
	maxBytes := fs.Int64("max-bytes", 0, "预览字节预算(0 = 默认)")
	maxInputBytes := fs.Int64("max-input-bytes", 0, "源文件大小上限(0 = 服务默认)")
	lines := fs.Int("lines", 0, "输出行数上限(0 = 默认)")
	depth := fs.Int("depth", 2, "--tree 时递归深度(1–4)")
	convert := fs.Bool("convert", false, "旧二进制 Office(.doc/.xls/.ppt)经本机 LibreOffice 转 PDF 后抽取(需 soffice)")
	raster := fs.Bool("raster", false, "PDF 页光栅化为 PNG(RST-1;优先本机 poppler pdftoppm)")
	selfRaster := fs.Bool("raster-self", false, "PDF 页光栅化:未装 poppler 时用内置 pdfium.wasm 自包含兜底(SELF-1;可用 GAH_PDFIUM_WASM 指定本地 wasm)")
	dpi := fs.Int("dpi", 0, "--raster 渲染 DPI(默认 96;区间 36–300)")
	outPath := fs.String("o", "", "--raster 输出文件(默认写 stdout)")
	// flag 包在首个位置参数处停止解析,而本命令习惯写 `gah doc <path> --json` →
	// 预分离「标志(+其值)」与「位置参数」后统一交给 Parse(两类顺序均可)。
	var flags, pos []string
	takesValue := map[string]bool{"page": true, "sheet": true, "max-bytes": true, "max-input-bytes": true, "lines": true, "depth": true, "dpi": true, "o": true}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			name := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
			if !strings.Contains(a, "=") && takesValue[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		pos = append(pos, a)
	}
	if err := fs.Parse(append(flags, pos...)); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "用法: gah doc <path> [--json|--md|--text] [--page N] [--sheet S] [--max-bytes B] [--tree]")
		return 2
	}
	path := rest[0]

	svc := hostdocview.New(hostdocview.Options{
		Home: os.Getenv("GAH_HOME"), ExternalConverters: *convert, ExternalRaster: *raster,
		SelfContainedRaster: *selfRaster || *raster, // 给 --raster 带上兜底:装 poppler 时优先外部路径
		PDFiumWASMPath:      os.Getenv("GAH_PDFIUM_WASM"),
		PDFiumWASMURL:       os.Getenv("GAH_PDFIUM_WASM_URL"),
	})
	ctx := context.Background()

	if *tree {
		t, err := svc.List(ctx, sdk.DocRequest{Path: path}, *depth)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gah doc:", err)
			return docExitCode(err)
		}
		for _, e := range t.Entries {
			mark := " "
			if e.Dir {
				mark = "/"
			}
			fmt.Printf("%s%s%s\n", e.Path, mark, fmtSizeCol(e.Size, e.Dir))
		}
		for _, warn := range t.Warnings {
			fmt.Fprintln(os.Stderr, "提示:", warn)
		}
		return 0
	}

	req := sdk.DocRequest{Path: path, Page: *page, Sheet: *sheet, MaxBytes: *maxBytes, MaxInputBytes: *maxInputBytes, Limit: *lines}
	if *raster || *selfRaster {
		rs, ok := any(svc).(sdk.DocRasterService)
		if !ok {
			fmt.Fprintln(os.Stderr, "gah doc: 光栅能力不可用")
			return 3
		}
		out, err := rs.Raster(ctx, sdk.DocRequest{Path: path}, *page, *dpi)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gah doc:", err)
			return docExitCode(err)
		}
		if *outPath == "" {
			if _, err := os.Stdout.Write(out.Data); err != nil {
				fmt.Fprintln(os.Stderr, "gah doc: 写 stdout 失败:", err)
				return 1
			}
			return 0
		}
		if err := os.WriteFile(*outPath, out.Data, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "gah doc: 写文件失败:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "已写出 %s(第 %d 页,%d dpi,%dx%d,%d 字节)\n",
			*outPath, out.Page, out.DPI, out.W, out.H, out.Bytes)
		return 0
	}
	if *asJSON {
		v, err := svc.Preview(ctx, req)
		if err != nil {
			fmt.Fprintln(os.Stderr, "gah doc:", err)
			return docExitCode(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			fmt.Fprintln(os.Stderr, "gah doc:", err)
			return 1
		}
		if v.Format == sdk.DocFormatUnsupported && !docConverted(v.Meta) {
			fmt.Fprintln(os.Stderr, "gah doc: 不支持预览该格式")
			return 3
		}
		return 0
	}

	tx, err := svc.Text(ctx, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "gah doc:", err)
		return docExitCode(err)
	}
	for _, l := range tx.Lines {
		if *asMD {
			fmt.Println(l.Text)
			continue
		}
		fmt.Printf("%5d | %s\n", l.Number, l.Text)
	}
	_ = asText
	if len(tx.Lines) < tx.TotalLines {
		fmt.Fprintf(os.Stderr, "… 共 %d 行,已显示 %d 行(用 --lines/--json 取更多)\n", tx.TotalLines, len(tx.Lines))
	}
	for _, warn := range tx.Warnings {
		fmt.Fprintln(os.Stderr, "提示:", warn)
	}
	if tx.Format == sdk.DocFormatUnsupported && !docConverted(tx.Meta) {
		fmt.Fprintln(os.Stderr, "gah doc: 不支持预览该格式(退出码 3)")
		return 3
	}
	return 0
}

// docConverted 内容是否来自外部转换器产物(D6-2:源格式仍 unsupported,但已有可读内容)。
func docConverted(meta map[string]string) bool {
	return meta != nil && meta["preview_via"] == "external-converter"
}

// fmtSizeCol 目录树右侧大小列(目录留空)。
func fmtSizeCol(n int64, isDir bool) string {
	if isDir || n <= 0 {
		return ""
	}
	if n < 1024 {
		return fmt.Sprintf("  (%d B)", n)
	}
	if n < 1024*1024 {
		return fmt.Sprintf("  (%.1f KB)", float64(n)/1024)
	}
	return fmt.Sprintf("  (%.1f MB)", float64(n)/1024/1024)
}

// isDocSubcommand `gah doc …` 判定(参数首词)。
func isDocSubcommand(args []string) bool {
	return len(args) > 1 && strings.TrimSpace(args[1]) == "doc"
}
