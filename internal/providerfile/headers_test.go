package providerfile

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GAH_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHeadersRoundTrip(t *testing.T) {
	writeFile(t, `active: g
providers:
  - name: g
    base_url: https://example.test/v1
    headers:
      x-opencode-session: ${session}
      User-Agent: gah/${version}
`)
	f, err := LoadFile()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Providers) != 1 {
		t.Fatalf("应有一条 provider: %+v", f.Providers)
	}
	h := f.Providers[0].Headers
	if h["x-opencode-session"] != "${session}" || h["User-Agent"] != "gah/${version}" {
		t.Fatalf("头没读回来(占位符必须原样保留,展开是宿主侧的事): %+v", h)
	}
	// 改一个头再存:**整组替换**(只传了一个键 ⇒ 只剩它)。
	// 这正是 UpdateHeaders 与 mergeFields 的分工:前者是「界面里改」,后者是「CLI 同名更新」。
	if err := UpdateHeaders("g", map[string]string{"x-opencode-session": "fixed"}); err != nil {
		t.Fatal(err)
	}
	f2, _ := LoadFile()
	h2 := f2.Providers[0].Headers
	if len(h2) != 1 || h2["x-opencode-session"] != "fixed" {
		t.Fatalf("应是整组替换: %+v", h2)
	}
}

// TestUpdateHeadersReplaceAndClear 覆盖式语义:存进去的就是要的那一组,删键表达得出来。
func TestUpdateHeadersReplaceAndClear(t *testing.T) {
	writeFile(t, `active: g
providers:
  - name: g
    headers:
      a: "1"
      b: "2"
`)
	if err := UpdateHeaders("g", map[string]string{"a": "9"}); err != nil {
		t.Fatal(err)
	}
	f, _ := LoadFile()
	h := f.Providers[0].Headers
	if len(h) != 1 || h["a"] != "9" {
		t.Fatalf("应是整组替换(删掉 b 表达得出来): %+v", h)
	}
	if err := UpdateHeaders("g", nil); err != nil {
		t.Fatal(err)
	}
	f, _ = LoadFile()
	if len(f.Providers[0].Headers) != 0 {
		t.Fatalf("清空应生效: %+v", f.Providers[0].Headers)
	}
	if err := UpdateHeaders("不存在", map[string]string{"a": "1"}); err == nil {
		t.Fatal("给不存在的 provider 设头应显式报错")
	}
}

// TestMergeFieldsHeaders 增量合并:AddProvider 同名更新时,头是逐键合并而不是整体替换。
func TestMergeFieldsHeaders(t *testing.T) {
	got := mergeFields(
		Provider{Name: "x", Headers: map[string]string{"a": "new", "c": "added"}},
		Provider{Name: "x", Headers: map[string]string{"a": "old", "b": "keep"}},
	)
	if got.Headers["a"] != "new" || got.Headers["b"] != "keep" || got.Headers["c"] != "added" {
		t.Fatalf("逐键合并不对: %+v", got.Headers)
	}
	// 显式置空 = 删这一个键
	got = mergeFields(Provider{Headers: map[string]string{"b": ""}}, got)
	if _, still := got.Headers["b"]; still {
		t.Fatalf("置空应删键: %+v", got.Headers)
	}
	if got.Headers["a"] != "new" || got.Headers["c"] != "added" {
		t.Fatalf("不应波及其它键: %+v", got.Headers)
	}
}

// TestExpandHeaders 占位符展开 + 未知占位符原样保留。
func TestExpandHeaders(t *testing.T) {
	in := map[string]string{
		"x-session": "sess-${session}",
		"ua":        "gah/${version}",
		"cwd":       "${cwd}/x",
		"mixed":     "${session}-${version}-${cwd}",
	}
	got := ExpandHeaders(in, HeaderVars{Session: "abc", Version: "0.5.5", CWD: "/tmp/p"})
	want := map[string]string{
		"x-session": "sess-abc",
		"ua":        "gah/0.5.5",
		"cwd":       "/tmp/p/x",
		"mixed":     "abc-0.5.5-/tmp/p",
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s 展开错: %q,期望 %q", k, got[k], v)
		}
	}
	// 未知占位符原样保留 —— 静默变空会悄悄改掉请求语义
	got = ExpandHeaders(map[string]string{"x": "${unknown}"}, HeaderVars{})
	if got["x"] != "${unknown}" {
		t.Fatalf("未知占位符应原样保留: %q", got["x"])
	}
	// 空值是合法的(主会话/未注入版本):替换成空串
	got = ExpandHeaders(map[string]string{"x": "a${session}b"}, HeaderVars{})
	if got["x"] != "ab" {
		t.Fatalf("空值应展开成空串: %q", got["x"])
	}
	if ExpandHeaders(nil, HeaderVars{}) != nil {
		t.Fatal("nil 输入应返回 nil")
	}
}
