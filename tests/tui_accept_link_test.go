// A-1 条目 38 的另一半:markdown 链接(OSC 8 超链接)。
// 口径:本 Harness 不发"打开链接"命令 —— md 链接用 **OSC 8** 交给终端(cmd/ctrl+点击由终端负责),
// 故产品侧可验的是"序列与 URL 正确写出";真正点开浏览器属终端行为(人眼,见剩余清单)。
package tests

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTUIAcceptMarkdownLinkOSC8(t *testing.T) {
	bin, env, root, _ := tuiAcceptSetup(t)
	writeAcceptFile(t, filepath.Join(root, "config"), "patch-acctui.yaml", `entries:
  - id: llm-openai-compat
    enabled: false
  - id: llm-mock
    enabled: true
    data:
      script: '[{"text":"见 [示例链接](https://example.com/x?a=1) 说明。"}]'
`)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("给个链接\r")
	if !s.waitRaw("示例链接", 30*time.Second) {
		t.Fatalf("条目 38:链接文本未渲染;尾段 %q", stripANSI(tailS(s.rawText(), 300)))
	}
	raw := s.rawText()
	hasURL := strings.Contains(raw, "https://example.com/x?a=1")
	hasOSC8 := strings.Contains(raw, "\x1b]8;")
	t.Logf("条目 38:OSC8=%v URL=%v", hasOSC8, hasURL)
	if !hasOSC8 {
		t.Errorf("条目 38:未写出 OSC 8 超链接序列")
	}
	if !hasURL {
		t.Errorf("条目 38:超链接目标 URL 未写入 OSC 8 载荷")
	}
}
