// P4-2 @文件引用补全单测:token 识别(URL/邮箱不触发)、候选刷新/过滤/关闭、
// 应用替换与光标、模型层键分派(Tab/Enter/↑↓/Esc 应用)、项目文件索引。
package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// keyText 可打印字符击键(Text 填充;default 分支依赖 Text 非空)。
func keyText(txt string) tea.KeyPressMsg {
	r := []rune(txt)
	code := rune(0)
	if len(r) == 1 {
		code = r[0]
	}
	return tea.KeyPressMsg{Code: code, Text: txt}
}

func TestMentionTokenAt(t *testing.T) {
	cases := []struct {
		in     string
		cursor int
		start  int
		ok     bool
	}{
		{"", 0, 0, false},
		{"@", 1, 0, true},
		{"@a", 2, 0, true},            // 词首 @
		{"看 @file", 7, 2, true},       // 空格后 @
		{"看 @", 3, 2, true},           // 空 token
		{"mail@x.com", 10, 0, false},  // 段内 @(非词首)→ 不触发
		{"http://a/@u", 12, 0, false}, // URL 段内 @:段起点非 @ → 不触发(防误触发)
		{"@a b", 2, 0, true},          // 光标在 @a 词尾(未越过空格):仍触发
		{"\u770b @file", 7, 2, true},  // 中文前缀+空格后 @ 触发
	}
	for _, c := range cases {
		start, ok := mentionTokenAt([]rune(c.in), c.cursor)
		if ok != c.ok || (ok && start != c.start) {
			t.Fatalf("%q c%d → (start=%d ok=%v),期望 (start=%d ok=%v)", c.in, c.cursor, start, ok, c.start, c.ok)
		}
	}
}

func TestSyncMentionFilterAndClose(t *testing.T) {
	files := []sdk.Option{
		{Value: "tui/model.go"}, {Value: "tui/state.go"}, {Value: "docs/ROADMAP.md"}, {Value: "go.mod"},
	}
	s := &State{Input: "\u770b @mo", Cursor: 5}
	s.syncMention(files)
	if s.Mention == nil {
		t.Fatal("光标在 @mo 后应激活候选")
	}
	if len(s.Mention.Items) != 2 || s.Mention.Items[0].Value != "tui/model.go" {
		t.Fatalf("token 'mo' 应过滤(含 go.mod): %+v", s.Mention.Items)
	}
	// 继续输入扩展 token(光标后移):model → 唯一命中 model.go
	s.Input = "\u770b @model"
	s.Cursor = 7
	s.syncMention(files)
	if len(s.Mention.Items) != 1 || s.Mention.Items[0].Value != "tui/model.go" {
		t.Fatalf("扩展后过滤: %+v", s.Mention.Items)
	}
	// 光标移出 token(到句子末尾)→ 关闭
	s.Input = "\u770b @model \u540e\u9762"
	s.Cursor = 10
	s.syncMention(files)
	if s.Mention != nil {
		t.Fatal("光标离开 token 应关闭候选")
	}
	// 纯 @:列出全部(供浏览)
	s2 := &State{Input: "\u8bf7 @", Cursor: 3}
	s2.syncMention(files)
	if s2.Mention == nil || len(s2.Mention.Items) != 4 {
		t.Fatalf("空 token 应全量: %+v", s2.Mention)
	}
	// 命令输入(/ 前缀)不启用
	s3 := &State{Input: "/model @x", Cursor: 9}
	s3.syncMention(files)
	if s3.Mention != nil {
		t.Fatal("命令输入不启用 @ 引用")
	}
}

func TestApplyMentionReplaces(t *testing.T) {
	s := &State{Input: "看 @mo 的代码", Cursor: 5} // token @mo → 应用替换区间 [2,5)
	s.Mention = &Mention{Start: 2, All: nil, Cursor: 0,
		Items: []sdk.Option{{Value: "tui/model.go"}}}
	if !s.applyMention("tui/model.go") {
		t.Fatal("应应用替换")
	}
	if s.Input != "看 tui/model.go 的代码" {
		t.Fatalf("替换结果: %q", s.Input)
	}
	if s.Mention != nil {
		t.Fatal("应用后候选应关闭")
	}
	wantCur := 2 + len([]rune("tui/model.go"))
	if s.Cursor != wantCur {
		t.Fatalf("光标应在路径后: %d 期望 %d", s.Cursor, wantCur)
	}
}

