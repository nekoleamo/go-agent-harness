// Package redact 会话内容发给模型前的**凭据脱敏**(单一事实源)。
//
// 为什么抽出来:两个消费方都需要它 —— 会话概述(host-session-summary)与
// 滚动摘要的模型摘要(host-llm-summary)。两份实现必然漂移,而漂移的方向是
// "某个新出现的凭据形态在另一处没被脱敏" ⇒ 密钥进模型上下文。
//
// 形态:`sk-xxx`、`Bearer xxx`、`api_key=xxx`、`apiKey: xxx`、`"app_secret": "xxx"`、
// `AppSecret xxx` 等。键名/前缀保留(便于模型读懂上下文),值替换为 [REDACTED]。
package redact

import "strings"

// Text 脱敏:密钥类字面量只保留键名/前缀,值替换为 [REDACTED](线性扫描,零回溯)。
func Text(s string) string {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		hit, hitKey := -1, ""
		for _, key := range keys {
			if j := strings.Index(s[i:], key); j >= 0 {
				if hit < 0 || i+j < hit {
					hit, hitKey = i+j, key
				}
			}
		}
		if hit < 0 {
			sb.WriteString(s[i:])
			break
		}
		// 键名原样保留(便于读懂上下文),其后分隔符也保留
		sep := hit + len(hitKey)
		for sep < len(s) && isSepByte(s[sep]) {
			sep++
		}
		end := sep
		for end < len(s) && !isSpaceByte(s[end]) && !isDelimByte(s[end]) {
			end++
		}
		sb.WriteString(s[hit:sep])
		if end > sep {
			sb.WriteString("[REDACTED]")
		}
		i = end
		if end == sep {
			i = sep // 无值:跳过键与分隔符,防死循环
		}
	}
	return sb.String()
}

// keys 触发脱敏的字面量(键名/前缀;值随其后被替换)。
var keys = []string{"sk-", "Bearer ", "api_key=", "api_key:", "apiKey=", "apiKey:",
	"client_secret", "app_secret", "appSecret", "AppSecret", "password", "token=", "token:"}

// isSepByte 键与值之间的分隔符(空格/冒号/等号/引号)。
func isSepByte(b byte) bool {
	return isSpaceByte(b) || b == ':' || b == '=' || b == '"' || b == '\''
}

func isSpaceByte(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

func isDelimByte(b byte) bool {
	switch b {
	case '"', '\'', ',', ';', ')', ']', '}', '<', '>':
		return true
	}
	return false
}
