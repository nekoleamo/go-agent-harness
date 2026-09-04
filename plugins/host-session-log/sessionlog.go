// Package sessionlog 提供 host-session-log 插件:ctx.sessions 会话日志服务。
// 追加式事件流 + 模型历史投影(不变量:模型可见即已记录)。
// 落盘:MVP 默认内存(路径空);data.path 配置为完整 jsonl 文件路径时追加落盘;
// 项目级会话隔离(~/.gah/sessions/<project>.jsonl)由 M4 host-cwd-sessions 负责。
package sessionlog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-session-log。
type Plugin struct{}

// Name 返回插件 id。
func (p *Plugin) Name() string { return "host-session-log" }

// Start 注册 ctx.sessions 服务。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var path string
	budget := 0
	if m != nil && m.Data != nil {
		if d, ok := m.Data["path"].(string); ok && d != "" {
			path = d
		}
		// token_budget_chars(M6.5):投影字符预算,超限滚动摘要压缩;0 = 关闭
		if b, ok := m.Data["token_budget_chars"].(int); ok && b > 0 {
			budget = b
		}
	}
	lg := newLog(path)
	lg.budget = budget
	lg.ctx = c
	if err := c.Provide("ctx.sessions", lg); err != nil {
		return nil, err
	}
	return func() { lg.Close() }, nil
}

// Log 是会话日志实现。事件并发追加,jsonl 落盘(按项目 key 一个文件)。
// 每次 Append 后经 ctx 广播 session/event(供 UI/遥测实时订阅)。
// M6.5 滚动摘要:budget > 0 时投影超预算 → 最旧块压缩为 session/summary 摘要事件,
// 原始事件保留(留盘完整),投影见“累计摘要 + 最近块”。
type Log struct {
	mu              sync.Mutex
	events          []sdk.SessionEvent
	seq             atomic.Uint64
	file            *os.File
	path            string
	ctx             sdk.Ctx
	historyLimit    int // -1 禁止 / 0 全部 / N>0 最近 N 条
	budget          int // 投影字符预算(0 = 关闭摘要压缩)
	compressedUntil int // 已被摘要覆盖的 events 索引水位(-1 = 未压缩)
}

func newLog(dir string) *Log {
	return &Log{path: dir, compressedUntil: -1}
}

// SetPath 设置落盘路径(项目级会话隔离在 host-cwd-sessions 中调用)。
func (l *Log) SetPath(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path = path
}

// Append 追加事件并落盘(jsonl;落盘失败仅记内存 + 返回错误,不丢事件)。
func (l *Log) Append(ev sdk.SessionEvent) error {
	l.mu.Lock()
	err := l.appendLocked(ev)
	l.mu.Unlock()
	if err == nil && l.ctx != nil {
		l.ctx.Emit(context.Background(), sdk.EventSession, &ev, sdk.Emit)
	}
	return err
}

// appendLocked 锁内追加:事件入列 + 落盘(调用方持有 mu;滚动摘要等内部路径使用)。
func (l *Log) appendLocked(ev sdk.SessionEvent) error {
	ev.Seq = l.seq.Add(1)
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	l.events = append(l.events, ev)
	f := l.file
	if err := l.ensureFileLocked(); err != nil && f == nil {
		return err
	}
	if l.file != nil {
		line, jerr := json.Marshal(ev)
		if jerr == nil {
			_, jerr = l.file.Write(append(line, '\n'))
		}
		return jerr
	}
	return nil
}

func (l *Log) ensureFileLocked() error {
	if l.path == "" || l.file != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("sessionlog: mkdir: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("sessionlog: open %s: %w", l.path, err)
	}
	l.file = f
	return nil
}

// Close 关闭落盘文件。
func (l *Log) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

// Append 便捷:构造并追加。
func (l *Log) append(kind string, payload any) error {
	return l.Append(sdk.SessionEvent{Kind: kind, Payload: payload})
}

