// updates_test.go:纯函数层测试(事件 → 呈现的映射规则与文本口径)。
package acpserver

import (
	"os"
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

func TestToolMetaMapsKnownTools(t *testing.T) {
	cases := []struct {
		name, args, wantKind, wantTitleHas string
		wantPath                           string
	}{
		{"file_read", `{"path":"/w/a.txt"}`, "read", "a.txt", "/w/a.txt"},
		{"file_write", `{"path":"/w/b.txt","content":"x"}`, "edit", "b.txt", "/w/b.txt"},
		{"file_append", `{"path":"/w/c.txt"}`, "edit", "c.txt", "/w/c.txt"},
		{"file_edit", `{"path":"/w/d.txt"}`, "edit", "d.txt", "/w/d.txt"},
		{"shell", `{"command":"go test ./..."}`, "execute", "go test", ""},
		{"web_search", `{"query":"acp 协议"}`, "search", "acp 协议", ""},
		{"web_fetch", `{"url":"https://example.com/x"}`, "fetch", "https://example.com/x", ""},
		{"read_document", `{"path":"/w/doc.md"}`, "read", "doc.md", "/w/doc.md"},
		{"subagent", `{"task":"重构"}`, "think", "重构", ""},
		{"todo", `{"subject":"写测试"}`, "other", "写测试", ""},
		{"mcp__github__list_issues", `{"repo":"a/b"}`, "other", "mcp__github__list_issues", ""},
	}
	for _, c := range cases {
		title, kind, path := toolMeta(c.name, c.args)
		if kind != c.wantKind {
			t.Errorf("%s: kind=%s want %s", c.name, kind, c.wantKind)
		}
		if !strings.Contains(title, c.wantTitleHas) {
			t.Errorf("%s: title=%q 应含 %q", c.name, title, c.wantTitleHas)
		}
		if path != c.wantPath {
			t.Errorf("%s: path=%q want %q", c.name, path, c.wantPath)
		}
	}
	// 坏 JSON 不 panic:退回原名 + 参数文本
	title, kind, path := toolMeta("shell", "{坏")
	if kind != "execute" || title == "" || path != "" {
		t.Fatalf("坏 JSON 应退化为标题兜底: %q %q %q", title, kind, path)
	}
}

func TestToolMetaTitleSingleLineAndTruncated(t *testing.T) {
	_, _, _ = toolMeta("shell", "{}")
	title, _, _ := toolMeta("shell", `{"command":"line1
line2"}`)
	if strings.Contains(title, "\n") {
		t.Fatalf("标题必须单行(编辑器标题栏): %q", title)
	}
	long, _, _ := toolMeta("shell", `{"command":"`+strings.Repeat("x", 500)+`"}`)
	if len(long) > 200 {
		t.Fatalf("标题应截断: %d", len(long))
	}
}

func TestFileChangeText(t *testing.T) {
	// 普通改动:摘要 + patch
	got := fileChangeText(sdk.FileChangeEvent{
		Path: "/w/a.go", Op: "write", Added: 2, Removed: 1, Diff: "@@ -1 +1,2 @@\n-a\n+b\n+c\n",
	})
	if !strings.Contains(got, "a.go +2 -1") || !strings.Contains(got, "@@ -1 +1,2 @@") {
		t.Fatalf("改动摘要异常: %q", got)
	}
	// 新建标注
	got = fileChangeText(sdk.FileChangeEvent{Path: "/w/b.txt", Op: "write", Created: true, Added: 3})
	if !strings.Contains(got, "新建") {
		t.Fatalf("新建应标注: %q", got)
	}
	// 二进制:无逐行 diff 必须显式说明
	got = fileChangeText(sdk.FileChangeEvent{Path: "/w/c.bin", Op: "write", Binary: true, Diff: "xx"})
	if !strings.Contains(got, "二进制") || strings.Contains(got, "xx") {
		t.Fatalf("二进制应只报统计: %q", got)
	}
	// 超预算截断标注
	got = fileChangeText(sdk.FileChangeEvent{Path: "/w/d.txt", Op: "write", Diff: strings.Repeat("y", 10000), Truncated: true})
	if !strings.Contains(got, "截断") {
		t.Fatalf("截断应标注: %q", got)
	}
}

func TestTruncateAndOneLine(t *testing.T) {
	if got := truncate("abc", 10); got != "abc" {
		t.Fatalf("未超预算不应改: %q", got)
	}
	got := truncate(strings.Repeat("a", 100), 10)
	if !strings.HasPrefix(got, "aaaaaaaaaa") || !strings.Contains(got, "截断") {
		t.Fatalf("截断口径异常: %q", got)
	}
	if got := oneLine("  a \n b  ", 50); got != "a b" {
		t.Fatalf("oneLine 应压成单行: %q", got)
	}
}

func TestPayloadOfAcceptsValueAndPointer(t *testing.T) {
	c := sdk.ToolCallEvent{ID: "x", Name: "shell"}
	if got, ok := payloadOf[sdk.ToolCallEvent](c); !ok || got.ID != "x" {
		t.Fatalf("值形状应可解析: %v %v", got, ok)
	}
	if got, ok := payloadOf[sdk.ToolCallEvent](&c); !ok || got.ID != "x" {
		t.Fatalf("指针形状应可解析: %v %v", got, ok)
	}
	if _, ok := payloadOf[sdk.ToolCallEvent](nil); ok {
		t.Fatal("nil 不应解析成功")
	}
	if _, ok := payloadOf[sdk.ToolCallEvent](map[string]any{}); ok {
		t.Fatal("map 形状不应解析成功(显式退化,不猜测)")
	}
	// 回放路径(sessionlog.normalizePayload)→ 值形状;广播路径 → 指针形状,两条都要认
	se := sdk.SessionEvent{Kind: sdk.EventToolCall, Payload: c}
	if got := sessionEventOf(&se); got == nil || got.Kind != sdk.EventToolCall {
		t.Fatal("SessionEvent 指针形状应可解析")
	}
	if got := sessionEventOf(se); got == nil || got.Kind != sdk.EventToolCall {
		t.Fatal("SessionEvent 值形状应可解析")
	}
	if sessionEventOf("x") != nil {
		t.Fatal("非法载荷应返回 nil")
	}
}

func TestPromptTextBaseline(t *testing.T) {
	// 基线内容:多段 text 拼接 + resource_link 渲染为引用
	got, err := promptText([]contentBlock{
		{Type: "text", Text: "第一段"},
		{Type: "text", Text: "   "},
		{Type: "resource_link", Name: "说明.md", URI: "file:///w/说明.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "第一段\n[资源] 说明.md file:///w/说明.md" {
		t.Fatalf("拼接口径异常: %q", got)
	}
	// 未声明能力的类型:显式报错(不静默丢内容)
	for _, kind := range []string{"image", "audio", "resource"} {
		if _, err := promptText([]contentBlock{{Type: kind, Data: "x"}}); err == nil {
			t.Fatalf("%s 应显式报错", kind)
		}
	}
	if _, err := promptText([]contentBlock{{Type: "weird"}}); err == nil {
		t.Fatal("未知类型应显式报错")
	}
	if _, err := promptText(nil); err == nil {
		t.Fatal("空提示应显式报错")
	}
}

func TestSameDirNormalizesSymlinks(t *testing.T) {
	dir := t.TempDir()
	if !sameDir(dir, dir+"/.") {
		t.Fatal("等价路径应判同")
	}
	if sameDir(dir, dir+"/sub") {
		t.Fatal("不同目录不应判同")
	}
	// 软链(如 macOS /tmp → /private/tmp)必须判同,否则同一工作区被拒
	link := dir + "/link"
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("无软链权限: %v", err)
	}
	if !sameDir(dir, link) {
		t.Fatal("软链应归一为同一目录")
	}
}

func TestRawArgsRejectsBadJSON(t *testing.T) {
	if rawArgs("") != nil || rawArgs("{坏") != nil {
		t.Fatal("空/坏 JSON 应回 nil(不把半截 JSON 当结构化输入送出)")
	}
	v, ok := rawArgs(`{"a":1}`).(map[string]any)
	if !ok || v["a"] != float64(1) {
		t.Fatalf("正常 JSON 应解析: %v", v)
	}
}
