// Package sessionlog 提供 host-session-log 插件:ctx.sessions 会话日志服务。
// 追加式事件流 + 模型历史投影(不变量:模型可见即已记录)。
// 落盘:MVP 默认内存(路径空);data.path 配置为完整 jsonl 文件路径时追加落盘;
// 项目级会话隔离(~/.gah/sessions/<project>.jsonl)由 M4 host-cwd-sessions 负责。
// 职责边界(M6.5 拆分):本插件只做持久化与投影;token 滚动摘要压缩由 token-compress
// 插件经 RegisterCompressor 注入(投影超预算时回调 SessionCompressor.Fold,摘要事件落盘)。
package sessionlog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

// SetPath 设置落盘路径(项目级会话隔离在 host-cwd-sessions 中调用;
// 仅改路径不加载 — 恢复历史走 Load,两者分离避免启动期误清内存)。
func (l *Log) SetPath(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path = path
}

// Load 切换到指定会话:关闭当前落盘文件、清空内存事件、读入 path 的 jsonl
// 已有事件(容忍坏行)并恢复序号(seq 接续;文件缺失/空 = 全新会话)。
// path 空 = 纯内存会话。切换会话时由 host-cwd-sessions 调用。
func (l *Log) Load(path string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	l.path = path
	l.events = nil
	l.compressedUntil = -1
	// 恢复持久化历史注入条数(重启/切换会话后一致;无 sidecar → 0 = 全部)
	l.historyLimit = loadHistory(path)
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 新会话:无历史,继续从头记
		}
		return fmt.Errorf("sessionlog: load open %s: %w", path, err)
	}
	defer f.Close()
	var maxSeq uint64
	// 单行上限 16MB:web_fetch 1MB 正文经 JSON 转义膨胀可到 ~2MB(实测 1.9MB),
	// 旧上限 1MB 会让有效事件行触发 token too long 拖垮整个 boot——提高余量;
	// 仍超限(>16MB 的极端单行)按坏行容忍跳过,不阻断历史恢复。
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev sdk.SessionEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // 容忍坏行(旧格式/半写):不中断恢复
		}
		// Payload 还原:json 反序列化后 Payload 是 map[string]any,按 Kind 二次还原为
		// 具体类型(展示层 ApplySessionEvent/投影 DeriveMessages 全靠类型断言;
		// 不还原则重放 11179 事件投影 0 行——TUI 启动/切换会话历史不可见)。
		ev.Payload = normalizePayload(&ev)
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
		l.events = append(l.events, ev)
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			// 极端单行(>16MB):丢弃该行及其后(Scanner 终止),恢复已读部分——
			// 会话为追加日志,不阻断 boot;seq 接续到超限行前最后正常事件。
			l.seq.Store(maxSeq)
			return nil
		}
		return fmt.Errorf("sessionlog: load read %s: %w", path, err)
	}
	// 序号从历史顶续接(新事件 Seq 不复用)
	l.seq.Store(maxSeq)
	return nil
}

// normalizePayload 把 json 反序列化后的 map Payload 按 Kind 还原为具体类型
// (与实时 Append 的对象同型(值类型);未识别 Kind 原样保留 map)。
func normalizePayload(ev *sdk.SessionEvent) any {
	m, ok := ev.Payload.(map[string]any)
	if !ok {
		return ev.Payload // 非 map(空/string):原样
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return m
	}
	switch ev.Kind {
	case sdk.EventUserMessage:
		var v sdk.UserMessage
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case sdk.EventAssistantChunk:
		var v sdk.LLMStreamEvent
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case sdk.EventAssistantMessage:
		var v sdk.AssistantMessage
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case sdk.EventToolCall:
		var v sdk.ToolCallEvent
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	case sdk.EventToolResult:
		var v sdk.ToolResultEvent
		if err := json.Unmarshal(raw, &v); err == nil {
			return v
		}
	}
	return m // 未识别/二次解析失败:保留 map(消费者自行容忍)
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
				out = append(out, sdk.LLMMessage{Role: sdk.RoleUser, Content: u.Content, Attachments: u.Attachments})
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
// 持久化到会话目录 sidecar(与当前 path 同目录、名 .history;重启/切换会话后 Load 恢复)。
func (l *Log) SetHistory(n int) {
	l.mu.Lock()
	l.historyLimit = n
	p := l.path
	l.mu.Unlock()
	l.persistHistory(p, n)
}

// historySidecar 历史设置 sidecar 路径(会话 path 同目录、同 basename + .history)。
// 项目级共享(同项目多会话同设置);path 空 = 内存会话不落盘。
func historySidecar(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(path), filepath.Base(path)+".history")
}

// persistHistory 原子写 sidecar(忽略失败:内存语义仍生效,静默降级)。
func (l *Log) persistHistory(path string, n int) {
	p := historySidecar(path)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	if err := os.WriteFile(p, []byte(fmt.Sprint(n)), 0o644); err != nil {
		return
	}
}

// loadHistory 从 sidecar 读历史条数(缺/坏 → 0 = 全部;路径空 → 0)。
func loadHistory(path string) int {
	p := historySidecar(path)
	if p == "" {
		return 0
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return 0
	}
	var n int
	if _, err := fmt.Sscanf(string(b), "%d", &n); err != nil {
		return 0
	}
	return n
}

// Replay 全量回放(完整日志留盘,含摘要事件)。
func (l *Log) Replay() []sdk.SessionEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]sdk.SessionEvent(nil), l.events...)
}

// Compact 实现 sdk.CompactService(/compact 手动压缩):立即以注册预算折叠滚动摘要,
// 不等待投影超限(自动路径仍由 DeriveMessages 触发)。prompt 仅作指示词记录
// (token-compress 抽取式引擎不消费其内容,不污染事实摘要)。
// 返回:最新累计摘要文本、本次折叠事件跨度(0 = 无可折叠)、错误(压缩器/预算未启用)。
func (l *Log) Compact(prompt string) (string, int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.compressor == nil {
		return "", 0, errors.New("压缩器未注册(token-compress 未装配)")
	}
	if l.budget <= 0 {
		return "", 0, errors.New("压缩预算关闭(data.token_budget_chars=0;自动压缩亦不生效)")
	}
	old := l.compressedUntil
	w := l.compressor.Fold(l.events, l.compressedUntil, l.budget, func(s string) {
		_ = l.appendLocked(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: s})
	})
	l.compressedUntil = w
	folded := w - old
	if folded < 0 {
		folded = 0
	}
	// 回读最新累计摘要(与 projectLocked 同口径:最新 EventSummary 载荷)
	summary := ""
	for _, ev := range l.events {
		if ev.Kind == sdk.EventSummary {
			if s, ok := ev.Payload.(string); ok && s != "" {
				summary = s
			}
		}
	}
	return summary, folded, nil
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