// DeriveMessages 从事件流投影模型历史(不变量来源)。
// M6.5:投影超字符预算时滚动压缩——最旧块折叠为累计摘要(session/summary 事件),
// 完整日志仍留盘(events 原样),投影见“摘要(system) + 最近块”。
func (l *Log) DeriveMessages() []sdk.LLMMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.historyLimit < 0 {
		return nil
	}
	var out []sdk.LLMMessage
	var summary string
	for i, ev := range l.events {
		if i <= l.compressedUntil && ev.Kind != sdk.EventSummary {
			continue // 已被滚动摘要覆盖的旧消息
		}
		switch ev.Kind {
		case sdk.EventSummary: // 累计摘要(载荷为字符串)
			if s, ok := ev.Payload.(string); ok && s != "" {
				summary = s
			}
		case sdk.EventUserMessage:
			if u, ok := ev.Payload.(sdk.UserMessage); ok {
				out = append(out, sdk.LLMMessage{Role: sdk.RoleUser, Content: u.Content})
			}
		case sdk.EventAssistantMessage:
			if a, ok := ev.Payload.(sdk.AssistantMessage); ok {
				out = append(out, sdk.LLMMessage{Role: sdk.RoleAssistant, Content: a.Content, ToolCalls: a.ToolCalls})
			}
		case sdk.EventToolResult:
			if r, ok := ev.Payload.(sdk.ToolResultEvent); ok {
				msg := sdk.LLMMessage{Role: sdk.RoleTool, ToolCallID: r.CallID, Content: r.Content}
				if r.Error != "" {
					msg.Content = "ERROR: " + r.Error
				}
				out = append(out, msg)
			}
		}
	}
	// history injection:-1 禁止(仅系统消息由组装层补充);N>0 保留最近 N 条;0 = 全部
	if l.historyLimit > 0 && len(out) > l.historyLimit {
		out = out[len(out)-l.historyLimit:]
	}
	// 累计摘要恒入投影(无论本次是否触发新压缩):摘要 + 最近块
	if summary != "" {
		out = append([]sdk.LLMMessage{{Role: sdk.RoleSystem, Content: "对先前对话的滚动摘要:\n" + summary}}, out...)
	}
	// 预算压缩:投影超限 → 滚动折叠最旧块(逐轮折叠至预算内)
	if l.budget > 0 && approxChars(out) > l.budget {
		out = l.compressLocked(out, summary)
	}
	return out
}

// compressLocked 滚动压缩:折叠水位后最旧块入累计摘要,直到投影回到预算内。
// 每轮折叠一块并落盘 session/summary 事件(追加式;不广播,UI 无需实时)。
func (l *Log) compressLocked(out []sdk.LLMMessage, summary string) []sdk.LLMMessage {
	for iter := 0; iter < maxCompressPerCall; iter++ {
		if len(out) == 0 || approxChars(out) <= l.budget {
			return out
		}
		need := approxChars(out) - l.budget/2 // 需腾出的字符量(压到约一半预算)
		if need < l.budget/3 {
			need = l.budget / 3
		}
		// 取水位后的原始事件块,累计到 need;块不得越过最后一个用户轮(保留最新轮)
		start := l.compressedUntil + 1
		lastUser := lastUserIndex(l.events)
		chars := 0
		end := start - 1
		for i := start; i < len(l.events) && i < lastUser; i++ {
			ev := l.events[i]
			if ev.Kind == sdk.EventSummary {
				continue
			}
			chars += approxEv(ev)
			end = i
			if chars >= need {
				break
			}
		}
		if end < start {
			return out // 无可压缩的普通消息
		}
		block := l.events[start : end+1]
		summary = mergeSummary(summary, summarizeEvents(block), l.budget)
		// 摘要事件落盘(锁内直接写,跳过广播)
		_ = l.appendLocked(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: summary})
		l.compressedUntil = end
		// 重建投影:累计摘要 + 未压缩块
		var rebuilt []sdk.LLMMessage
		if summary != "" {
			rebuilt = append(rebuilt, sdk.LLMMessage{Role: sdk.RoleSystem, Content: "对先前对话的滚动摘要:\n" + summary})
		}
		for i := end + 1; i < len(l.events); i++ {
			ev := l.events[i]
			switch ev.Kind {
			case sdk.EventUserMessage:
				if u, ok := ev.Payload.(sdk.UserMessage); ok {
					rebuilt = append(rebuilt, sdk.LLMMessage{Role: sdk.RoleUser, Content: u.Content})
				}
			case sdk.EventAssistantMessage:
				if a, ok := ev.Payload.(sdk.AssistantMessage); ok {
					rebuilt = append(rebuilt, sdk.LLMMessage{Role: sdk.RoleAssistant, Content: a.Content, ToolCalls: a.ToolCalls})
				}
			case sdk.EventToolResult:
				if r, ok := ev.Payload.(sdk.ToolResultEvent); ok {
					msg := sdk.LLMMessage{Role: sdk.RoleTool, ToolCallID: r.CallID, Content: r.Content}
					if r.Error != "" {
						msg.Content = "ERROR: " + r.Error
					}
					rebuilt = append(rebuilt, msg)
				}
			}
		}
		out = rebuilt
	}
	return out
}

