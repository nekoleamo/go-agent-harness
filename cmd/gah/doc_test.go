// `gah doc` CLI 单测(D1):退出码映射 / 输出模式(对齐 DOC_PREVIEW_PLAN §6.3)。
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
