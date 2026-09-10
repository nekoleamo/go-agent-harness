// `gah doc` CLI 单测(D1):退出码映射 / 输出模式(对齐 DOC_PREVIEW_PLAN §6.3)。
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// wrapDocErr 包装哨兵错误(验证 errors.Is 链路)。
func wrapDocErr(sentinel error) error { return fmt.Errorf("docview: %w", sentinel) }

var _ = errors.Is

func TestDocExitCodeMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{sdk.ErrDocUnsupported, 3},
		{sdk.ErrDocTooLarge, 4},
		{sdk.ErrDocParse, 5},
		{sdk.ErrDocNotFound, 1},
		{sdk.ErrDocDenied, 1},
	}
	for _, c := range cases {
		if got := docExitCode(c.err); got != c.want {
			t.Fatalf("docExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
	// 包装错误(errors.Is 语义)
	if got := docExitCode(wrapDocErr(sdk.ErrDocTooLarge)); got != 4 {
		t.Fatalf("包装错误应映射 4,得 %d", got)
	}
}

func TestRunDocCmdUsage(t *testing.T) {
	if code := runDocCmd(nil); code != 2 {
		t.Fatalf("无参数应返回用法码 2,得 %d", code)
	}
	if code := runDocCmd([]string{"--bad-flag"}); code != 2 {
		t.Fatalf("未知标志应返回 2,得 %d", code)
	}
}

func TestRunDocCmdSuccessAndErrors(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "a.md")
	if err := os.WriteFile(md, []byte("# 标题\n\n段落\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runDocCmd([]string{md, "--md"}); code != 0 {
		t.Fatalf("markdown 读取应成功,得 %d", code)
	}
	if code := runDocCmd([]string{md, "--json"}); code != 0 {
		t.Fatalf("JSON 输出应成功,得 %d", code)
	}
	if code := runDocCmd([]string{dir, "--tree"}); code != 0 {
		t.Fatalf("目录树应成功,得 %d", code)
	}
	legacy := filepath.Join(dir, "old.doc")
	if err := os.WriteFile(legacy, []byte("D0CF11E0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runDocCmd([]string{legacy}); code != 3 {
		t.Fatalf("旧二进制格式应退出码 3,得 %d", code)
	}
	if code := runDocCmd([]string{filepath.Join(dir, "nope.md")}); code != 1 {
		t.Fatalf("文件不存在应退出码 1,得 %d", code)
	}
	if code := runDocCmd([]string{md, "--max-input-bytes", "1"}); code != 4 {
		t.Fatalf("超源大小预算应退出码 4,得 %d", code)
	}
	bad := filepath.Join(dir, "bad.ipynb")
	if err := os.WriteFile(bad, []byte("{oops"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runDocCmd([]string{bad}); code != 5 {
		t.Fatalf("解析失败应退出码 5,得 %d", code)
	}
}

// D6-2:--convert 标志可解析(未安装 soffice 时仍显式回退,不静默)。
func TestDocCmdConvertFlagParses(t *testing.T) {
	// 文件不存在:应走错误路径(退出码 1),而不是用法错误(2)→ 说明标志被接受
	if code := runDocCmd([]string{filepath.Join(t.TempDir(), "missing.doc"), "--convert"}); code != 1 {
		t.Fatalf("--convert 应被接受并走到文件错误路径,得退出码 %d", code)
	}
	// 真正的旧 Office 文件:无 soffice 时给出结构化提示(退出码 0,块为提示;不静默失败)
	dir := t.TempDir()
	p := filepath.Join(dir, "old.doc")
	if err := os.WriteFile(p, []byte{0xd0, 0xcf, 0x11, 0xe0}, 0o644); err != nil {
		t.Fatal(err)
	}
	// 本机通常无 soffice:回退为结构化提示 → 仍是「不支持」语义(退出码 3)。
	// 若本机装有 LibreOffice 且转换成功,则应退出码 0(内容来自转换产物)。
	code := runDocCmd([]string{p, "--convert"})
	if code != 3 && code != 0 {
		t.Fatalf("--convert 旧 Office 应退出 3(不可用)或 0(转换成功),得 %d", code)
	}
	// 未加 --convert:始终 3(默认不启用外部转换器)
	if code := runDocCmd([]string{p}); code != 3 {
		t.Fatalf("默认应退出 3,得 %d", code)
	}
}

// RST-1:--raster 光栅化(需本机 poppler;缺失则跳过 —— CI 无 poppler 时不失败)。
func TestDocCmdRaster(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("本机无 pdftoppm(poppler),跳过真实光栅")
	}
	dir := t.TempDir()
	// 用 CUPS/系统 PDF:优先本机生成的语料,缺失则跳过(不引入二进制夹具入库)
	src := os.Getenv("GAH_DOC_CORPUS")
	var pdf string
	if src != "" {
		ents, _ := os.ReadDir(src)
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".pdf") {
				pdf = filepath.Join(src, e.Name())
				break
			}
		}
	}
	if pdf == "" {
		t.Skip("未提供 GAH_DOC_CORPUS 中的 PDF 语料,跳过(见 scripts/gen-doc-corpus.sh)")
	}
	out := filepath.Join(dir, "page.png")
	if code := runDocCmd([]string{pdf, "--raster", "--page", "1", "--dpi", "72", "-o", out}); code != 0 {
		t.Fatalf("--raster 应成功,退出码 %d", code)
	}
	fi, err := os.Stat(out)
	if err != nil || fi.Size() == 0 {
		t.Fatalf("应产出 PNG: %v", err)
	}
	head := make([]byte, 8)
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Read(head); err != nil {
		t.Fatal(err)
	}
	if string(head[1:4]) != "PNG" {
		t.Fatalf("产物应为 PNG: %q", head)
	}
	// 未启用光栅(不带 --raster)时,同一 PDF 走文本路径不产 PNG
	if code := runDocCmd([]string{pdf, "--json"}); code != 0 {
		t.Fatalf("PDF 文本路径应成功,退出码 %d", code)
	}
}
