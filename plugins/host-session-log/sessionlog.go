// Package sessionlog 提供 host-session-log 插件:ctx.sessions 会话日志服务。
// 追加式事件流 + 模型历史投影(不变量:模型可见即已记录)。
// 落盘:MVP 默认内存(路径空);data.path 配置为完整 jsonl 文件路径时追加落盘;
// 项目级会话隔离(~/.gah/sessions/<project>.jsonl)由 M4 host-cwd-sessions 负责。
// 职责边界(M6.5 拆分):本插件只做持久化与投影;token 滚动摘要压缩由 token-compress
// 插件经 RegisterCompressor 注入(投影超预算时回调 SessionCompressor.Fold,摘要事件落盘)。
package sessionlog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nekoleamo/go-agent-harness/sdk"
)

// Plugin 实现 host-session-log。
type Plugin struct{}

// Name 返回插件 id。
func (p *Plugin) Name() string { return "host-session-log" }

// Start 注册 ctx.sessions 服务。data.path 指定 jsonl 落盘路径(空 = 内存)。
func (p *Plugin) Start(c sdk.Ctx, m *sdk.Manifest) (sdk.Disposer, error) {
	var path string
	if m != nil && m.Data != nil {
		if d, ok := m.Data["path"].(string); ok && d != "" {
			path = d
		}
	}
	lg := newLog(path)
	lg.ctx = c
	if err := c.Provide("ctx.sessions", lg); err != nil {
		return nil, err
	}
	return func() { lg.Close() }, nil
}

// Log 是会话日志实现。事件并发追加,jsonl 落盘(按项目 key 一个文件)。
// 每次 Append 后经 ctx 广播 session/event(供 UI/遥测实时订阅)。
// 滚动摘要(token-compress):预算与压缩器经 RegisterCompressor 注册;
// 投影超预算时回调压缩器折叠最旧块为 session/summary 摘要事件,
// 原始事件保留(留盘完整),投影见“累计摘要 + 最近块”。
type Log struct {
	mu              sync.Mutex
	events          []sdk.SessionEvent
	seq             atomic.Uint64
	file            *os.File
	path            string
	ctx             sdk.Ctx
	historyLimit    int // -1 禁止 / 0 全部 / N>0 最近 N 条
	budget          int // 投影字符预算(0 = 关闭压缩;由 token-compress 注册时设置)
	compressor      sdk.SessionCompressor
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

// RegisterCompressor 注册滚动摘要压缩器与预算(token-compress 注入;budget<=0 关闭)。
func (l *Log) RegisterCompressor(budget int, c sdk.SessionCompressor) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.budget = budget
	l.compressor = c
	l.compressedUntil = -1
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

// appendLocked 锁内追加:事件入列 + 落盘(调用方持有 mu;summary 落盘等内部路径使用)。
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
// 语义:历史注入(historyLimit)→ 累计摘要置顶 + 最近块 → 超预算时经压缩器折叠最旧块。
// 完整日志仍留盘(events 原样),投影见“摘要(system) + 最近块”。
func (l *Log) DeriveMessages() []sdk.LLMMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.deriveLocked()
}

// deriveLocked 锁内投影(调用方持有 mu)。
func (l *Log) deriveLocked() []sdk.LLMMessage {
	if l.historyLimit < 0 {
		return nil
	}
	out := l.projectLocked(l.compressedUntil)
	// history injection:-1 禁止(仅系统消息由组装层补充);N>0 保留最近 N 条;0 = 全部
	if l.historyLimit > 0 && len(out) > l.historyLimit {
		out = out[len(out)-l.historyLimit:]
	}
	// 预算压缩:投影超限且已注册压缩器 → 滚动折叠最旧块,并以新水位重建投影
	if l.budget > 0 && l.compressor != nil && approxChars(out) > l.budget {
		l.compressedUntil = l.compressor.Fold(l.events, l.compressedUntil, l.budget,
			func(s string) { _ = l.appendLocked(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: s}) })
		out = l.projectLocked(l.compressedUntil)
	}
	return out
}

// projectLocked 从事件流重建投影:累计摘要(system)置顶 + 水位后的未压缩块。
// summary 从 events 中最新 EventSummary 读取(压缩器落盘的水位内摘要)。
func (l *Log) projectLocked(watermark int) []sdk.LLMMessage {
	summary := ""
	// 全流扫描取最新 EventSummary(摘要事件总是追加在末尾,水位外)
	for _, ev := range l.events {
		if ev.Kind == sdk.EventSummary {
			if s, ok := ev.Payload.(string); ok && s != "" {
				summary = s
			}
		}
	}
	var out []sdk.LLMMessage
	for i := watermark + 1; i < len(l.events); i++ {
		ev := l.events[i]
		switch ev.Kind {
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
		case sdk.EventSummary:
			// 水位后的摘要事件(通常不存在;防御性跳过)
		}
	}
	// 累计摘要恒入投影(摘要 + 最近块)
	if summary != "" {
		out = append([]sdk.LLMMessage{{Role: sdk.RoleSystem, Content: "对先前对话的滚动摘要:\n" + summary}}, out...)
	}
	return out
}

// approxChars 消息序列字符估算(中文 1 字 ≈ 1 token 的粗代理;与压缩器估算口径一致)。
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

// SetHistory 设置历史注入条数。-1 禁止;0 全部;N>0 最近 N 条。
func (l *Log) SetHistory(n int) {
	l.mu.Lock()
	l.historyLimit = n
	l.mu.Unlock()
}

// Replay 全量回放(完整日志留盘,含摘要事件)。
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
