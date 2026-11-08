package memory

// 收录范围行(scope)的契约。
//
// 这些断言钉的都是"错了会静默害人"的方向:
//   - 它必须**不改动文件其余内容**(这个文件是人的手写本,顺手重排就没人敢改了);
//   - 它必须**只在真会渲染项目条目时**出现(空小节挂一句范围行既误导又白占预算);
//   - 它必须**算进注入预算**(否则记忆超预算把上下文顶出去 —— 这一层要防的正是这个)。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func scopeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	return home
}

func TestScopeRoundTrip(t *testing.T) {
	scopeHome(t)
	key := "Documents-Work-demo"
	if err := SetScope(key, "只收编译/测试/发布约定"); err != nil {
		t.Fatal(err)
	}
	got, err := ScopeLine(ProjectPath(key))
	if err != nil {
		t.Fatal(err)
	}
	if got != "只收编译/测试/发布约定" {
		t.Fatalf("往返不对: %q", got)
	}
	// 加了范围行之后,往同一个文件里记条目仍然正常解析(注释不算记忆)。
	if err := Append(ProjectPath(key), "发版前跑 gate.sh", ""); err != nil {
		t.Fatal(err)
	}
	es, err := Read(ProjectPath(key))
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].Content != "发版前跑 gate.sh" {
		t.Fatalf("范围行不该被当成记忆条目: %+v", es)
	}
}

// 改范围只动首行:正文**逐字节**保留。
func TestSetScopeReplacesHeaderOnly(t *testing.T) {
	scopeHome(t)
	key := "k1"
	body := "- [2026-10-01] 第一条(来源: 会话 s1)\n- [2026-10-02] 第二条\n"
	if err := os.MkdirAll(filepath.Dir(ProjectPath(key)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProjectPath(key), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"第一版范围", "第二版范围"} {
		if err := SetScope(key, scope); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(ProjectPath(key))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(raw), body) {
			t.Fatalf("正文被改了(scope=%q):\n%s", scope, raw)
		}
		if n := strings.Count(string(raw), "<!-- scope:"); n != 1 {
			t.Fatalf("范围行应恰好一行, got %d:\n%s", n, raw)
		}
	}
	// 清除:正文仍然完整,范围行消失。
	if err := SetScope(key, ""); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(ProjectPath(key))
	if string(raw) != body {
		t.Fatalf("清除后应与原文逐字节相同:\n%q", raw)
	}
	if got, err := ScopeLine(ProjectPath(key)); err != nil || got != "" {
		t.Fatalf("清除后应读不到范围, got %q err=%v", got, err)
	}
}

func TestSetScopeRejectsBadInput(t *testing.T) {
	scopeHome(t)
	if err := SetScope("k", "第一行\n第二行"); err == nil {
		t.Fatal("多行应拒(范围只能一句话)")
	}
	long := strings.Repeat("字", MaxScopeBytes) // 每字 3 字节 ⇒ 远超上限
	if err := SetScope("k", long); err == nil {
		t.Fatal("超长应拒")
	}
	if err := SetScope("  ", "x"); err == nil {
		t.Fatal("没有项目 key 应显式报错")
	}
}

func TestScopeLineMissingAndNoHeader(t *testing.T) {
	scopeHome(t)
	if got, err := ScopeLine(ProjectPath("nope")); err != nil || got != "" {
		t.Fatalf("文件不存在应是空串而非错误, got %q err=%v", got, err)
	}
	path := ProjectPath("plain")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("- [2026-10-01] 只有记忆\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ScopeLine(path); err != nil || got != "" {
		t.Fatalf("没有范围行应是空串, got %q err=%v", got, err)
	}
}

