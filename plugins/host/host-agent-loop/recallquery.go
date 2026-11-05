// recallquery.go:抽出「本步该拿什么去检索记忆」(P0 自动调取)。
//
// 为什么单独一个文件:它有一条不该被埋进 step() 的判定逻辑 —— 斜杠命令不是自然语言
// 查询,拿它去检索只会命中一堆无关条目。而"取不到查询"必须是一个**合法状态**
// (片段走无上下文回退),不是错误。
package hostagentloop

import (
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// recallQueryMaxRunes 查询截断长度:长粘贴内容(日志/代码)不该把检索拖成全库扫描的主因,
// 也防止把整段代码当成查询词表。
const recallQueryMaxRunes = 512

// recallQuery 取最后一条用户消息作为检索查询;没有则空串。
//
// 斜杠命令(如 `/status`)不算查询:它由命令面消费,不进模型循环;判据是首段形如
// `/名字`(字母开头、不含空白)且整条不含换行 —— 宁可漏检索(回退到默认注入),
// 也不拿命令行去命中一堆无关记忆。
func recallQuery(history []sdk.LLMMessage) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != sdk.RoleUser {
			continue
		}
		t := strings.TrimSpace(history[i].Content)
		if t == "" {
			return ""
		}
		if isSlashCommand(t) {
			return ""
		}
		if r := []rune(t); len(r) > recallQueryMaxRunes {
			t = string(r[:recallQueryMaxRunes])
		}
		return t
	}
	return ""
}

// isSlashCommand 形如 `/status` 的单行命令(首段字母开头、无空白)。
func isSlashCommand(s string) bool {
	if !strings.HasPrefix(s, "/") || strings.ContainsRune(s, '\n') {
		return false
	}
	head := s
	if i := strings.IndexAny(s, " \t"); i >= 0 {
		head = s[:i]
	}
	if len(head) < 2 || len(head) > 24 {
		return false
	}
	for i, r := range head[1:] {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '-' || r == '_' || r == ':'):
		default:
			// 含 `/` 就不当命令(路径开头的自然语言,如 `/usr/local/bin 下的脚本`)。
			return false
		}
	}
	return true
}
