package tui

import (
	"strings"
	"testing"
)

// A-1 条目 31 回归:待确认提示必须进会话流 —— 视图层不渲染 PendingConfirm,
// 只置字段会让用户看不到"在确认什么"(pty 探针实测:屏上只有"执行工具: xxx")。
func TestApplyConfirmPromptIsVisibleInLines(t *testing.T) {
	s := &State{}
	const prompt = `确认执行工具调用 [file_write {"path":"a.txt"}]? y/n`
	s.ApplyConfirmPrompt(prompt)

	if s.PendingConfirm != prompt {
		t.Fatalf("PendingConfirm=%q,想要 %q", s.PendingConfirm, prompt)
	}
	var got string
	for _, ln := range s.Lines {
		if strings.Contains(ln.Text, "确认执行工具调用") {
			got = ln.Text
		}
	}
	if got == "" {
		t.Fatal("会话流里没有确认提示(用户看不到确认内容)")
	}
	if !strings.Contains(got, "y/n") {
		t.Errorf("提示缺 y/n 语义:%q", got)
	}

	// 答复后清空待确认态(幂等;y/n 均如此)
	if !s.ResolveConfirm(true) || s.PendingConfirm != "" {
		t.Errorf("ResolveConfirm 未清空待确认态:PendingConfirm=%q", s.PendingConfirm)
	}
}
