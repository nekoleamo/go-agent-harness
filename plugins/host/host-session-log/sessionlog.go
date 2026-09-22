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

	"github.com/nekoleamo/go-agent-harness/internal/sessionevents"
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
	// 窗口快照:压缩阈值按窗口比例派生(host-usage-stats 每轮 usage 后广播)。
	// 启动期拿不到那个服务(bundle 里本插件排在它前面,Provide/Inject 无晚绑定),运行期拿值就够。
	dw := c.Subscribe(sdk.EventUsageWindow, func(_ context.Context, ev *sdk.Event) error {
		if w, ok := ev.Payload.(int); ok {
			lg.mu.Lock()
			lg.window = w
			lg.mu.Unlock()
		}
		return nil
	})
	return func() { dw(); lg.Close() }, nil
}

// Log 是会话日志实现。事件并发追加,jsonl 落盘(按项目 key 一个文件)。
// 每次 Append 后经 ctx 广播 session/event(供 UI/遥测实时订阅)。
// 滚动摘要(token-compress):预算与压缩器经 RegisterCompressor 注册;
// 投影超预算时回调压缩器折叠最旧块为 session/summary 摘要事件,
// 原始事件保留(留盘完整),投影见“累计摘要 + 最近块”。
type Log struct {
	mu               sync.Mutex
	events           []sdk.SessionEvent
	seq              atomic.Uint64
	file             *os.File
	path             string
	ctx              sdk.Ctx
	historyLimit     int // -1 禁止 / 0 全部 / N>0 最近 N 条
	budget           int // 投影字符预算(0 = 关闭压缩;由 token-compress 注册时设置)
	compressor       sdk.SessionCompressor
	compressedUntil  int // 已被摘要覆盖的 events 索引水位(-1 = 未压缩)
	window           int // 当前模型上下文窗口(token;0 = 未知;经 usage/window 事件下发)
	lastProjectChars int // 最近一次派生出去的投影字符数
	// pairedProjectChars 产生 pairedUsageSeq 那次请求所发的投影字符数(-1 = 未知):
	// 与实测 PromptTokens 配套才能算“投影涨了多少 ⇒ prompt 涨了多少”(增量口径抵消固定开销)。
	// 不能直接用 lastProjectChars —— 一轮内多次派生(ReAct 每步一次),那拿到的是“上次派生”,
	// 不是“上次请求”。
	pairedProjectChars int
	pairedUsageSeq     uint64
}

func newLog(dir string) *Log {
	return &Log{path: dir, compressedUntil: -1, pairedProjectChars: -1}
}

// SetPath 设置落盘路径(项目级会话隔离在 host-cwd-sessions 中调用;
// 仅改路径不加载 — 恢复历史走 Load,两者分离避免启动期误清内存)。
func (l *Log) SetPath(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.path = path
}

// Load 切换到指定会话:读入 path 的 jsonl 并恢复历史;path 空 = 置空(不落盘)。
// 先读盘成局部状态,全部成功后再一次性提交(path/events/seq 同源)——非 NotExist 失败
// (权限/目录/EIO)不改动现状,避免留下"已切路径 + 空历史"的分叉(Append 落到失败路径)。
func (l *Log) Load(path string) error {
	historyLimit := loadHistory(path)
	var (
		events []sdk.SessionEvent
		maxSeq uint64
	)
	if path != "" {
		f, err := os.Open(path)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("sessionlog: load open %s: %w", path, err)
		}
		if err == nil {
			// 单行上限 16MB:web_fetch 1MB 正文经 JSON 转义膨胀可到 ~2MB(实测 1.9MB),
			// 旧上限 1MB 会让有效事件行触发 token too long 拖垮整个 boot——提高余量;
			// 仍超限(>16MB 的极端单行)按坏行容忍跳过,不阻断历史恢复。
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
			var off, lastStart int64 // off 按"每行含 \n"累计;lastStart 末行起点
			lastOK := false
			for sc.Scan() {
				line := sc.Bytes()
				lastStart = off
				off += int64(len(line)) + 1
				if len(line) == 0 {
					continue
				}
				var ev sdk.SessionEvent
				if err := json.Unmarshal(line, &ev); err != nil {
					lastOK = false
					continue // 容忍坏行(旧格式/半写):不中断恢复
				}
				// Payload 还原:json 反序列化后 Payload 是 map[string]any,按 Kind 二次还原为
				// 具体类型(展示层 ApplySessionEvent/投影 DeriveMessages 全靠类型断言;
				// 不还原则重放 11179 事件投影 0 行——TUI 启动/切换会话历史不可见)。
				ev.Payload = sessionevents.NormalizePayload(&ev)
				if ev.Seq > maxSeq {
					maxSeq = ev.Seq
				}
				events = append(events, ev)
				lastOK = true
			}
			serr := sc.Err()
			size := int64(0)
			if st, serr2 := f.Stat(); serr2 == nil {
				size = st.Size()
			}
			f.Close()
			if serr != nil && !errors.Is(serr, bufio.ErrTooLong) {
				return fmt.Errorf("sessionlog: load read %s: %w", path, serr)
			}
			// 尾部残行(缺 \n = 崩溃/断电半写)修复:能解析 → 补终止符(完整事件只缺 \n);
			// 解析失败 → 截断。否则下一条 Append(O_APPEND)会与残行粘成一行坏行,
			// 残行与紧随其后的新事件双双静默丢失(破坏"模型可见即已记录")。
			if serr == nil && size == off-1 {
				if lastOK {
					if fh, ferr := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600); ferr == nil {
						_, _ = fh.Write([]byte("\n"))
						fh.Close()
					}
				} else if terr := os.Truncate(path, lastStart); terr != nil {
					return fmt.Errorf("sessionlog: 截断残行 %s: %w", path, terr)
				}
			}
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
	l.path = path
	l.events = events
	// 水位从**最后一个摘要事件的索引**恢复,而不是恒 -1:摘要事件本身就在日志里,
	// 恒 -1 会让下一次投影把“最新摘要 + 已被该摘要覆盖的全部原始事件”一起发出去
	// (同一段内容重复占位),再靠一次大折叠收回。
	l.compressedUntil = lastSummaryIndex(events)
	l.pairedProjectChars, l.pairedUsageSeq = -1, 0 // 重启后首次:不估算投影增长,直接用实测值
	l.lastProjectChars = 0
	l.historyLimit = historyLimit
	l.seq.Store(maxSeq) // 序号从历史顶续接(新事件 Seq 不复用)
	return nil
}