func TestMentionKeyDispatch(t *testing.T) {
	files := []sdk.Option{
		{Value: "tui/model.go"}, {Value: "tui/state.go"}, {Value: "docs/api.md"},
	}
	m := &Model{state: &State{}}
	m.onFiles = func() []sdk.Option { return files }
	m.onSubmit = func(string) {}

	// 输入 "@m"(逐字符触发候选刷新);完成后断言文本完整且候选过滤命中
	for _, ch := range "@m" {
		_, _ = m.Update(keyText(string(ch)))
	}
	if m.state.Input != "@m" {
		t.Fatalf("字符输入: %q", m.state.Input)
	}
	if m.state.Mention == nil || len(m.state.Mention.Items) != 2 {
		t.Fatalf("@m 应激活且过滤(model.go/api.md): %+v", m.state.Mention)
	}
	// Tab 应用首项
	if _, _ = m.Update(keyPress(tea.KeyTab, 0)); m.state.Input != "tui/model.go" {
		t.Fatalf("Tab 应应用候选: %q", m.state.Input)
	}
	if m.state.Mention != nil {
		t.Fatal("应用后关闭候选")
	}
	// ↑↓ 在候选中移动 + Enter 应用高亮
	m.state.Input = "@"
	m.state.Cursor = 1
	m.refreshMention()
	if m.state.Mention == nil || len(m.state.Mention.Items) != 3 {
		t.Fatalf("纯 @ 应列出全部: %+v", m.state.Mention)
	}
	if _, _ = m.Update(keyPress(tea.KeyDown, 0)); m.state.Mention.Cursor != 1 {
		t.Fatal("↓ 应移动候选光标")
	}
	if _, _ = m.Update(keyPress(tea.KeyUp, 0)); m.state.Mention.Cursor != 0 {
		t.Fatal("↑ 应回候选首项")
	}
	if _, _ = m.Update(keyPress(tea.KeyEnter, 0)); m.state.Input != "tui/model.go" {
		t.Fatalf("Enter 应应用高亮项: %q", m.state.Input)
	}
	// Esc 关闭候选(文本保留)
	m.state.Input = "等 @"
	m.state.Cursor = 3
	m.refreshMention()
	if m.state.Mention == nil {
		t.Fatal("应为激活态")
	}
	if _, _ = m.Update(keyPress(tea.KeyEscape, 0)); m.state.Mention != nil {
		t.Fatal("Esc 应关闭候选")
	}
	if m.state.Input != "等 @" {
		t.Fatalf("Esc 后文本保留: %q", m.state.Input)
	}
}

func TestIndexProjectFiles(t *testing.T) {
	dir := t.TempDir()
	mk := func(p string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("a.go")
	mk("pkg/b.go")
	mk("docs/read.md")
	mk(".hidden/h.go")          // 隐藏目录跳过
	mk("node_modules/dep/x.go") // 依赖目录跳过
	mk("vendor/y.go")           // 命名约定外保留
	opts := indexProjectFiles(dir)
	got := map[string]bool{}
	for _, o := range opts {
		got[o.Value] = true
	}
	for _, want := range []string{"a.go", "pkg/b.go", "docs/read.md", "vendor/y.go"} {
		if !got[want] {
			t.Fatalf("索引应含 %s: %+v", want, got)
		}
	}
	for _, no := range []string{".hidden/h.go", "node_modules/dep/x.go"} {
		if got[no] {
			t.Fatalf("不应含 %s: %+v", no, got)
		}
	}
	if !strings.HasPrefix(opts[0].Value, "a.go") && !strings.HasSuffix(opts[len(opts)-1].Value, "y.go") {
		t.Fatal("应字典序排序")
	}
}

func TestRenderMentionWindow(t *testing.T) {
	s := &State{Input: "@m", Cursor: 2, Mention: &Mention{Start: 0, Cursor: 0,
		Items: []sdk.Option{{Value: "tui/model.go"}, {Value: "tui/main.go"}}}}
	out := Render(s, 80, 12)
	clean := stripColor(out)
	if !strings.Contains(clean, "@tui/model.go") {
		t.Fatalf("候选窗口应渲染: %q", clean)
	}
	if !strings.Contains(clean, "▸") {
		t.Fatal("当前候选应高亮")
	}
}
