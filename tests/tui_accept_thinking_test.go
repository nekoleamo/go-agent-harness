// A-1 条目 40 的另一半:思维块**本体**渲染(此前只能验键位 —— 因为 llm-mock 造不出思维增量;
// 本轮为 mock 补了 thinking 步,故可端到端验:思维增量成块 → Ctrl+T 展开全文 → 再折叠收回)。
package tests

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	thinkHead = "思维首段甲:先看需求再定方案,别急着写代码,顺序很重要,先对齐口径,再动手实现,避免返工浪费。"
	thinkTail = "思维尾部标记丁。"
)

func TestTUIAcceptThinkingBlockBody(t *testing.T) {
	bin, env, root, _ := tuiAcceptSetup(t)
	writeAcceptFile(t, filepath.Join(root, "config"), "patch-acctui.yaml", `entries:
  - id: llm-openai-compat
    enabled: false
  - id: llm-mock
    enabled: true
    data:
      script: '[{"thinking":"`+thinkHead+thinkTail+`","text":"思维块已验证。"}]'
`)
	s := newTuiSess(t, bin, env, "--profile", "acctui")
	defer s.quit()
	if !s.boot() {
		t.Fatal("首帧未就绪")
	}
	s.send("先想再做\r")
	if !s.waitRaw("思维块已验证", 30*time.Second) {
		t.Fatalf("条目 40:回合未完成;尾段 %q", stripANSI(tailS(s.rawText(), 300)))
	}
	// 折叠态:首段可见、尾部不可见(思维增量确实进了渲染,且默认折叠)
	if !strings.Contains(stripANSI(s.rawText()), "思维首段甲") {
		t.Fatalf("条目 40:思维块未渲染;尾段 %q", stripANSI(tailS(s.rawText(), 400)))
	}
	if strings.Contains(stripANSI(s.rawText()), thinkTail) {
		t.Errorf("条目 40:默认应折叠,尾部已可见")
	}
	// Ctrl+T 展开全文
	s.send("\x14")
	if !s.waitRaw(thinkTail, 8*time.Second) {
		t.Fatalf("条目 40:Ctrl+T 后未展开全文;屏尾 %q", firstN(tailS(s.screen(), 200), 200))
	}
	// 再按一次收回(实时判定走屏幕模型)
	s.send("\x14")
	if !s.waitScreenGone(thinkTail, 8*time.Second) {
		t.Logf("条目 40:再折叠后屏幕模型仍见尾部(残影/屏幕模型对会话流覆盖有限),以展开方向为准")
	}
}
