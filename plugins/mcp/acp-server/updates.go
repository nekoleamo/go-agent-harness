// updates.go:会话账本事件 → ACP session/update 映射(服务端唯一的呈现出口)。
//
// 纪律:只翻译账本里已有的事实,不发明新状态 —— 事件来自 ctx.sessions.Append 的广播
// (带 Seq/TS,与 TUI/Web 同一份事实),因此编辑器侧看到的进度与 TUI/Web 必然一致。
// 回合外的事件一律不推(编辑器只关心当前回合;历史由它自己维护)。
package acpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// toolResultMaxChars 工具结果回传预算:编辑器渲染的是过程提示,不需要整个结果;
// 截断显式标注(仍保留结果确有内容这一事实)。
const toolResultMaxChars = 6000

// onSessionEvent 会话事件订阅入口(Append 广播 → session/update)。
func (s *server) onSessionEvent(_ context.Context, ev *sdk.Event) error {
	t := s.currentTurn()
	if t == nil {
		return nil // 无在跑回合:不推(回合外的工具/job 事件不属于任何 ACP 回合)
	}
	se := sessionEventOf(ev.Payload)
	if se == nil {
		return nil
	}
	switch se.Kind {
	case sdk.EventStepStart:
		// 一步 = 一次模型请求 = 一段助手消息(ACP 的可选 messageId 据此归组)
		t.nextMessage()
	case sdk.EventAssistantChunk:
		if c, ok := payloadOf[sdk.LLMStreamEvent](se.Payload); ok {
			if c.Thinking != "" {
				s.emitText(t, c.Thinking, true)
			}
			if c.Delta != "" {
				t.markChunked()
				s.emitText(t, c.Delta, false)
			}
		}
	case sdk.EventAssistantMessage:
		// 兜底:非流式适配器(或本步没流过增量)时用完整消息补一段,
		// 否则编辑器里这一轮会"没有回复"(模型可见即已记录,呈现也不能缺)。
		if m, ok := payloadOf[sdk.AssistantMessage](se.Payload); ok {
			if !t.currentChunked() && strings.TrimSpace(m.Content) != "" {
				s.emitText(t, m.Content, false)
			}
		}
	case sdk.EventToolCall:
		if c, ok := payloadOf[sdk.ToolCallEvent](se.Payload); ok {
			s.emitToolCall(t, c)
		}
	case sdk.EventToolResult:
		if r, ok := payloadOf[sdk.ToolResultEvent](se.Payload); ok {
			s.emitToolResult(t, r)
		}
	case sdk.EventFileChange:
		// 写盘审计(带 unified diff):挂到对应工具调用上,让编辑器能就地看到改了什么。
		// ACP 的 diff 内容块要求 oldText/newText 全文,gah 侧只捕获 patch(不重读文件、
		// 不伪造未改动的旧内容)→ 以文本形式挂 patch,真实但少一档富呈现。
		if fc, ok := payloadOf[sdk.FileChangeEvent](se.Payload); ok {
			s.emitFileChange(t, fc)
		}
	case sdk.EventUsage:
		if u, ok := payloadOf[sdk.UsageEvent](se.Payload); ok {
			s.emitUsage(t, u)
		}
	}
	return nil
}

// emitText 推一段助手文本(thought=true → agent_thought_chunk)。
func (s *server) emitText(t *turn, text string, thought bool) {
	kind := "agent_message_chunk"
	if thought {
		kind = "agent_thought_chunk"
	}
	s.notify("session/update", sessionUpdateParams{
		SessionID: t.sess.id,
		Update: chunkUpdate{
			SessionUpdate: kind,
			Content:       textBlock{Type: "text", Text: text},
			MessageID:     t.messageID(),
		},
	})
}

// emitToolCall 工具调用首报(tool_call):标题/类别/路径来自工具名与参数。
func (s *server) emitToolCall(t *turn, c sdk.ToolCallEvent) {
	title, kind, path := toolMeta(c.Name, c.Arguments)
	t.setCalls(c.ID, c.Name, path)
	upd := toolUpdate{
		SessionUpdate: "tool_call",
		ToolCallID:    c.ID,
		Title:         title,
		Name:          c.Name,
		Kind:          kind,
		Status:        "in_progress",
	}
	if path != "" {
		upd.Locations = []toolLocation{{Path: path}}
	}
	if raw := rawArgs(c.Arguments); raw != nil {
		upd.RawInput = raw
	}
	s.notify("session/update", sessionUpdateParams{SessionID: t.sess.id, Update: upd})
}

// emitToolResult 工具终态(tool_call_update):completed / failed。
func (s *server) emitToolResult(t *turn, r sdk.ToolResultEvent) {
	if _, known := t.resolveCall(r.CallID); !known {
		return // 非本回合发起的调用(或已终态):不推重复 update
	}
	body := r.Content
	if r.Error != "" {
		body = r.Error
	}
	status := "completed"
	if strings.TrimSpace(r.Error) != "" {
		status = "failed"
	}
	s.notify("session/update", sessionUpdateParams{
		SessionID: t.sess.id,
		Update: toolUpdate{
			SessionUpdate: "tool_call_update",
			ToolCallID:    r.CallID,
			Status:        status,
			Content:       []any{contentItem{Type: "content", Content: textBlock{Type: "text", Text: truncate(body, toolResultMaxChars)}}},
		},
	})
}

