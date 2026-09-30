// `gah doc` 边界分支补测(第九十四批):目录树标记与告警、不支持格式、
// JSON/text 两条链上的读取失败。这些分支在成功用例里到不了,却决定"失败时退出码对不对"。
package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr 捕获 os.Stderr(doc 命令的提示/告警都走它)。
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	_ = w.Close()
	os.Stderr = old
	out := <-done
	_ = r.Close()
	return out
}

// TestRunDocCmdTreeMarksWarningsAndErrors 目录树模式:
// 子目录要带 "/" 标记、文件要带大小列、重型目录要显式告知被跳过;
// 路径不可读时按哨兵错误映射退出码(不是静默空输出)。
func TestRunDocCmdTreeMarksWarningsAndErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "inner.md"), "# 内层\n")
	writeFile(t, filepath.Join(dir, "a.md"), "abc\n") // 4 字节:大小列可逐字断言
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	var code int
	errOut := captureStderr(t, func() {
		code = runDocCmd([]string{dir, "--tree"})
	})
	if code != 0 {
		t.Fatalf("--tree 应成功,得 %d(stderr: %s)", code, errOut)
	}
	if !strings.Contains(errOut, "已跳过重型/元数据目录") || !strings.Contains(errOut, ".git") {
		t.Fatalf("被跳过的目录要显式告知: %q", errOut)
	}

	out := captureStdout(t, func() {
		if c := runDocCmd([]string{dir, "--tree", "--depth", "2"}); c != 0 {
			t.Errorf("--tree --depth 2 应成功,得 %d", c)
		}
	})
	if !strings.Contains(out, "sub/") {
		t.Fatalf("子目录应带 / 标记:%q", out)
	}
	if !strings.Contains(out, "a.md") || !strings.Contains(out, "(4 B)") {
		t.Fatalf("文件应带大小列:%q", out)
	}

	// 不存在的路径:错误 + 退出码(不是空目录树)
	errOut = captureStderr(t, func() {
		if c := runDocCmd([]string{filepath.Join(dir, "nope"), "--tree"}); c == 0 {
			t.Error("不存在的路径不该成功")
		}
	})
	if !strings.Contains(errOut, "gah doc:") {
		t.Fatalf("读取失败应报错:%q", errOut)
	}
}

// TestRunDocCmdUnsupportedAndMissing 文本/JSON 两条链上的失败与"不支持格式"退出码 3。
func TestRunDocCmdUnsupportedAndMissing(t *testing.T) {
	dir := t.TempDir()
	md := filepath.Join(dir, "a.md")
	writeFile(t, md, "第一行\n第二行\n")
	bin := filepath.Join(dir, "a.bin")
	writeFile(t, bin, "\x00\x01二进制")

	// 文本:缺失文件 → 退出码 1
	errOut := captureStderr(t, func() {
		if c := runDocCmd([]string{filepath.Join(dir, "missing.md")}); c != 1 {
			t.Errorf("缺失文件应退出码 1,得 %d", c)
		}
	})
	if !strings.Contains(errOut, "gah doc:") {
		t.Fatalf("缺失文件应报错:%q", errOut)
	}
	// 文本:不支持的格式 → 退出码 3(且提示里带退出码口径)
	errOut = captureStderr(t, func() {
		if c := runDocCmd([]string{bin}); c != 3 {
			t.Errorf("不支持格式应退出码 3,得 %d", c)
		}
	})
	if !strings.Contains(errOut, "不支持预览该格式") {
		t.Fatalf("不支持格式应显式说明:%q", errOut)
	}
	// JSON:缺失文件同样退出码 1(不许输出空 JSON 骗人)
	errOut = captureStderr(t, func() {
		if c := runDocCmd([]string{filepath.Join(dir, "missing.md"), "--json"}); c != 1 {
			t.Errorf("JSON 缺失文件应退出码 1,得 %d", c)
		}
	})
	if !strings.Contains(errOut, "gah doc:") {
		t.Fatalf("JSON 缺失文件应报错:%q", errOut)
	}
	// JSON:不支持格式 → 退出码 3(内容仍是有效 JSON,便于调用方读 meta)
	errOut = captureStderr(t, func() {
		if c := runDocCmd([]string{bin, "--json"}); c != 3 {
			t.Errorf("JSON 不支持格式应退出码 3,得 %d", c)
		}
	})
	if !strings.Contains(errOut, "不支持预览该格式") {
		t.Fatalf("JSON 不支持格式应提示:%q", errOut)
	}
	// --md:只输出正文,不带行号列
	out := captureStdout(t, func() {
		if c := runDocCmd([]string{md, "--md"}); c != 0 {
			t.Errorf("--md 应成功,得 %d", c)
		}
	})
	if strings.Contains(out, "|") || !strings.Contains(out, "第一行") {
		t.Fatalf("--md 不该带行号列:%q", out)
	}
}
