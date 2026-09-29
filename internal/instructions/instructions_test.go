package instructions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nekoleamo/go-agent-harness/internal/roles"
)

// TestPathUnderHome 路径必须落在数据根(便携纪律:一切自身运行数据经 GAH_HOME 派生)。
func TestPathUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if got, want := Path(), filepath.Join(home, FileName); got != want {
		t.Fatalf("路径不在数据根: %q != %q", got, want)
	}
}

// existsOnDisk 文件是否在盘上(仅测试用:生产侧不需要这个入口 —— Read 已回 exists bool,
// 面板也按 Read 的返回值渲染;少一个导出函数就少一处漂移面)。
func existsOnDisk() bool {
	_, err := os.Stat(Path())
	return err == nil
}

// TestReadMissingIsNotError "没有全局指令"是合法状态:不报错、不算存在。
func TestReadMissingIsNotError(t *testing.T) {
	t.Setenv("GAH_HOME", t.TempDir())
	text, ok, err := Read()
	if err != nil || ok || text != "" {
		t.Fatalf("缺文件应回空且无错: %q %v %v", text, ok, err)
	}
	if existsOnDisk() {
		t.Fatal("缺文件时 Exists 应为 false")
	}
}

// TestWriteReadRoundTrip 写入即读回(含目录不存在时自建)与原子替换语义。
func TestWriteReadRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	body := "全局规则:先说结论。\n"
	if err := Write(body); err != nil {
		t.Fatal(err)
	}
	text, ok, err := Read()
	if err != nil || !ok || text != body {
		t.Fatalf("读回不一致: %q %v %v", text, ok, err)
	}
	if !existsOnDisk() {
		t.Fatal("写入后 Exists 应为 true")
	}
	// 覆盖写(旧内容不残留)
	if err := Write("新的\n"); err != nil {
		t.Fatal(err)
	}
	if text, _, _ := Read(); text != "新的\n" {
		t.Fatalf("覆盖写未生效: %q", text)
	}
	// 不留临时文件(失败也要清理)
	ents, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".gah-instructions-") {
			t.Fatalf("残留临时文件: %s", e.Name())
		}
	}
}

// TestWriteRejectsOverMax 超上限显式失败且不落盘(截断一份指令 = 悄悄改用户的话)。
func TestWriteRejectsOverMax(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := Write(strings.Repeat("a", MaxBytes+1)); err == nil {
		t.Fatal("超上限应报错")
	} else if !strings.Contains(err.Error(), "超上限") {
		t.Fatalf("错误信息应说明超上限: %v", err)
	}
	if existsOnDisk() {
		t.Fatal("超限失败不应落盘")
	}
	// 边界:正好等于上限可以通过
	if err := Write(strings.Repeat("a", MaxBytes)); err != nil {
		t.Fatalf("边界值应放行: %v", err)
	}
}

// TestInjectedTruncatesUTF8 注入侧超限截断不切半截字符 + 带显式标注;未超限原样返回。
func TestInjectedTruncatesUTF8(t *testing.T) {
	small := "短"
	if got, cut := Injected(small); got != small || cut {
		t.Fatalf("未超限不该改: %q %v", got, cut)
	}
	// 边界值:恰好等于上限 → 不截断(与 Write 放行边界一致)
	if got, cut := Injected(strings.Repeat("a", MaxBytes)); cut || len(got) != MaxBytes {
		t.Fatalf("边界值不该截断: len=%d cut=%v", len(got), cut)
	}
	// 构造:前缀填满到 MaxBytes-1,再放一个 3 字节汉字 → 必须退到字符边界
	body := strings.Repeat("a", MaxBytes-1) + "汉"
	got, cut := Injected(body)
	if !cut {
		t.Fatal("超限应报告截断")
	}
	if !strings.HasSuffix(got, "\n(全局指令超过 "+fmt.Sprint(MaxBytes)+" 字节上限,已截断)") {
		t.Fatalf("截断必须带标注(否则用户不知道指令被截了): %q", got[len(got)-20:])
	}
	if !strings.HasSuffix(strings.TrimSuffix(got, "\n(全局指令超过 "+fmt.Sprint(MaxBytes)+" 字节上限,已截断)"), "a") {
		t.Fatalf("截断应退到字符边界(不能留半截汉字): %q", got[len(got)-30:])
	}
	if !utf8.ValidString(got) {
		t.Fatalf("截断后必须仍是合法 UTF-8: %q", got)
	}
}

// TestMaxBytesMatchesRoles 两份指令(全局/角色)上限必须一致:不一致会让「这里能存
// 那里不能」变成悬案(面板同屏展示两者)。
func TestMaxBytesMatchesRoles(t *testing.T) {
	if MaxBytes != roles.MaxAgentsBytes {
		t.Fatalf("上限漂移: instructions=%d roles=%d", MaxBytes, roles.MaxAgentsBytes)
	}
}

// TestReadErrorNotSwallowed 读错误必须如实上报:不能把"读不了"当成"没有全局指令"
// (那会让用户以为自己的指令被清了)。
func TestReadErrorNotSwallowed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.Mkdir(filepath.Join(home, FileName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Read(); err == nil {
		t.Fatal("路径被目录占用时应报错,而不是回空")
	}
}

// TestWriteFailureLeavesNoTempAndKeepsOriginal 写失败(改名目标被目录占住)时:
// 报错、清掉临时文件、原有东西一个不动。
func TestWriteFailureLeavesNoTempAndKeepsOriginal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	dir := filepath.Join(home, FileName)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Write("新内容")
	if err == nil {
		t.Fatal("改名到目录应失败")
	}
	if !strings.Contains(err.Error(), "全局指令写入失败") {
		t.Fatalf("错误应带上语义(不是裸 rename 错误): %v", err)
	}
	ents, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".gah-instructions-") {
			t.Fatalf("失败后残留临时文件: %s", e.Name())
		}
	}
	if b, err := os.ReadFile(marker); err != nil || string(b) != "keep" {
		t.Fatalf("失败不该动原内容: %q %v", b, err)
	}
}

// TestWriteMkdirFailure 数据根不可用(父级是文件)→ 显式报错并指出路径。
func TestWriteMkdirFailure(t *testing.T) {
	base := t.TempDir()
	notADir := filepath.Join(base, "notadir")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAH_HOME", filepath.Join(notADir, "home"))
	err := Write("x")
	if err == nil {
		t.Fatal("数据根不可写时应报错")
	}
	if !strings.Contains(err.Error(), "数据根不可写") {
		t.Fatalf("错误应指出数据根: %v", err)
	}
}