// RegisterCompressor 注册滚动摘要压缩器与预算(token-compress 注入;budget<=0 关闭)。
func (l *Log) RegisterCompressor(budget int, c sdk.SessionCompressor) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.budget = budget
	l.compressor = c
	l.compressedUntil = lastSummaryIndex(l.events)
	l.pairedProjectChars, l.pairedUsageSeq = -1, 0
}

// Append 追加事件并落盘(jsonl;落盘失败仅记内存 + 返回错误,不丢事件)。
func (l *Log) Append(ev sdk.SessionEvent) error {
	l.mu.Lock()
	stamped, err := l.appendLocked(ev)
	l.mu.Unlock()
	if err == nil && l.ctx != nil {
		// 广播回填后的事件(与落盘同源):web/events.go 依赖载荷自带 Seq/TS 支撑
		// Last-Event-ID 断线续传与前端时间戳;此前广播未回填的副本 → 帧 id/TS 恒为 0。
		l.ctx.Emit(context.Background(), sdk.EventSession, &stamped, sdk.Emit)
	}
	return err
}

// appendLocked 锁内追加:回填 Seq/TS + 事件入列 + 落盘,返回回填后的事件
// (调用方持有 mu;summary 落盘等内部路径使用)。
func (l *Log) appendLocked(ev sdk.SessionEvent) (sdk.SessionEvent, error) {
	ev.Seq = l.seq.Add(1)
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	l.events = append(l.events, ev)
	f := l.file
	if err := l.ensureFileLocked(); err != nil && f == nil {
		return ev, err
	}
	if l.file != nil {
		line, jerr := json.Marshal(ev)
		if jerr == nil {
			_, jerr = l.file.Write(append(line, '\n'))
		}
		return ev, jerr
	}
	return ev, nil
}

func (l *Log) ensureFileLocked() error {
	if l.path == "" || l.file != nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return fmt.Errorf("sessionlog: mkdir: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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
	// 预算:压缩器可自行按真实占用裁量(BudgetPlanner);否则用注册的固定字符预算。
	budget, trim := l.planLocked(out)
	if l.compressor != nil && budget > 0 && approxChars(out) > budget {
		l.compressedUntil = l.compressor.Fold(l.events, l.compressedUntil, budget,
			func(s string) { _, _ = l.appendLocked(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: s}) })
		out = l.projectLocked(l.compressedUntil)
		if l.historyLimit > 0 && len(out) > l.historyLimit {
			out = out[len(out)-l.historyLimit:]
		}
	}
	// 长单轮善后:水位不得越过最后一个用户轮,单轮内工具结果折叠不掉 ⇒ 截断最旧的(日志不动)。
	if trim > 0 {
		out = clipOldToolResults(out, trim)
	}
	l.lastProjectChars = approxChars(out)
	return out
}

