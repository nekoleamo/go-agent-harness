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
type Log struct {
	mu     sync.Mutex
	events []sdk.SessionEvent
	seq    atomic.Uint64
	file   *os.File
	path   string
	ctx    sdk.Ctx
}

func newLog(dir string) *Log {
	return &Log{path: dir}
}

// SetPath 设置落盘路径(项目级会话隔离在 host-cwd-sessions 中调用)。
func (l *Log) SetPath(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path = path
}

// Append 追加事件并落盘(jsonl;落盘失败仅记内存 + 返回错误,不丢事件)。
func (l *Log) Append(ev sdk.SessionEvent) error {
	ev.Seq = l.seq.Add(1)
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	l.mu.Lock()
	l.events = append(l.events, ev)
	f := l.file
	if err := l.ensureFileLocked(); err != nil && f == nil {
		l.mu.Unlock()
		return err
	}
	var line []byte
	var jerr error
	if l.file != nil {
		line, jerr = json.Marshal(ev)
		_, jerr = l.file.Write(append(line, '\n'))
	}
	l.mu.Unlock()
	if l.ctx != nil {
		l.ctx.Emit(context.Background(), sdk.EventSession, &ev, sdk.Emit)
	}
	return jerr
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
func (l *Log) DeriveMessages() []sdk.LLMMessage {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []sdk.LLMMessage
	for _, ev := range l.events {
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
		}
	}
	return out
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
