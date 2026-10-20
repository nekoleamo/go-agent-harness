package toolfiles

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// writeLines 造一个每行 "L<n>" 的文件,返回路径。
func writeLines(t *testing.T, dir string, n int) string {
	t.Helper()
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "L%d\n", i)
	}
	p := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestFileReadDefaultLimit 默认只读前 2000 行,并给出 next_offset(超大文件不一次灌满上下文)。
//
// **反向验证**:把 readSegment 的默认上限改回"不限"(limit<=0 时不设上限)必须让本用例转红。
func TestFileReadDefaultLimit(t *testing.T) {
	dir := t.TempDir()
	p := writeLines(t, dir, 5000)
	c := buildEnv(t, dir, sdk.SandboxWorkspace)

	out, err := call(t, c, "file_read", fmt.Sprintf(`{"path":%q}`, p))
	if err != nil || out["error"] != nil {
		t.Fatalf("读失败: %v %v", err, out)
	}
	lines := numOf(t, out["lines"])
	if lines != readDefaultLimit {
		t.Fatalf("默认应读 %d 行,实际 %d 行(上限没生效?)", readDefaultLimit, lines)
	}
	if out["truncated"] != true {
		t.Fatalf("还有内容却没标 truncated: %v", out)
	}
	next := numOf(t, out["next_offset"])
	if next != readDefaultLimit+1 {
		t.Fatalf("next_offset 应为 %d,实际 %d", readDefaultLimit+1, next)
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "offset=2001") {
		t.Fatalf("note 应说明如何续读: %v", out["note"])
	}
	// 首行内容正确(证明不是乱切)
	content, _ := out["content"].(string)
	if !strings.HasPrefix(content, "L1\n") {
		t.Fatalf("首行应为 L1: %q", firstLine(content))
	}
}

// TestFileReadOffsetLimit 指定 offset/limit 精确取段。
func TestFileReadOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	p := writeLines(t, dir, 100)
	c := buildEnv(t, dir, sdk.SandboxWorkspace)

	out, err := call(t, c, "file_read", fmt.Sprintf(`{"path":%q,"offset":10,"limit":3}`, p))
	if err != nil || out["error"] != nil {
		t.Fatalf("读失败: %v %v", err, out)
	}
	content, _ := out["content"].(string)
	if content != "L10\nL11\nL12" {
		t.Fatalf("取段内容不符: %q", content)
	}
	if out["truncated"] != true {
		t.Fatalf("后面还有内容应标 truncated: %v", out)
	}
	if s := numOf(t, out["start_line"]); s != 10 {
		t.Fatalf("start_line 应为 10: %v", out["start_line"])
	}
	if e := numOf(t, out["end_line"]); e != 12 {
		t.Fatalf("end_line 应为 12: %v", out["end_line"])
	}
	// 读完最后一段 ⇒ 不再 truncated,且无 next_offset
	out, _ = call(t, c, "file_read", fmt.Sprintf(`{"path":%q,"offset":99,"limit":10}`, p))
	if out["truncated"] != false {
		t.Fatalf("读到末尾不该 truncated: %v", out)
	}
	if _, ok := out["next_offset"]; ok {
		t.Fatalf("未截断时不应给 next_offset: %v", out)
	}
}

// TestFileReadLimitClamped limit 超上限按上限给并说明(不报错 —— 报错会让模型放弃读文件)。
func TestFileReadLimitClamped(t *testing.T) {
	dir := t.TempDir()
	p := writeLines(t, dir, 10)
	c := buildEnv(t, dir, sdk.SandboxWorkspace)

	out, err := call(t, c, "file_read", fmt.Sprintf(`{"path":%q,"limit":999999}`, p))
	if err != nil || out["error"] != nil {
		t.Fatalf("超上限不应报错: %v %v", err, out)
	}
	if note, _ := out["note"].(string); !strings.Contains(note, "上限") {
		t.Fatalf("note 应说明按上限给: %v", out["note"])
	}
}

// TestFileReadSmallFileUnchanged 小文件行为不变(不分页、不标截断):content 等于全文。
func TestFileReadSmallFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.txt")
	if err := os.WriteFile(p, []byte("你好文件"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := buildEnv(t, dir, sdk.SandboxWorkspace)
	out, err := call(t, c, "file_read", fmt.Sprintf(`{"path":%q}`, p))
	if err != nil {
		t.Fatal(err)
	}
	if out["content"] != "你好文件" {
		t.Fatalf("小文件内容应原样返回: %v", out["content"])
	}
	if out["truncated"] != false {
		t.Fatalf("小文件不该 truncated: %v", out)
	}
}

// numOf 取 JSON 数字字段(工具结果经 json.Unmarshal ⇒ 数字是 float64)。
func numOf(t *testing.T, v any) int {
	t.Helper()
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	t.Fatalf("不是数字: %v(%T)", v, v)
	return 0
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