// planLocked 本轮预算与尾巴截断阀值:压缩器实现 BudgetPlanner 则由它裁量,否则用固定预算。
func (l *Log) planLocked(out []sdk.LLMMessage) (budget, trim int) {
	budget = l.budget
	p, ok := l.compressor.(sdk.BudgetPlanner)
	if !ok {
		return budget, 0
	}
	tokens, seq := l.lastUsageLocked()
	if seq != l.pairedUsageSeq {
		l.pairedUsageSeq = seq
		// lastProjectChars > 0 ⇒ 本进程已经派生过一份投影,那就是这次请求发出去的历史部分。
		if l.lastProjectChars > 0 {
			l.pairedProjectChars = l.lastProjectChars
		}
	}
	lastProject := l.pairedProjectChars
	if lastProject < 0 {
		lastProject = approxChars(out) // 未知:不减估算增长 ⇒ 预测就是实测值本身
	}
	d := p.Plan(sdk.CompressInput{
		LastPromptTokens: tokens,
		LastProjectChars: lastProject,
		Window:           l.window,
		ProjectChars:     approxChars(out),
	})
	return d.BudgetChars, d.TrimChars
}

// lastUsageLocked 最近一次实测用量(倒扫 session/usage:Model 与 Usage;无则 0)。返回其 Seq 供配对。
func (l *Log) lastUsageLocked() (promptTokens int, seq uint64) {
	for i := len(l.events) - 1; i >= 0; i-- {
		ev := l.events[i]
		if ev.Kind != sdk.EventUsage {
			continue
		}
		switch p := ev.Payload.(type) {
		case sdk.UsageEvent:
			return p.Usage.PromptTokens, ev.Seq
		case sdk.Usage:
			return p.PromptTokens, ev.Seq
		}
	}
	return 0, 0
}

// lastSummaryIndex 最后一条摘要事件的索引(-1 = 无):它就是已被摘要覆盖的水位。
func lastSummaryIndex(evs []sdk.SessionEvent) int {
	last := -1
	for i, ev := range evs {
		if ev.Kind == sdk.EventSummary {
			last = i
		}
	}
	return last
}

// 截断保留的头/尾字符数(头部是命令/结果开头,尾部常带错误与退出状态)。
const (
	clipHeadChars = 1200
	clipTailChars = 300
)

// clipOldToolResults 投影层截断(日志一字不动):从**最旧**的工具结果起改写成“头 + 省略标记 + 尾”,
// 直到总字符回到 target 内;最近一条工具结果永不截(它大概率是模型接下来要看的东西)。
// 只截 Content 而不丢消息 —— tool_call 与 tool 结果必须成对,丢一条就破坏协议。
func clipOldToolResults(msgs []sdk.LLMMessage, target int) []sdk.LLMMessage {
	total := approxChars(msgs)
	if total <= target || target <= 0 {
		return msgs
	}
	lastTool := -1
	for i, m := range msgs {
		if m.Role == sdk.RoleTool {
			lastTool = i
		}
	}
	out := append([]sdk.LLMMessage(nil), msgs...)
	for i := range out {
		if total <= target {
			break
		}
		if out[i].Role != sdk.RoleTool || i == lastTool {
			continue
		}
		clipped, saved := clipContent(out[i].Content, clipHeadChars, clipTailChars)
		if saved <= 0 {
			continue
		}
		out[i].Content = clipped
		total -= saved
	}
	return out
}

// clipContent 保留头部 head 与尾部 tail 个字符,中间换成省略标记;返回新文本与被去掉的字符数。
// 已是够短的不动(返回 saved=0)。
func clipContent(s string, head, tail int) (string, int) {
	r := []rune(s)
	if len(r) <= head+tail+64 {
		return s, 0
	}
	dropped := len(r) - head - tail
	kept := make([]rune, 0, head+tail+32)
	kept = append(kept, r[:head]...)
	kept = append(kept, []rune(fmt.Sprintf("\n…[截断 %d 字符]…\n", dropped))...)
	kept = append(kept, r[len(r)-tail:]...)
	return string(kept), dropped
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
	if err := os.WriteFile(p, []byte(fmt.Sprint(n)), 0o600); err != nil {
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
	_, planned := l.compressor.(sdk.BudgetPlanner)
	if l.budget <= 0 && !planned {
		return "", 0, errors.New("压缩预算关闭(data.token_budget_chars=0;自动压缩亦不生效)")
	}
	old := l.compressedUntil
	budget, _ := l.planLocked(l.projectLocked(l.compressedUntil))
	if budget <= 0 {
		return "", 0, errors.New("压缩预算关闭(data.token_budget_chars=0;自动压缩亦不生效)")
	}
	w := l.compressor.Fold(l.events, l.compressedUntil, budget, func(s string) {
		_, _ = l.appendLocked(sdk.SessionEvent{Kind: sdk.EventSummary, Payload: s})
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
