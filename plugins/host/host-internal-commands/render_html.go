// B2 /export html:会话事件流渲染为自包含 HTML(内嵌 CSS,零外部依赖;对齐 pi /export HTML)。
// 增量 fidelity:assistant 块由 AssistantChunk delta 逐段拼合(thinking 灰块独立),
// AssistantMessage 最终消息跳过(防重复);工具调用/结果成块;user/error/turn 分类。
package hostintcmd

import (
	"html"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// renderSessionHTML 事件流 → 自包含 HTML 文档。
func renderSessionHTML(evs []sdk.SessionEvent) string {
	var b strings.Builder
	b.WriteString("<!DOCTYPE html>\n<html lang=\"zh\"><head><meta charset=\"utf-8\"><title>会话导出</title><style>\n")
	b.WriteString(cssExport)
	b.WriteString("\n</style></head><body><main>\n")

	inChunks := false // 正处 assistant chunk 增量段(闭合块用)
	flush := func() {
		if inChunks {
			b.WriteString("</div>\n")
			inChunks = false
		}
	}
	for _, ev := range evs {
		switch ev.Kind {
		case sdk.EventUserMessage:
			if u, ok := ev.Payload.(sdk.UserMessage); ok {
				flush()
				b.WriteString("<div class=\"msg user\"><span class=\"tag\">用户</span> " + html.EscapeString(u.Content) + "</div>\n")
			}
		case sdk.EventAssistantChunk:
			if cev, ok := ev.Payload.(sdk.LLMStreamEvent); ok {
				if cev.Thinking != "" {
					flush()
					b.WriteString("<div class=\"msg think\"><span class=\"tag\">思考</span> " + html.EscapeString(cev.Thinking) + "</div>\n")
				} else if cev.Delta != "" {
					if !inChunks {
						b.WriteString("<div class=\"msg assistant\">" + html.EscapeString(cev.Delta))
						inChunks = true
					} else {
						b.WriteString(html.EscapeString(cev.Delta))
					}
				}
			}
		case sdk.EventAssistantMessage:
			if a, ok := ev.Payload.(sdk.AssistantMessage); ok && !inChunks && a.Content != "" {
				b.WriteString("<div class=\"msg assistant\">" + html.EscapeString(a.Content) + "</div>\n")
			}
		case sdk.EventToolCall:
			if tc, ok := ev.Payload.(sdk.ToolCallEvent); ok {
				flush()
				b.WriteString("<div class=\"msg tool\"><span class=\"tag\">工具</span><span class=\"tname\">" + html.EscapeString(tc.Name) + "</span> <code>" + html.EscapeString(tc.Arguments) + "</code></div>\n")
			}
		case sdk.EventToolResult:
			if r, ok := ev.Payload.(sdk.ToolResultEvent); ok {
				flush()
				cls, label := "tool ok", "✓"
				body := r.Content
				if r.Error != "" {
					cls, label, body = "tool err", "✗", r.Error
				}
				b.WriteString("<div class=\"msg " + cls + "\"><span class=\"tag\">" + label + " " + html.EscapeString(r.Name) + "</span> <pre>" + html.EscapeString(body) + "</pre></div>\n")
			}
		case sdk.EventAgentError:
			if e, ok := ev.Payload.(error); ok {
				flush()
				b.WriteString("<div class=\"msg err\">" + html.EscapeString(e.Error()) + "</div>\n")
			}
		case sdk.EventTurnEnd:
			flush()
			b.WriteString("<hr>\n")
		}
	}
	flush()
	b.WriteString("</main></body></html>\n")
	return b.String()
}

// cssExport 自包含样式(gruvbox 系底色,与 TUI 默认表同族)。
const cssExport = `body{background:#1d2021;color:#ebdbb2;font:14px/1.7 -apple-system,"PingFang SC",sans-serif;margin:32px auto;max-width:860px;padding:0 16px} .tag{background:#3c3836;color:#d5c4a1;border-radius:4px;font-size:12px;padding:1px 8px;margin-right:8px} .msg{margin:10px 0;padding:10px 14px;border-radius:8px} .user{background:#282828} .assistant{background:#1d2021;color:#ebdbb2;white-space:pre-wrap;word-break:break-word} .think{background:#26221c;color:#928374;font-style:italic;white-space:pre-wrap} .tool{background:#282828;color:#d5c4a1} .tool .tname{color:#8ec07c;font-weight:600} .tool pre{background:#1d2021;white-space:pre-wrap;font:12px/1.6 ui-monospace,Menlo,monospace;padding:8px;border-radius:6px} .tool.ok .tag{color:#b8bb26} .tool.err .tag{color:#fb4934} .err{color:#fb4934;background:#3c2828} hr{border:none;border-top:1px dashed #665c54;margin:16px 0} code{font:12px ui-monospace,Menlo,monospace;color:#8ec07c}`