// maxCompressPerCall 单次投影单次调用的最大折叠数(防极端场景死循环)。
const maxCompressPerCall = 20

// approximateChars 消息序列字符估算(中文 1 字 ≈ 1 token 的粗代理)。
func approxChars(msgs []sdk.LLMMessage) int {
	n := 0
	for _, m := range msgs {
		n += len([]rune(m.Content))
		for _, c := range m.ToolCalls {
			n += len([]rune(c.Name)) + len([]rune(c.Arguments))
		}
	}
	return n
}

// approxEv 单个事件的字符估算。
func approxEv(ev sdk.SessionEvent) int {
	switch p := ev.Payload.(type) {
	case sdk.UserMessage:
		return len([]rune(p.Content))
	case sdk.AssistantMessage:
		return len([]rune(p.Content))
	case sdk.ToolResultEvent:
		return len([]rune(p.Content))
	default:
		return 0
	}
}

// summarizeEvents 块 → 抽取式摘要(用户问/助手答/工具结果,逐条截断)。
func summarizeEvents(evs []sdk.SessionEvent) string {
	var b strings.Builder
	for _, ev := range evs {
		switch p := ev.Payload.(type) {
		case sdk.UserMessage:
			b.WriteString("- 用户: " + clip(p.Content, 120) + "\n")
		case sdk.AssistantMessage:
			if p.Content != "" {
				b.WriteString("- 助手: " + clip(p.Content, 200) + "\n")
			}
		case sdk.ToolResultEvent:
			if p.Name != "" {
				b.WriteString("- 工具 " + p.Name + ": " + clip(p.Content, 120) + "\n")
			}
		}
	}
	return b.String()
}

// lastUserIndex 最后一个用户消息索引(最新轮起点,压缩块不得越过;无则 -1)。
func lastUserIndex(evs []sdk.SessionEvent) int {
	last := -1
	for i, ev := range evs {
		if ev.Kind == sdk.EventUserMessage {
			last = i
		}
	}
	return last
}

// mergeSummary 累计摘要:旧 + 新段,限长(预算一半,防摘要自身膨胀)。
func mergeSummary(old, seg string, budget int) string {
	merged := old + seg
	if budget > 0 && len([]rune(merged)) > budget/2 {
		merged = clip(merged, budget/2)
	}
	return merged
}

// clip 按 rune 截断并加省略号。
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// SetHistory 设置历史注入条数。-1 禁止;0 全部;N>0 最近 N 条。
func (l *Log) SetHistory(n int) {
	l.mu.Lock()
	l.historyLimit = n
	l.mu.Unlock()
}

// Replay 全量回放。
func (l *Log) Replay() []sdk.SessionEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]sdk.SessionEvent(nil), l.events...)
}

// Flush 落盘(os.Sync)。
func (l *Log) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		return l.file.Sync()
	}
	return nil
}
