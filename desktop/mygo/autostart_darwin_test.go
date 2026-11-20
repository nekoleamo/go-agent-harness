//go:build darwin

package main

import (
	"strings"
	"testing"
)

// TestXMLMarksReplaced 开机自启的 plist 里路径必须转义(路径带 & / < 是合法目录名)。
func TestPlistEscapesBundlePath(t *testing.T) {
	got := plistContent("/Users/x/Dev & Tools/gah.app")
	if strings.Contains(got, "Dev & Tools") {
		t.Fatal("未转义的 & 会让 plist 解析失败")
	}
	if !strings.Contains(got, "Dev &amp; Tools") {
		t.Fatalf("应转义成 &amp;,得到 %q", got)
	}
}