// 渲染:范围行落在「本项目记忆」小节标题之后、条目之前。
func TestInjectRendersScopeInProjectSection(t *testing.T) {
	scopeHome(t)
	user := []Entry{{Date: "2026-10-01", Content: "用户级偏好"}}
	proj := []Entry{{Date: "2026-10-02", Content: "项目级约定"}}
	text := Inject(user, proj, DefaultInjectBudget, "只收编译约定的口径")
	iTitle := strings.Index(text, "### 本项目记忆")
	iScope := strings.Index(text, "[收录范围] 只收编译约定的口径")
	iEntry := strings.Index(text, "项目级约定")
	if iScope < 0 {
		t.Fatalf("应渲染范围行:\n%s", text)
	}
	if !(iTitle < iScope && iScope < iEntry) {
		t.Fatalf("范围行应在「本项目记忆」标题之后、条目之前(title=%d scope=%d entry=%d):\n%s", iTitle, iScope, iEntry, text)
	}
}

// 没有项目条目时**不渲染**范围行:它属于小节,给空小节加一句话是误导;
// 更关键的是它**不能白占预算** —— 那条预算是用户级记忆的。
func TestInjectNoScopeWithoutProjectEntries(t *testing.T) {
	scopeHome(t)
	content := "只有用户级"
	// 预算恰好够「壳 + 这一条」:范围行若被预留,这条就会被挤出去。
	one := len("  · ") + len(content) + 1
	text := Inject([]Entry{{Date: "2026-10-01", Content: content}}, nil, ShellOverhead+one, "范围")
	if strings.Contains(text, "[收录范围]") {
		t.Fatalf("没有项目条目时不该出现范围行:\n%s", text)
	}
	if !strings.Contains(text, content) {
		t.Fatalf("范围行白占了用户级预算(那条记忆被挤掉了):\n%s", text)
	}
}

// 范围行**算进预算**:无论预算多紧,输出都不得超过预算。
func TestInjectScopeCountsAgainstBudget(t *testing.T) {
	scopeHome(t)
	user := []Entry{{Date: "2026-10-01", Content: "用户级偏好"}}
	proj := []Entry{{Date: "2026-10-02", Content: "项目级约定"}}
	scope := strings.Repeat("收", 60) // 180 字节,足够把极小预算挤爆
	for b := ShellOverhead + 64; b < 2048; b += 13 {
		got := Inject(user, proj, b, scope)
		if len(got) > b {
			t.Fatalf("预算 %d 时输出 %d 字节(范围行没算进预算):\n%s", b, len(got), got)
		}
	}
}

// 手改文件写超长的范围:注入时按 rune 边界截断,不留半个汉字,也不顶爆预算。
func TestInjectTruncatesOverlongScopeSafely(t *testing.T) {
	scopeHome(t)
	long := strings.Repeat("范", MaxScopeBytes) // 3 字节/字 ⇒ 超限 3 倍
	got := renderScope(long)
	if !utf8.ValidString(got) {
		t.Fatal("截断切坏了 UTF-8(半个汉字会污染提示词)")
	}
	if body := strings.TrimSuffix(strings.TrimPrefix(got, "  [收录范围] "), "\n"); len(body) > MaxScopeBytes {
		t.Fatalf("截断后仍超上限: %d 字节", len(body))
	}
}

// 范围行不是记忆条目 ⇒ 不参与"稳定块已注入"去重集合。
func TestInjectedContentsIgnoresScope(t *testing.T) {
	scopeHome(t)
	user := []Entry{{Date: "2026-10-01", Content: "用户级偏好"}}
	proj := []Entry{{Date: "2026-10-02", Content: "项目级约定"}}
	set := InjectedContents(user, proj, DefaultInjectBudget, "只收编译约定")
	if set == nil {
		t.Fatal("应至少收进一条")
	}
	for k := range set {
		if strings.Contains(k, "收录范围") {
			t.Fatalf("范围行不该被当成记忆内容: %q", k)
		}
	}
	// 条数口径与 Inject 一致(范围行不占"条数")。
	if n := StableCount(user, proj, DefaultInjectBudget, "只收编译约定"); n != 2 {
		t.Fatalf("稳定块条数应是 2 条记忆, got %d", n)
	}
}
