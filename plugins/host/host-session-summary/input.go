// 概述输入构造(F3 §5.2):从会话 jsonl 裁剪出「首 N 轮 + 最近 M 轮」的文本,总预算 8KB。
//
// 纪律:
//   - 只取 user/assistant 文本;工具调用只保留工具名(不喂参数/结果,避免泄漏与噪声);
//   - 先过脱敏(剥离常见密钥字面量),再进模型;
//   - 超预算按"首 + 尾"取舍,并在输入尾部注明省略了多少条(不静默)。
package hostsummary

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// turn 一条裁剪后的会话轮次文本。
type turn struct {
	role string
	text string
}

// buildInput 读会话 jsonl 构造概述输入(返回输入文本 + 用户轮数)。
func buildInput(path string, o Options) (string, int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", 0, fmt.Errorf("会话文件不存在: %s", path)
		}
		return "", 0, err
	}
	defer f.Close()

	var turns []turn
	users := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev struct {
			Kind    string          `json:"Kind"`
			Payload json.RawMessage `json:"Payload"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue // 坏行容忍(同 sessionlog)
		}
		switch ev.Kind {
		case sdk.EventUserMessage:
			var m sdk.UserMessage
			if json.Unmarshal(ev.Payload, &m) != nil {
				continue
			}
			if t := strings.TrimSpace(m.Content); t != "" {
				turns = append(turns, turn{role: "用户", text: redact(t)})
				users++
			}
		case sdk.EventAssistantMessage:
			var m sdk.AssistantMessage
			if json.Unmarshal(ev.Payload, &m) != nil {
				continue
			}
			text := strings.TrimSpace(m.Content)
			names := make([]string, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				if tc.Name != "" {
					names = append(names, tc.Name)
				}
			}
			if len(names) > 0 {
				call := "[工具调用:" + strings.Join(names, ",") + "]"
				if text == "" {
					text = call
				} else {
					text += " " + call
				}
			}
			if text != "" {
				turns = append(turns, turn{role: "助手", text: redact(text)})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", users, err
	}
	if len(turns) == 0 {
		return "", users, fmt.Errorf("会话无可用文本")
	}

	keep := selectTurns(turns, o)
	var sb strings.Builder
	skipped := len(turns) - len(keep)
	for _, t := range keep {
		fmt.Fprintf(&sb, "%s:%s\n", t.role, t.text)
	}
	if skipped > 0 {
		fmt.Fprintf(&sb, "(中间省略 %d 条消息)\n", skipped)
	}
	input := sb.String()
	if len(input) > o.InputBudget {
		input = truncBytesRunes(input, o.InputBudget)
		input += "\n(输入已按字节预算截断)\n"
	}
	return input, users, nil
}

// selectTurns 首 FirstTurns + 最近 LastTurns(按 1 条 = 1 项;顺序保持时间序)。
func selectTurns(turns []turn, o Options) []turn {
	n := len(turns)
	if n <= o.FirstTurns+o.LastTurns {
		return turns
	}
	out := make([]turn, 0, o.FirstTurns+o.LastTurns)
	out = append(out, turns[:o.FirstTurns]...)
	out = append(out, turns[n-o.LastTurns:]...)
	return out
}

// userTurns 会话用户轮数(自动档触发前置判断;轻量)。
func userTurns(path string) int {
	_, n, err := buildInput(path, Options{FirstTurns: 0, LastTurns: 0, InputBudget: 1})
	if err != nil && n == 0 {
		return 0
	}
	return n
}

// redact 脱敏:密钥类字面量只保留键名/前缀,值替换为 [REDACTED](线性扫描,零回溯)。
// 支持的形态:`sk-xxx`、`Bearer xxx`、`api_key=xxx`、`apiKey: xxx`、`"app_secret": "xxx"`、`AppSecret xxx`。
// 目的:会话内容发给模型做概述前,凭据不进模型上下文。
func redact(s string) string {
	var sb strings.Builder
	i := 0
	for i < len(s) {
		hit, hitKey := -1, ""
		for _, key := range redactKeys {
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

// redactKeys 触发脱敏的字面量(键名/前缀;值随其后被替换)。
var redactKeys = []string{"sk-", "Bearer ", "api_key=", "api_key:", "apiKey=", "apiKey:",
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

// truncBytesRunes 按字节预算截断(不切碎 UTF-8;退回最近 rune 边界)。
func truncBytesRunes(s string, budget int) string {
	if len(s) <= budget {
		return s
	}
	b := []byte(s)[:budget]
	for len(b) > 0 {
		if r := b[len(b)-1]; r < 0x80 || r >= 0xC0 { // ASCII 或 rune 首字节
			return string(b)
		}
		b = b[:len(b)-1]
	}
	return ""
}