// emitFileChange 把一次写盘改动挂到对应工具调用(tool_call_update 的文本内容 + 定位)。
func (s *server) emitFileChange(t *turn, fc sdk.FileChangeEvent) {
	id := t.callForPath(fc.Path)
	if id == "" {
		return // 找不到归属(非工具写盘/已结束):不硬塞给别人
	}
	text := fileChangeText(fc)
	s.notify("session/update", sessionUpdateParams{
		SessionID: t.sess.id,
		Update: toolUpdate{
			SessionUpdate: "tool_call_update",
			ToolCallID:    id,
			Status:        "in_progress",
			Content:       []any{contentItem{Type: "content", Content: textBlock{Type: "text", Text: text}}},
			Locations:     []toolLocation{{Path: fc.Path}},
		},
	})
}

// fileChangeText 改动的单行摘要 + patch(无 patch 时只报统计:二进制/超大改动)。
func fileChangeText(fc sdk.FileChangeEvent) string {
	op := fc.Op
	if fc.Created {
		op += "(新建)"
	}
	head := fmt.Sprintf("%s %s +%d -%d", op, oneLine(filepath.Base(fc.Path), 120), fc.Added, fc.Removed)
	if fc.Binary {
		return head + "(二进制,无逐行 diff)"
	}
	if strings.TrimSpace(fc.Diff) == "" {
		return head
	}
	out := head + "\n" + fc.Diff
	if fc.Truncated {
		out += "\n… (patch 超预算已截断)"
	}
	return truncate(out, toolResultMaxChars)
}

// emitUsage 上报上下文占用(used = 最近一次请求的输入 token,size = 窗口)。
func (s *server) emitUsage(t *turn, u sdk.UsageEvent) {
	size := 0
	var stats sdk.UsageStatsService
	if err := s.c.Inject("ctx.usageStats", &stats); err == nil && stats != nil {
		size = stats.Stats().Window
	}
	if u.Usage.PromptTokens <= 0 || size <= 0 {
		return // 无窗口信息时不报:used/size 都是规范必填且不得为 null,编造数值比不报更坏
	}
	s.notify("session/update", sessionUpdateParams{
		SessionID: t.sess.id,
		Update:    usageUpdate{SessionUpdate: "usage_update", Used: u.Usage.PromptTokens, Size: size},
	})
}

// —— 纯函数:工具名/参数 → 呈现元信息(编辑器标题栏与图标) ——

// toolMeta 由工具名与参数给出标题、类别与目标路径。
// 工具名不在这张表里的一律 other + 原名(不猜语义:MCP 等外部工具的名字无从判断)。
func toolMeta(name, args string) (title, kind, path string) {
	var a map[string]any
	_ = json.Unmarshal([]byte(args), &a)
	str := func(k string) string {
		if v, ok := a[k].(string); ok {
			return v
		}
		return ""
	}
	switch name {
	case "file_read":
		path = str("path")
		return "读取 " + oneLine(filepath.Base(path), 120), "read", path
	case "file_write":
		path = str("path")
		return "写入 " + oneLine(filepath.Base(path), 120), "edit", path
	case "file_append":
		path = str("path")
		return "追加 " + oneLine(filepath.Base(path), 120), "edit", path
	case "file_edit":
		path = str("path")
		return "编辑 " + oneLine(filepath.Base(path), 120), "edit", path
	case "shell":
		return "执行 " + oneLine(str("command"), 160), "execute", ""
	case "web_search":
		return "搜索 " + oneLine(str("query"), 120), "search", ""
	case "web_fetch":
		return "抓取 " + oneLine(str("url"), 120), "fetch", ""
	case "read_document":
		path = str("path")
		return "阅读文档 " + oneLine(filepath.Base(path), 120), "read", path
	case "doc_open", "doc_list":
		path = str("path")
		return "预览文档 " + oneLine(filepath.Base(path), 120), "read", path
	case "ask_user_question":
		return "提问 " + oneLine(str("question"), 120), "other", ""
	case "subagent":
		return "子代理 " + oneLine(str("task"), 120), "think", ""
	case "todo":
		return "待办 " + oneLine(str("subject"), 120), "other", ""
	default:
		return oneLine(name+" "+args, 160), "other", ""
	}
}

// rawArgs 解析 rawInput(解析失败返回 nil:不把半截 JSON 当结构化输入送出去)。
func rawArgs(args string) any {
	if strings.TrimSpace(args) == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(args), &v); err != nil {
		return nil
	}
	return v
}

// sessionEventOf 从事件载荷取 SessionEvent(值/指针兼容)。
func sessionEventOf(payload any) *sdk.SessionEvent {
	switch p := payload.(type) {
	case *sdk.SessionEvent:
		return p
	case sdk.SessionEvent:
		return &p
	}
	return nil
}

// payloadOf 事件载荷取值/指针兼容转换。
func payloadOf[T any](payload any) (T, bool) {
	switch p := payload.(type) {
	case T:
		return p, true
	case *T:
		if p != nil {
			return *p, true
		}
	}
	var zero T
	return zero, false
}

// truncate 超预算截断并显式标注。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n… (超 %d 字节已截断)", max)
}
