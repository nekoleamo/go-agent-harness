package hostagentloop

// recallQuery 的行为契约:它决定"每步拿什么去检索记忆"。
// 错的方向有两种:把斜杠命令当查询(命一堆无关记忆),或把长粘贴整段当查询(检索失真)。

import (
	"strings"
	"testing"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// u/a 构造用户/助手消息(LLMMessage.Role 是 sdk.Role 类型,不能传裸字符串)。
func u(content string) sdk.LLMMessage {
	return sdk.LLMMessage{Role: sdk.RoleUser, Content: content}
}

func a(content string) sdk.LLMMessage {
	return sdk.LLMMessage{Role: sdk.RoleAssistant, Content: content}
}

func TestRecallQueryLastUserMessage(t *testing.T) {
	h := []sdk.LLMMessage{u("先看看项目结构"), a("好的"), u("  部署脚本放哪了  ")}
	if got := recallQuery(h); got != "部署脚本放哪了" {
		t.Fatalf("应取最后一条用户消息并 trim,got %q", got)
	}
}

func TestRecallQuerySkipsNonUserAndEmpty(t *testing.T) {
	if got := recallQuery([]sdk.LLMMessage{a("只有助手消息")}); got != "" {
		t.Fatalf("没有用户消息应为空,got %q", got)
	}
	if got := recallQuery([]sdk.LLMMessage{u("   ")}); got != "" {
		t.Fatalf("空白用户消息应为空,got %q", got)
	}
}

func TestRecallQuerySlashCommandIsNotAQuery(t *testing.T) {
	for _, in := range []string{"/status", "/memory list", "/new", "/skill:code-review 看看"} {
		if got := recallQuery([]sdk.LLMMessage{u(in)}); got != "" {
			t.Fatalf("斜杠命令不该被当成检索查询: %q ⇒ %q", in, got)
		}
	}
}

func TestRecallQueryPathIsNotACommand(t *testing.T) {
	in := "/usr/local/bin 下的脚本要能跑"
	if got := recallQuery([]sdk.LLMMessage{u(in)}); got != in {
		t.Fatalf("路径开头的自然语言不是命令,应保留为查询,got %q", got)
	}
}

func TestRecallQueryTruncatesLongInput(t *testing.T) {
	long := strings.Repeat("日志行\n", 500)
	got := recallQuery([]sdk.LLMMessage{u(long)})
	if n := len([]rune(got)); n > recallQueryMaxRunes {
		t.Fatalf("查询应截断到 %d rune,got %d", recallQueryMaxRunes, n)
	}
	if n := len([]rune(got)); n < recallQueryMaxRunes {
		t.Fatalf("超长输入应保留前 %d rune,got %d", recallQueryMaxRunes, n)
	}
}
