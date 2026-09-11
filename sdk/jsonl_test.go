package sdk

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readLines 逐行解析 jsonl,返回可解析记录数(坏行单独计数)。
func readLines(t *testing.T, path string) (ok int, bad int) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, ln := range strings.Split(string(raw), "\n") {
		if ln == "" {
			continue
		}
		var v map[string]any
		if json.Unmarshal([]byte(ln), &v) == nil {
			ok++
		} else {
			bad++
		}
	}
	return ok, bad
}

// TestAppendJSONLineRepairsPartialTail 尾部残行必须修掉,否则下一条追加会与它
// 粘成坏行 → 读侧按行解析失败,两条记录同时静默丢失。
func TestAppendJSONLineRepairsPartialTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.jsonl")
	if err := AppendJSONLine(p, map[string]any{"a": 1}); err != nil {
		t.Fatal(err)
	}
	// 模拟断电半写:合法首行 + 半截第二行
	if err := os.WriteFile(p, []byte(`{"a":1}`+"\n"+`{"a":2`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendJSONLine(p, map[string]any{"a": 3}); err != nil {
		t.Fatal(err)
	}
	ok, bad := readLines(t, p)
	if bad != 0 || ok != 2 {
		t.Fatalf("残行应被截断: ok=%d bad=%d", ok, bad)
	}
}

// TestAppendJSONLineAddsNewlineToCompleteTail 尾部是合法 JSON 但缺换行 → 补换行
// (否则与新记录粘成 `{...}{...}`,两条都丢)。
func TestAppendJSONLineAddsNewlineToCompleteTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.jsonl")
	if err := os.WriteFile(p, []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AppendJSONLine(p, map[string]any{"a": 2}); err != nil {
		t.Fatal(err)
	}
	ok, bad := readLines(t, p)
	if bad != 0 || ok != 2 {
		t.Fatalf("缺换行的完整行应补换行: ok=%d bad=%d", ok, bad)
	}
}

// TestAppendJSONLineNormalAndMissing 正常文件不被改动;文件不存在时自动创建。
func TestAppendJSONLineNormalAndMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "n.jsonl")
	for i := 0; i < 3; i++ {
		if err := AppendJSONLine(p, map[string]any{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, bad := readLines(t, p); ok != 3 || bad != 0 {
		t.Fatalf("正常追加: ok=%d bad=%d", ok, bad)
	}
	q := filepath.Join(dir, "new.jsonl")
	if err := AppendJSONLine(q, map[string]any{"i": 0}); err != nil {
		t.Fatalf("文件不存在应创建: %v", err)
	}
	if ok, _ := readLines(t, q); ok != 1 {
		t.Fatal("新文件应含一条记录")
	}
}
