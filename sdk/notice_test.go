package sdk

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestNoticeNormalizeLevel 级别归一:合法值原样、非法/空 → info(拼错不该让提示消失,也不该假装严重)。
func TestNoticeNormalizeLevel(t *testing.T) {
	cases := []struct {
		in   NoticeLevel
		want NoticeLevel
	}{
		{NoticeInfo, NoticeInfo}, {NoticeWarn, NoticeWarn}, {NoticeError, NoticeError},
		{"", NoticeInfo}, {"ERROR", NoticeInfo}, {"critical", NoticeInfo},
	}
	for _, c := range cases {
		if got := (Notice{Level: c.in, Title: "x"}).Normalize().Level; got != c.want {
			t.Fatalf("Level %q 归一为 %q,期望 %q", c.in, got, c.want)
		}
	}
}

// TestNoticeNormalizeSinceFallback 空标题回落正文首行;两者皆空给兜底文案(不静默丢弃)。
func TestNoticeNormalizeSinceFallback(t *testing.T) {
	n := Notice{Body: "\n\n  失败:连接超时\n细节\n"}.Normalize()
	if n.Title != "失败:连接超时" {
		t.Fatalf("空标题应回落正文首个非空行,得到 %q", n.Title)
	}
	empty := Notice{}.Normalize()
	if empty.Title != "有一条提示" {
		t.Fatalf("空标题空正文应有兜底文案,得到 %q", empty.Title)
	}
	if empty.Source != "unknown" {
		t.Fatalf("未声明来源应归一为 unknown,得到 %q", empty.Source)
	}
}

// TestNoticeNormalizeClipExplicit 超长字段裁剪必须**显式**(加省略号,不静默丢)。
func TestNoticeNormalizeClipExplicit(t *testing.T) {
	long := strings.Repeat("字", NoticeTitleMax+50)
	n := Notice{Title: long, Body: strings.Repeat("细", NoticeBodyMax+50), Source: strings.Repeat("s", NoticeSourceMax+5)}.Normalize()
	if got := len([]rune(n.Title)); got != NoticeTitleMax+1 { // +1 = 省略号
		t.Fatalf("标题裁剪后 rune 数 = %d,期望 %d(+省略号)", got, NoticeTitleMax+1)
	}
	if !strings.HasSuffix(n.Title, "…") || !strings.HasSuffix(n.Body, "…") {
		t.Fatalf("裁剪必须显式加省略号: %q / %q", n.Title, n.Body)
	}
	if got := len([]rune(n.Body)); got != NoticeBodyMax+1 {
		t.Fatalf("正文裁剪后 rune 数 = %d,期望 %d(+省略号)", got, NoticeBodyMax+1)
	}
	if len([]rune(n.Source)) != NoticeSourceMax+1 {
		t.Fatalf("来源应裁到 %d rune(+省略号),得到 %q", NoticeSourceMax, n.Source)
	}
	// rune 边界安全(中文不被切成半个字符 = 不出现替换符)
	if strings.ContainsRune(n.Title, '\uFFFD') {
		t.Fatal("裁剪破坏 rune 边界")
	}
}

// TestNoticeNormalizeTitleFoldsLines 标题折叠成单行(状态行/toast 的显示前提)。
func TestNoticeNormalizeTitleFoldsLines(t *testing.T) {
	n := Notice{Title: "计划\t执行\n失败"}.Normalize()
	if n.Title != "计划 执行 失败" {
		t.Fatalf("标题应折叠为单行,得到 %q", n.Title)
	}
	if strings.ContainsAny(n.Title, "\n\t") {
		t.Fatal("标题不得含换行/制表符")
	}
}

// TestNoticeFieldsSerialize 字段名是跨端契约(JSON 键名 = 前端类型字段),改名即断。
func TestNoticeFieldsSerialize(t *testing.T) {
	if EventNotice != "notice" {
		t.Fatalf("事件名冻结为 notice,得到 %q", EventNotice)
	}
	n := Notice{ID: 7, Level: NoticeWarn, Title: "t", Body: "b", Source: "host-jobs", TS: time.Unix(0, 0).UTC()}
	b, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"id":7`, `"level":"warn"`, `"title":"t"`, `"body":"b"`, `"source":"host-jobs"`, `"ts":`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("JSON 缺字段 %s: %s", key, b)
		}
	}
}
