// `gah doc` CLI 补充单测:子命令判定、目录树大小列、行数截断提示、光栅错误映射。
// 与 doc_test.go 互补(那边覆盖退出码映射与主要输出模式,这里补边界与错误分支)。
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsDocSubcommand 锁定:`gah doc …` 判定只看首词且容忍空白(入口糖不吞其它子命令)。
func TestIsDocSubcommand(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"gah", "doc"}, true},
		{[]string{"gah", "doc", "a.md", "--json"}, true},
		{[]string{"gah", " doc "}, true}, // 前后空白容忍
		{[]string{"gah", "web"}, false},
		{[]string{"gah", "--profile", "doc"}, false}, // 标志位不当作子命令
		{[]string{"gah"}, false},
		{nil, false},
	}
	for _, c := range cases {
		if got := isDocSubcommand(c.args); got != c.want {
			t.Errorf("isDocSubcommand(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

// TestFmtSizeCol 锁定目录树右侧大小列:目录/未知大小留空,其余按 B/KB/MB 单列显示。
func TestFmtSizeCol(t *testing.T) {
	cases := []struct {
		n     int64
		isDir bool
		want  string
	}{
		{0, false, ""},
		{-5, false, ""},
		{4096, true, ""}, // 目录不显示大小
		{1, false, "  (1 B)"},
		{1023, false, "  (1023 B)"},
		{1024, false, "  (1.0 KB)"},
		{1536, false, "  (1.5 KB)"},
		{1024 * 1024, false, "  (1.0 MB)"},
		{3 * 1024 * 1024, false, "  (3.0 MB)"},
	}
	for _, c := range cases {
		if got := fmtSizeCol(c.n, c.isDir); got != c.want {
			t.Errorf("fmtSizeCol(%d, %v) = %q, want %q", c.n, c.isDir, got, c.want)
		}
	}
}

// TestRunDocCmdLineLimitAndFlagPassthrough 锁定:--lines 截断仍成功退出(并给出截断提示),
// 页码/工作表/预算等标志被接受并透传(不误判为用法错误)。
func TestRunDocCmdLineLimitAndFlagPassthrough(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "many.md")
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString("行内容\n")
	}
	if err := os.WriteFile(md, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := runDocCmd([]string{md, "--lines", "1"}); code != 0 {
			t.Errorf("--lines 截断应成功,得 %d", code)
		}
	})
	if n := len(strings.Split(strings.TrimRight(out, "\n"), "\n")); n != 1 {
		t.Fatalf("--lines 1 应只输出一行,得 %d 行: %q", n, out)
	}
	// 标志透传:text 显式 / page / sheet / 预算(markdown 也接受这些参数,不报用法错)
	if code := runDocCmd([]string{md, "--text", "--page", "1", "--sheet", "1", "--max-bytes", "4096", "--max-input-bytes", "1048576"}); code != 0 {
		t.Fatalf("标志透传应成功,得 %d", code)
	}
	// --tree 深度参数(工作台入口)
	if code := runDocCmd([]string{dir, "--tree", "--depth", "1"}); code != 0 {
		t.Fatalf("--tree --depth 1 应成功,得 %d", code)
	}
}

// TestRunDocCmdRasterExplicitErrors 锁定光栅路径的显式失败:
// 非 PDF + 源文件不存在都不得产出文件,退出码按哨兵错误映射(不支持=3 / 其它=1)。
func TestRunDocCmdRasterExplicitErrors(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "a.md")
	if err := os.WriteFile(md, []byte("# 标题\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	png := filepath.Join(dir, "out.png")
	if code := runDocCmd([]string{md, "--raster", "-o", png}); code != 3 {
		t.Fatalf("非 PDF 光栅应退出码 3(不支持格式),得 %d", code)
	}
	if _, err := os.Stat(png); err == nil {
		t.Fatal("光栅失败不得产出文件")
	}
	if code := runDocCmd([]string{filepath.Join(dir, "missing.pdf"), "--raster"}); code != 1 {
		t.Fatalf("源文件不存在应退出码 1,得 %d", code)
	}
	// --raster-self 同路径(未装 poppler 时仍不得产出文件)
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		if code := runDocCmd([]string{md, "--raster-self", "-o", png}); code != 3 {
			t.Fatalf("非 PDF 自包含光栅应退出码 3,得 %d", code)
		}
	}
}
